// Package resident serves a shared La Roca service for one data directory.
package resident

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/thellmwhisperer/la-roca/internal/distribution/mcpplug"
	"github.com/thellmwhisperer/la-roca/internal/provider/service"
	transport "github.com/thellmwhisperer/la-roca/pkg/resident"
	"github.com/thellmwhisperer/la-roca/pkg/vectorresident"
)

type Server struct {
	service  *service.Service
	build    mcpplug.Build
	dataDir  string
	started  time.Time
	clients  atomic.Int64
	listener net.Listener
	vectorMu sync.Mutex
	vector   *vectorresident.Client

	home Home
	// mu guards retired and running; drained closes once a retired resident has
	// nothing running.
	mu      sync.Mutex
	retired bool
	running int
	drained chan struct{}
	drain   sync.Once
}

// Home is what a resident answers with: the bytes of config.toml and the roca
// executable it was started from.
type Home struct {
	configPath, executable string
	config                 [sha256.Size]byte
	configRead             bool
	binary                 os.FileInfo
}

func RecordHome(configPath string) Home {
	executable, _ := os.Executable()
	return readHome(configPath, executable)
}

func readHome(configPath, executable string) Home {
	home := Home{configPath: configPath, executable: executable}
	if raw, err := os.ReadFile(configPath); err == nil {
		home.config, home.configRead = sha256.Sum256(raw), true
	}
	home.binary, _ = os.Stat(executable)
	return home
}

// Changed reports whether config.toml or the executable on disk is not the one
// recorded. An executable that cannot be read is a change.
func (h Home) Changed() bool {
	now := readHome(h.configPath, h.executable)
	return now.config != h.config || now.configRead != h.configRead ||
		h.binary == nil || now.binary == nil || !os.SameFile(h.binary, now.binary) ||
		h.binary.Size() != now.binary.Size() || !h.binary.ModTime().Equal(now.binary.ModTime())
}

func Run(ctx context.Context, svc *service.Service, build mcpplug.Build, home Home) error {
	dataDir := svc.DataDir()
	socket := transport.SocketPath(dataDir)
	if err := os.MkdirAll(filepath.Dir(socket), 0o700); err != nil {
		return fmt.Errorf("create resident directory: %w", err)
	}
	if err := os.Remove(socket); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("replace resident socket: %w", err)
	}
	// The socket is born 0600, so a client dialing during startup never sees it with group or other bits.
	old := setUmask(0o177)
	listener, err := net.Listen("unix", socket)
	setUmask(old)
	if err != nil {
		return fmt.Errorf("listen for resident clients: %w", err)
	}
	// Closing the listener unlinks its socket, once, so a retired resident never
	// removes the socket of the one that replaced it.
	defer listener.Close()
	if err := os.Chmod(socket, 0o600); err != nil {
		return fmt.Errorf("restrict resident socket: %w", err)
	}
	server := &Server{service: svc, build: build, dataDir: dataDir, started: time.Now(), listener: listener,
		home: home, drained: make(chan struct{})}
	defer server.closeVector()
	go func() {
		<-ctx.Done()
		_ = listener.Close()
	}()
	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if server.isRetired() {
				<-server.drained
				return nil
			}
			return err
		}
		server.clients.Add(1)
		go func() {
			defer server.clients.Add(-1)
			_ = server.handle(ctx, conn)
		}()
	}
}

func (s *Server) handle(ctx context.Context, conn net.Conn) error {
	defer conn.Close()
	reader := bufio.NewReader(conn)
	line, err := reader.ReadBytes('\n')
	if err != nil {
		return err
	}
	var probe transport.Request
	readOnly := false
	// A CLI call runs with the home its connect found; an MCP session is
	// checked again before each request.
	admitted := 0
	defer func() { s.finish(admitted) }()
	if json.Unmarshal(bytes.TrimSpace(line), &probe) == nil && probe.Op == "connect" {
		admitted = 1
		if !s.admit(true) {
			return s.respond(conn, transport.Response{Error: transport.ErrRetired.Error()})
		}
		if probe.DBPath != "" {
			requested, reqErr := filepath.Abs(probe.DBPath)
			serving, svcErr := filepath.Abs(s.service.ConfiguredDBPath())
			if reqErr != nil || svcErr != nil || requested != serving {
				return s.respond(conn, transport.Response{Error: transport.ErrDatabaseMismatch.Error()})
			}
		}
		readOnly = probe.ReadOnly
		if err := s.respond(conn, transport.Response{Result: json.RawMessage("{}")}); err != nil {
			return err
		}
		line, err = reader.ReadBytes('\n')
		if err != nil {
			return err
		}
		probe = transport.Request{}
	}
	if json.Unmarshal(bytes.TrimSpace(line), &probe) == nil && probe.Op != "" {
		if readOnly && probe.Op == "store" {
			return s.respond(conn, transport.Response{Error: "La Roca is in read-only mode: this operation writes"})
		}
		return s.handleCall(ctx, conn, line)
	}
	s.finish(admitted)
	admitted = 0
	transportConn := &mcpConn{
		Conn: conn, server: s, running: map[string]bool{},
		reader: bufio.NewReader(io.MultiReader(bytes.NewReader(line), reader)),
	}
	defer transportConn.finishAll()
	err = mcpplug.ServeConnection(ctx, s.service, s.build, transportConn, readOnly)
	return err
}

