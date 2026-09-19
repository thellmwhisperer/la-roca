// Package resident owns the one long-lived La Roca service on a machine.
package resident

import (
	"bufio"
	"bytes"
	"context"
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
}

func Run(ctx context.Context, svc *service.Service, build mcpplug.Build) error {
	dataDir := svc.DataDir()
	socket := transport.SocketPath(dataDir)
	if err := os.MkdirAll(filepath.Dir(socket), 0o700); err != nil {
		return fmt.Errorf("create resident directory: %w", err)
	}
	if err := os.Remove(socket); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("replace resident socket: %w", err)
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		return fmt.Errorf("listen for resident clients: %w", err)
	}
	defer listener.Close()
	defer os.Remove(socket)
	if err := os.Chmod(socket, 0o600); err != nil {
		return fmt.Errorf("restrict resident socket: %w", err)
	}
	server := &Server{service: svc, build: build, dataDir: dataDir, started: time.Now(), listener: listener}
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
	if json.Unmarshal(bytes.TrimSpace(line), &probe) == nil && probe.Op == "connect" {
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
	transportConn := &bufferedConn{
		Conn:   conn,
		reader: io.MultiReader(bytes.NewReader(line), reader),
	}
	err = mcpplug.ServeConnection(ctx, s.service, s.build, transportConn, readOnly)
	return err
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
	// The service methods above return typed zero values alongside errors. Run
	// them again through a small typed helper so no operation can accidentally
	// hide its error behind a successful JSON null.
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
	return transport.Status{
		PID: os.Getpid(), StartedAt: s.started, UptimeMS: time.Since(s.started).Milliseconds(),
		AttachedClients: int(s.clients.Load()), OpenConnections: s.service.OpenConnections(), WAL: walSizes(s.dataDir),
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

type bufferedConn struct {
	net.Conn
	reader io.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) { return c.reader.Read(p) }

var _ io.ReadWriteCloser = (*bufferedConn)(nil)
