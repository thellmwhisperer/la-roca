package mcpplug

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/thellmwhisperer/la-roca/internal/provider/service"
	"github.com/thellmwhisperer/la-roca/internal/securefile"
)

var vectorQueryTool = &mcp.Tool{
	Name: "roca_vector_query",
	Description: "Search local memory by meaning. Same job as `roca vector query`: " +
		"pass a short first-person phrase or a bare word, and how many hits (default 10, max 100). " +
		"The machine keeps one shared embedding process so every MCP session reuses it.",
}

type vectorQueryArgs struct {
	Query     string `json:"query" jsonschema:"short first-person phrase or bare word"`
	K         int    `json:"k,omitempty" jsonschema:"number of nearest results, default 10, max 100"`
	Databases string `json:"databases,omitempty" jsonschema:"comma list of attached database names (corpus,ops), or all"`
}

type residentEnvelope struct {
	Kind      string          `json:"kind"`
	Stage     string          `json:"stage,omitempty"`
	ID        int64           `json:"id,omitempty"`
	Message   string          `json:"message,omitempty"`
	Error     string          `json:"error,omitempty"`
	ElapsedMS int64           `json:"elapsed_ms,omitempty"`
	Result    json.RawMessage `json:"result,omitempty"`
	Extra     map[string]any  `json:"extra,omitempty"`
}

type residentVector struct {
	stdin     io.WriteCloser
	conn      io.Closer
	encoder   *json.Encoder
	status    io.Writer
	closeOnce sync.Once
	writeMu   sync.Mutex
	stateMu   sync.Mutex
	pendingMu sync.Mutex
	ready     chan struct{}
	failed    chan struct{}
	readyErr  error
	failure   error
	prewarmMS int64
	nextID    int64
	pending   map[int64]chan residentEnvelope
	closing   bool
}

func startResidentVector(ctx context.Context, svc *service.Service) (*residentVector, error) {
	binary := vectorPayloadPath()
	if binary == "" {
		return nil, nil
	}
	socket, lockPath := residentSocketPaths(svc)
	conn, err := dialOrSpawnResident(ctx, svc, binary, socket, lockPath)
	if err != nil {
		return nil, err
	}
	resident := &residentVector{
		stdin: conn, conn: conn, encoder: json.NewEncoder(conn),
		status: os.Stderr, ready: make(chan struct{}), failed: make(chan struct{}),
		pending: make(map[int64]chan residentEnvelope),
	}
	go resident.decode(conn)
	return resident, nil
}

func residentSocketPaths(svc *service.Service) (socket, lock string) {
	dir := ""
	if svc != nil {
		dir = svc.DataDir()
	}
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".roca")
	}
	lock = filepath.Join(dir, "vector-resident.lock")
	if override := strings.TrimSpace(os.Getenv("ROCA_VECTOR_RESIDENT_SOCKET")); override != "" {
		return clampUnixSocketPath(override), override + ".lock"
	}
	return clampUnixSocketPath(filepath.Join(dir, "vector-resident.sock")), lock
}

func clampUnixSocketPath(path string) string {
	if runtime.GOOS == "windows" || len(path) < 100 {
		return path
	}
	sum := sha256.Sum256([]byte(path))
	return filepath.Join("/tmp", "roca-vr-"+hex.EncodeToString(sum[:8])+".sock")
}

func dialOrSpawnResident(ctx context.Context, svc *service.Service, binary, socket, lockPath string) (io.ReadWriteCloser, error) {
	if conn, err := dialResidentSocket(socket); err == nil {
		return conn, nil
	}
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		return nil, fmt.Errorf("create semantic search resident directory: %w", err)
	}
	release, err := securefile.Lock(lockPath)
	if err != nil {
		return nil, fmt.Errorf("lock semantic search resident: %w", err)
	}
	defer func() { _ = release() }()
	if conn, err := dialResidentSocket(socket); err == nil {
		return conn, nil
	}
	if err := spawnResidentVector(svc, binary, socket); err != nil {
		return nil, err
	}
	return waitResidentSocket(ctx, socket)
}

func dialResidentSocket(socket string) (io.ReadWriteCloser, error) {
	return dialUnixTimeout(socket, 200*time.Millisecond)
}

func waitResidentSocket(ctx context.Context, socket string) (io.ReadWriteCloser, error) {
	deadline := time.Now().Add(10 * time.Second)
	var last error
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		conn, err := dialResidentSocket(socket)
		if err == nil {
			return conn, nil
		}
		last = err
		time.Sleep(20 * time.Millisecond)
	}
	if last == nil {
		last = fmt.Errorf("timed out")
	}
	return nil, fmt.Errorf("wait for semantic search resident: %w", last)
}

func spawnResidentVector(svc *service.Service, binary, socket string) error {
	args := []string{"_resident", "--listen", socket}
	if svc != nil {
		if path := svc.DB().Path(); path != "" {
			args = append([]string{"--db-path", path}, args...)
		}
	}
	command := exec.Command(binary, args...)
	command.Env = append(os.Environ(), residentSpawnEnv(svc)...)
	command.SysProcAttr = detachedResidentAttr()
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		return fmt.Errorf("open semantic search input: %w", err)
	}
	defer devNull.Close()
	logFile, err := residentLogFile(svc, socket)
	if err != nil {
		return err
	}
	defer logFile.Close()
	command.Stdin, command.Stdout, command.Stderr = devNull, logFile, logFile
	if err := command.Start(); err != nil {
		return fmt.Errorf("start semantic search: %w", err)
	}
	if command.Process != nil {
		_ = command.Process.Release()
	}
	return nil
}

