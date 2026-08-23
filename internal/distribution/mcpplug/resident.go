package mcpplug

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/thellmwhisperer/la-roca/internal/provider/service"
)

var vectorQueryTool = &mcp.Tool{
	Name: "roca_vector_query",
	Description: "Search local memory by meaning. Same job as `roca vector query`: " +
		"pass a short first-person phrase or a bare word, and how many hits (default 10, max 100). " +
		"The session prepares the embedding model in the background so the first call does not wait on startup.",
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
	cmd       *exec.Cmd
	encoder   *json.Encoder
	closeOnce sync.Once
	queryMu   sync.Mutex
	stateMu   sync.Mutex
	ready     chan struct{}
	responses chan residentEnvelope
	failures  chan error
	done      chan error
	readyErr  error
	prewarmMS int64
	nextID    int64
}

func startResidentVector(ctx context.Context, svc *service.Service) (*residentVector, error) {
	binary := vectorPayloadPath()
	if binary == "" {
		return nil, nil
	}
	args := []string{"_resident"}
	if path := svc.DB().Path(); path != "" {
		args = append([]string{"--db-path", path}, args...)
	}
	command := exec.CommandContext(ctx, binary, args...)
	command.Env = append(os.Environ(),
		"ROCA_VECTOR_PLUGIN_ROOT="+svc.PluginDir(),
	)
	if dataDir := svc.DataDir(); dataDir != "" {
		command.Env = append(command.Env,
			"ROCA_VECTOR_STATE_DIR="+filepath.Join(dataDir, "plugins", "roca-vector", "state"),
		)
	}
	stdin, err := command.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("open semantic search input: %w", err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		stdin.Close()
		return nil, fmt.Errorf("open semantic search output: %w", err)
	}
	command.Stderr = os.Stderr
	resident := &residentVector{
		stdin: stdin, cmd: command, encoder: json.NewEncoder(stdin),
		ready: make(chan struct{}), responses: make(chan residentEnvelope, 4),
		failures: make(chan error, 1), done: make(chan error, 1),
	}
	if err := command.Start(); err != nil {
		stdin.Close()
		return nil, fmt.Errorf("start semantic search: %w", err)
	}
	go resident.decode(stdout)
	go func() { resident.done <- command.Wait() }()
	return resident, nil
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
			r.responses <- response
		case "error":
			if response.Stage == "prewarm" {
				r.markReady(fmt.Errorf("%s", firstNonEmpty(response.Error, response.Message, "semantic search is not ready")))
				continue
			}
			r.responses <- response
		default:
			r.fail(fmt.Errorf("semantic search protocol: unknown message %q", response.Kind))
			return
		}
	}
	if err := scanner.Err(); err != nil {
		r.fail(err)
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
	select {
	case r.failures <- err:
	default:
	}
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
	r.queryMu.Lock()
	defer r.queryMu.Unlock()
	r.nextID++
	request := map[string]any{"id": r.nextID, "op": "query", "query": query, "k": in.K}
	if strings.TrimSpace(in.Databases) != "" {
		request["databases"] = in.Databases
	}
	if err := r.encoder.Encode(request); err != nil {
		return nil, nil, fmt.Errorf("ask semantic search: %w", err)
	}
	var response residentEnvelope
	select {
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	case err := <-r.failures:
		return nil, nil, err
	case response = <-r.responses:
	}
	if response.ID != 0 && response.ID != r.nextID {
		return nil, nil, fmt.Errorf("semantic search response id %d, want %d", response.ID, r.nextID)
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
	r.closeOnce.Do(func() { _ = r.stdin.Close() })
	return <-r.done
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