// admit reports whether the home this resident opened is still the one on
// disk. A request counts, run or refused, until its caller finishes it, so the
// resident exits only after its last answer. The first change retires the
// resident: its socket goes at once, so the next client starts a new one.
func (s *Server) admit(request bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if request {
		s.running++
	}
	if !s.retired && s.home.Changed() {
		s.retired = true
		_ = s.listener.Close()
		s.settle()
	}
	return !s.retired
}

func (s *Server) finish(requests int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.running -= requests
	s.settle()
}

// settle runs with mu held.
func (s *Server) settle() {
	if s.retired && s.running == 0 {
		s.drain.Do(func() { close(s.drained) })
	}
}

func (s *Server) isRetired() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.retired
}

func (s *Server) handleCall(ctx context.Context, conn net.Conn, first []byte) error {
	var request transport.Request
	if err := json.Unmarshal(bytes.TrimSpace(first), &request); err != nil {
		return s.respond(conn, transport.Response{Error: "decode resident request: " + err.Error()})
	}
	result, err := s.call(ctx, request.Op, request.Args)
	if err != nil {
		return s.respond(conn, transport.Response{Error: err.Error()})
	}
	return s.respond(conn, transport.Response{Result: result})
}

func (s *Server) respond(conn net.Conn, response transport.Response) error {
	return json.NewEncoder(conn).Encode(response)
}

