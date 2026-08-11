package mcpplug

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The five handlers. Each one is a single call into the service and nothing
// else, and passthrough_test.go fails when that stops being true.
//
// The wrappers return any(nil) as the typed output so the SDK does not attach a
// structured JSON envelope beside the TOON text. Service errors are packed as
// tool errors carrying the words the service wrote. That is why the read-only
// refusal, an unknown template and a missing argument all reach the agent as
// something it can read and act on, instead of as a dead session.
//
// The readable half is painted by the typed wrappers in plug.go, which turn a
// row-shaped answer into the AXI TOON table the shell uses too. A handler does
// no rendering of its own: it stays one statement, and the wrapper it names is
// the whole of its part in the output.

func (p *plug) exec(ctx context.Context, _ *mcp.CallToolRequest,
	in execArgs) (*mcp.CallToolResult, any, error) {
	return execText(p.svc.Exec(ctx, in.request()))
}

func (p *plug) query(ctx context.Context, _ *mcp.CallToolRequest,
	in queryArgs) (*mcp.CallToolResult, any, error) {
	return queryText(p.svc.Query(ctx, in.request()))
}

func (p *plug) sql(ctx context.Context, _ *mcp.CallToolRequest,
	in sqlArgs) (*mcp.CallToolResult, any, error) {
	return queryText(p.svc.Query(ctx, in.request()))
}

func (p *plug) store(ctx context.Context, _ *mcp.CallToolRequest,
	in storeArgs) (*mcp.CallToolResult, any, error) {
	return storeText(p.svc.Store(ctx, in.request()))
}

func (p *plug) health(ctx context.Context, _ *mcp.CallToolRequest,
	in healthArgs) (*mcp.CallToolResult, any, error) {
	return healthText(p.svc.Health(ctx, in.request()))
}
