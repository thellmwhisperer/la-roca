package mcpplug

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/thellmwhisperer/la-roca/internal/distribution/axi"
	"github.com/thellmwhisperer/la-roca/internal/provider/service"
)

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

func (p *plug) explore(ctx context.Context, _ *mcp.CallToolRequest,
	in exploreArgs) (*mcp.CallToolResult, any, error) {
	return exploreText(p.playground(ctx, "explore", in.Query, in.Layer, in.Databases, in.MaxChars, in.Deep))
}

func (p *plug) query(ctx context.Context, _ *mcp.CallToolRequest,
	in queryArgs) (*mcp.CallToolResult, any, error) {
	req := in.request()
	if req.Question == "" {
		return nil, nil, fmt.Errorf("a query is required")
	}
	return searchText(p.svc.Search(ctx, req))
}

func (p *plug) sql(ctx context.Context, _ *mcp.CallToolRequest,
	in sqlArgs) (*mcp.CallToolResult, any, error) {
	return queryText(p.playground(ctx, "sql", in.Query, in.Layer, in.Databases, 0, false))
}

func (p *plug) store(ctx context.Context, req *mcp.CallToolRequest,
	in storeArgs) (*mcp.CallToolResult, any, error) {
	return storeText(p.svc.Store(ctx, in.request(authorshipFromRequest(req))))
}

func (p *plug) health(ctx context.Context, _ *mcp.CallToolRequest,
	in healthArgs) (*mcp.CallToolResult, any, error) {
	return healthText(p.svc.Health(ctx, in.request()))
}

func (p *plug) handoffLatest(ctx context.Context, _ *mcp.CallToolRequest,
	in handoffLatestArgs) (*mcp.CallToolResult, any, error) {
	project, err := resolveSessionProject(in.Project)
	if err != nil {
		return nil, nil, err
	}
	list, err := p.svc.LatestHandoffs(ctx, project)
	if err != nil {
		return nil, nil, err
	}
	return rendered(list, nil, axi.Handoffs)
}

func resolveSessionProject(project string) (string, error) {
	return service.ResolveSessionProject(project)
}

func (p *plug) pillShow(ctx context.Context, _ *mcp.CallToolRequest,
	in pillShowArgs) (*mcp.CallToolResult, any, error) {
	project, err := resolveSessionProject(in.Project)
	if err != nil {
		return nil, nil, err
	}
	record, err := p.svc.ShowPill(ctx, project, in.Slug)
	if err != nil {
		var unknown *service.UnknownPillError
		if errors.As(err, &unknown) {
			var help []string
			if len(unknown.Known) > 0 {
				help = append(help, "known slugs: "+strings.Join(unknown.Known, ", "))
			}
			if in.Project == "" {
				help = append(help, "project scope came from the working directory")
			}
			if len(help) > 0 {
				return nil, nil, fmt.Errorf("%w\n%s", err, axi.RenderHelp(help...))
			}
		}
		return nil, nil, err
	}
	return rendered(record, nil, axi.Pill)
}