func (s *Server) call(ctx context.Context, op string, raw json.RawMessage) (json.RawMessage, error) {
	decode := func(target any) error {
		if len(raw) == 0 {
			raw = []byte("{}")
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		return decoder.Decode(target)
	}
	var value any
	switch op {
	case "exec":
		var request service.ExecRequest
		if err := decode(&request); err != nil {
			return nil, err
		}
		var callErr error
		value, callErr = s.service.Exec(ctx, request)
		if callErr != nil {
			return nil, callErr
		}
	case "query":
		var request service.SearchRequest
		if err := decode(&request); err != nil {
			return nil, err
		}
		var callErr error
		value, callErr = s.service.Search(ctx, request)
		if callErr != nil {
			return nil, callErr
		}
	case "vector_query":
		var request vectorresident.Request
		if err := decode(&request); err != nil {
			return nil, err
		}
		result, err := s.vectorQuery(ctx, request)
		if err != nil {
			return nil, err
		}
		value = result
	case "store":
		var request service.StoreRequest
		if err := decode(&request); err != nil {
			return nil, err
		}
		result, err := s.service.Store(ctx, request)
		if err != nil {
			return nil, err
		}
		_ = s.service.Checkpoint(ctx)
		value = result
	case "health":
		var request service.HealthRequest
		if err := decode(&request); err != nil {
			return nil, err
		}
		var callErr error
		value, callErr = s.service.Health(ctx, request)
		if callErr != nil {
			return nil, callErr
		}
	case "handoff_latest":
		var request struct {
			Project string `json:"project"`
		}
		if err := decode(&request); err != nil {
			return nil, err
		}
		var callErr error
		value, callErr = s.service.LatestHandoffs(ctx, request.Project)
		if callErr != nil {
			return nil, callErr
		}
	case "handoff_all":
		var request struct {
			Since     time.Time `json:"since"`
			HeadChars int       `json:"head_chars"`
		}
		if err := decode(&request); err != nil {
			return nil, err
		}
		var callErr error
		value, callErr = s.service.LatestHandoffsByProject(ctx, request.Since, request.HeadChars)
		if callErr != nil {
			return nil, callErr
		}
	case "pill_show":
		var request struct{ Project, Slug string }
		if err := decode(&request); err != nil {
			return nil, err
		}
		var callErr error
		value, callErr = s.service.ShowPill(ctx, request.Project, request.Slug)
		if callErr != nil {
			return nil, callErr
		}
	case "pill_list":
		var request struct {
			Project string `json:"project"`
		}
		if err := decode(&request); err != nil {
			return nil, err
		}
		var callErr error
		value, callErr = s.service.ListPills(ctx, request.Project)
		if callErr != nil {
			return nil, callErr
		}
	case "doctor":
		var callErr error
		value, callErr = s.service.Doctor(ctx)
		if callErr != nil {
			return nil, callErr
		}
	case "status":
		value = s.status()
	default:
		return nil, fmt.Errorf("unknown resident operation %q", op)
	}
	if value == nil {
		return nil, fmt.Errorf("resident operation %q returned no result", op)
	}
	encoded, err := json.Marshal(value)
	return encoded, err
}

func (s *Server) vectorQuery(ctx context.Context, request vectorresident.Request) (json.RawMessage, error) {
	if !s.service.VectorEnabled() {
		return nil, fmt.Errorf("semantic search is disabled; enable features.vector in config.toml")
	}
	s.vectorMu.Lock()
	client := s.vector
	if client == nil {
		binary := vectorresident.PayloadPath()
		if binary == "" {
			s.vectorMu.Unlock()
			return nil, fmt.Errorf("semantic search resident is not installed")
		}
		host, err := os.Executable()
		if err != nil {
			s.vectorMu.Unlock()
			return nil, fmt.Errorf("locate roca for semantic search: %w", err)
		}
		client, err = vectorresident.ConnectCurrent(ctx, vectorresident.Options{
			Binary: binary, HostBinary: host, DataDir: s.dataDir,
			DBPath: s.service.DB().Path(), PluginRoot: s.service.PluginDir(),
			StateDir: filepath.Join(s.dataDir, "plugins", "roca-vector", "state"), Status: os.Stderr,
		})
		if err != nil {
			s.vectorMu.Unlock()
			return nil, err
		}
		s.vector = client
	}
	s.vectorMu.Unlock()
	result, err := client.Query(ctx, request)
	if client.Failed() {
		s.vectorMu.Lock()
		if s.vector == client {
			s.vector = nil
		}
		s.vectorMu.Unlock()
		_ = client.Close()
	}
	return result, err
}

func (s *Server) closeVector() {
	s.vectorMu.Lock()
	defer s.vectorMu.Unlock()
	if s.vector != nil {
		_ = s.vector.Close()
		s.vector = nil
	}
}

func (s *Server) status() transport.Status {
	options := s.service.Options()
	return transport.Status{
		PID: os.Getpid(), StartedAt: s.started, UptimeMS: time.Since(s.started).Milliseconds(),
		AttachedClients: int(s.clients.Load()), OpenConnections: s.service.OpenConnections(), WAL: walSizes(s.dataDir),
		Version: s.build.Version, Commit: s.build.Commit, ConfigPath: options.ConfigPath,
		Vector: s.service.VectorEnabled(), RocaOps: options.RocaOpsEnabled, Serving: string(s.service.ServingLayout()),
	}
}

func walSizes(root string) map[string]int64 {
	result := map[string]int64{}
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(entry.Name(), ".db-wal") {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err == nil {
			result[filepath.ToSlash(rel)] = info.Size()
		}
		return nil
	})
	return result
}

// mcpConn hands the MCP server one line at a time. A request that arrives once
// the resident retired is refused with its id and never reaches the server; a
// request that runs counts until its response is written.
type mcpConn struct {
	net.Conn
	server  *Server
	reader  *bufio.Reader
	line    []byte
	mu      sync.Mutex
	running map[string]bool
}

type envelope struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
}

func (c *mcpConn) Read(p []byte) (int, error) {
	for len(c.line) == 0 {
		line, err := c.reader.ReadBytes('\n')
		if len(line) == 0 {
			return 0, err
		}
		var message envelope
		_ = json.Unmarshal(line, &message)
		call := len(message.ID) > 0 && message.Method != ""
		if message.Method != "" && !c.server.admit(call) {
			if call {
				refusal, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": message.ID,
					"error": map[string]any{"code": -32000, "message": transport.ErrRetired.Error()}})
				// A refusal that cannot be written is a broken connection the next read reports.
				_, _ = c.Write(append(refusal, '\n'))
				c.server.finish(1)
			}
			continue
		}
		if call {
			c.mu.Lock()
			c.running[string(message.ID)] = true
			c.mu.Unlock()
		}
		c.line = line
	}
	n := copy(p, c.line)
	c.line = c.line[n:]
	return n, nil
}

func (c *mcpConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var message envelope
	if json.Unmarshal(p, &message) == nil && message.Method == "" && c.running[string(message.ID)] {
		delete(c.running, string(message.ID))
		defer c.server.finish(1)
	}
	return c.Conn.Write(p)
}

// finishAll releases what a closed connection will never answer.
func (c *mcpConn) finishAll() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.server.finish(len(c.running))
	clear(c.running)
}

var _ io.ReadWriteCloser = (*mcpConn)(nil)
