package mcpplug

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/thellmwhisperer/la-roca/internal/provider/service"
	"github.com/thellmwhisperer/la-roca/pkg/vectorresident"
)

var vectorQueryTool = &mcp.Tool{
	Name: "roca_vector_query",
	Description: "Fast semantic search over local memory. Pass a short first-person " +
		"phrase or a bare word, and how many hits (default 10, max 100). This is the " +
		"semantic leg alone: no full-text scan and no fusion. Use roca_query only when " +
		"exact terms matter.",
}

type vectorQueryArgs struct {
	Query     string `json:"query,omitempty" jsonschema:"short first-person phrase or bare word"`
	Question  string `json:"question,omitempty" jsonschema:"alias of query"`
	Text      string `json:"text,omitempty" jsonschema:"alias of query"`
	K         int    `json:"k,omitempty" jsonschema:"number of nearest results, default 10, max 100"`
	Limit     int    `json:"limit,omitempty" jsonschema:"alias of k"`
	Top       int    `json:"top,omitempty" jsonschema:"alias of k"`
	Databases string `json:"databases,omitempty" jsonschema:"comma list of attached database names (corpus,ops), or all"`
}

func (a vectorQueryArgs) queryText() string {
	return firstNonEmpty(a.Query, a.Question, a.Text)
}

func (a vectorQueryArgs) hitCount() int {
	return firstPositive(a.K, a.Limit, a.Top)
}

type residentVector struct {
	*vectorresident.Client
}

// MCP already dials the shared resident socket. The 6.9s vs 1.4s CLI gap
// from the help audit is left out of scope: the spawn-or-reuse path is the
// same, and a deeper process-sharing fix is not a bounded change.

func startResidentVector(ctx context.Context, svc *service.Service) (*residentVector, error) {
	binary := vectorresident.PayloadPath()
	if binary == "" {
		return nil, nil
	}
	conn, err := dialOrSpawnResident(ctx, svc, binary, "", "")
	if err != nil {
		return nil, err
	}
	return &residentVector{Client: vectorresident.NewClient(conn, os.Stderr)}, nil
}

func residentSocketPaths(svc *service.Service) (socket, lock string) {
	dir := ""
	if svc != nil {
		dir = svc.DataDir()
	}
	return vectorresident.SocketPaths(dir)
}

func dialOrSpawnResident(ctx context.Context, svc *service.Service, binary, socket, lockPath string) (io.ReadWriteCloser, error) {
	opts := residentOptions(svc, binary)
	opts.Socket = socket
	opts.Lock = lockPath
	if strings.TrimSpace(socket) == "" {
		opts.Socket, opts.Lock = residentSocketPaths(svc)
	} else if strings.TrimSpace(lockPath) == "" {
		opts.Lock = socket + ".lock"
	}
	return vectorresident.DialOrSpawn(ctx, opts)
}

func residentOptions(svc *service.Service, binary string) vectorresident.Options {
	opts := vectorresident.Options{Binary: binary, Status: os.Stderr}
	if svc == nil {
		return opts
	}
	opts.DataDir = svc.DataDir()
	if path := svc.DB().Path(); path != "" {
		opts.DBPath = path
	}
	if root := svc.PluginDir(); root != "" {
		opts.PluginRoot = root
	}
	if opts.DataDir != "" {
		opts.StateDir = filepath.Join(opts.DataDir, "plugins", "roca-vector", "state")
	}
	return opts
}

func (r *residentVector) waitReady(ctx context.Context) error {
	if r == nil || r.Client == nil {
		return fmt.Errorf("semantic search is not ready")
	}
	return r.WaitReady(ctx)
}

func (r *residentVector) call(ctx context.Context, _ *mcp.CallToolRequest,
	in vectorQueryArgs) (*mcp.CallToolResult, any, error) {
	if r == nil || r.Client == nil {
		return nil, nil, fmt.Errorf("semantic search is not ready")
	}
	if err := r.WaitReady(ctx); err != nil {
		return nil, nil, err
	}
	query := in.queryText()
	if query == "" {
		return nil, nil, fmt.Errorf("a query is required")
	}
	raw, err := r.Query(ctx, vectorresident.Request{Query: query, K: in.hitCount(), Databases: in.Databases})
	if err != nil {
		return nil, nil, err
	}
	text := string(raw)
	if text == "" || text == "null" {
		text = ""
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}, nil, nil
}