func residentSpawnEnv(svc *service.Service) []string {
	env := []string{}
	if svc == nil {
		return env
	}
	if root := svc.PluginDir(); root != "" {
		env = append(env, "ROCA_VECTOR_PLUGIN_ROOT="+root)
	}
	if dataDir := svc.DataDir(); dataDir != "" {
		env = append(env, "ROCA_VECTOR_STATE_DIR="+filepath.Join(dataDir, "plugins", "roca-vector", "state"))
	}
	return env
}

func residentLogFile(svc *service.Service, socket string) (*os.File, error) {
	dir := filepath.Dir(socket)
	if svc != nil && svc.DataDir() != "" {
		dir = filepath.Join(svc.DataDir(), "logs")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create semantic search log directory: %w", err)
	}
	file, err := os.OpenFile(filepath.Join(dir, "vector-resident.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open semantic search log: %w", err)
	}
	return file, nil
}

func vectorPayloadPath() string {
	if override := strings.TrimSpace(os.Getenv("ROCA_VECTOR_RESIDENT_BINARY")); override != "" {
		return override
	}
	name := "roca-vector"
	if runtime.GOOS == "windows" {
		name = "roca-vector.exe"
	}
	if exe, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(exe), name)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	path, err := exec.LookPath(name)
	if err != nil {
		return ""
	}
	return path
}

func (r *residentVector) decode(reader io.Reader) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		var response residentEnvelope
		if err := json.Unmarshal(scanner.Bytes(), &response); err != nil {
			r.fail(fmt.Errorf("semantic search protocol: %w", err))
			return
		}
		switch response.Kind {
		case "progress":
			if r.status != nil && strings.TrimSpace(response.Message) != "" {
				fmt.Fprintln(r.status, response.Message)
			}
			continue
		case "result":
			if response.Stage == "prewarm" {
				if response.Extra != nil {
					if ms, ok := response.Extra["prewarm_ms"].(float64); ok {
						r.stateMu.Lock()
						r.prewarmMS = int64(ms)
						r.stateMu.Unlock()
					}
				}
				r.markReady(nil)
				continue
			}
			r.route(response)
		case "error":
			if response.Stage == "prewarm" {
				r.markReady(fmt.Errorf("%s", firstNonEmpty(response.Error, response.Message, "semantic search is not ready")))
				continue
			}
			r.route(response)
		default:
			r.fail(fmt.Errorf("semantic search protocol: unknown message %q", response.Kind))
			return
		}
	}
	if err := scanner.Err(); err != nil {
		r.fail(err)
		return
	}
	r.stateMu.Lock()
	closing := r.closing
	r.stateMu.Unlock()
	if !closing {
		r.fail(io.ErrUnexpectedEOF)
	}
}

func (r *residentVector) route(response residentEnvelope) {
	r.pendingMu.Lock()
	ch := r.pending[response.ID]
	r.pendingMu.Unlock()
	if ch != nil {
		select {
		case ch <- response:
		default:
		}
	}
}

func (r *residentVector) markReady(err error) {
	r.stateMu.Lock()
	defer r.stateMu.Unlock()
	if r.readyErr == nil {
		r.readyErr = err
	}
	select {
	case <-r.ready:
	default:
		close(r.ready)
	}
}

func (r *residentVector) fail(err error) {
	r.markReady(err)
	r.stateMu.Lock()
	if r.failure == nil {
		r.failure = err
		close(r.failed)
	}
	r.stateMu.Unlock()
}

func (r *residentVector) waitReady(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-r.ready:
		r.stateMu.Lock()
		defer r.stateMu.Unlock()
		return r.readyErr
	}
}

func (r *residentVector) call(ctx context.Context, _ *mcp.CallToolRequest,
	in vectorQueryArgs) (*mcp.CallToolResult, any, error) {
	query := strings.TrimSpace(in.Query)
	if query == "" {
		return nil, nil, fmt.Errorf("query is required")
	}
	if in.K == 0 {
		in.K = 10
	}
	if in.K < 1 || in.K > 100 {
		return nil, nil, fmt.Errorf("k must be between 1 and 100")
	}
	if err := r.waitReady(ctx); err != nil {
		return nil, nil, err
	}
	r.pendingMu.Lock()
	r.nextID++
	id := r.nextID
	responseCh := make(chan residentEnvelope, 1)
	r.pending[id] = responseCh
	r.pendingMu.Unlock()
	defer func() {
		r.pendingMu.Lock()
		delete(r.pending, id)
		r.pendingMu.Unlock()
	}()
	request := map[string]any{"id": id, "op": "query", "query": query, "k": in.K}
	if strings.TrimSpace(in.Databases) != "" {
		request["databases"] = in.Databases
	}
	r.writeMu.Lock()
	err := r.encoder.Encode(request)
	r.writeMu.Unlock()
	if err != nil {
		return nil, nil, fmt.Errorf("ask semantic search: %w", err)
	}
	var response residentEnvelope
	select {
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	case <-r.failed:
		r.stateMu.Lock()
		err := r.failure
		r.stateMu.Unlock()
		return nil, nil, err
	case response = <-responseCh:
	}
	if response.Kind == "error" || response.Error != "" {
		return nil, nil, fmt.Errorf("%s", firstNonEmpty(response.Error, response.Message, "semantic search failed"))
	}
	text := string(response.Result)
	if text == "" {
		text = response.Message
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}, nil, nil
}

func (r *residentVector) Close() error {
	r.stateMu.Lock()
	r.closing = true
	r.stateMu.Unlock()
	r.closeOnce.Do(func() {
		if r.stdin != nil {
			_ = r.stdin.Close()
		} else if r.conn != nil {
			_ = r.conn.Close()
		}
	})
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
