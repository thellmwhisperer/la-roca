package mcpplug

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/thellmwhisperer/la-roca/internal/provider/service"
)

// The five handlers. Each one is a single call into the service and nothing
// else, and passthrough_test.go fails when that stops being true.
//
// The shape is the SDK's: what the service returns becomes the structured half
// of the result, and the error it returns is packed as a tool error carrying
// the words the service wrote. That is why the read-only refusal, an unknown
// template and a missing argument all reach the agent as something it can read
// and act on, instead of as a dead session.

func (p *plug) exec(ctx context.Context, _ *mcp.CallToolRequest,
	in execArgs) (*mcp.CallToolResult, service.ExecResult, error) {
	return through(p.svc.Exec(ctx, in.request()))
}

func (p *plug) query(ctx context.Context, _ *mcp.CallToolRequest,
	in queryArgs) (*mcp.CallToolResult, service.QueryResult, error) {
	return through(p.svc.Query(ctx, in.request()))
}

func (p *plug) sql(ctx context.Context, _ *mcp.CallToolRequest,
	in sqlArgs) (*mcp.CallToolResult, service.QueryResult, error) {
	return through(p.svc.Query(ctx, in.request()))
}

func (p *plug) store(ctx context.Context, _ *mcp.CallToolRequest,
	in storeArgs) (*mcp.CallToolResult, service.StoreResult, error) {
	return through(p.svc.Store(ctx, in.request()))
}

func (p *plug) health(ctx context.Context, _ *mcp.CallToolRequest,
	in healthArgs) (*mcp.CallToolResult, service.HealthReport, error) {
	return through(p.svc.Health(ctx, in.request()))
}
