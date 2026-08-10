// Package mcpplug is La Roca's MCP surface: the plug, not the product.
//
// The CLI is the complete surface over the kernel. This package exists for the
// agents that have no shell, and it carries exactly five tools, each one a
// single call into the same service object `roca query` drives. There is no
// state between calls: the process is born when the agent launches it and dies
// when the agent closes the pipe, matching the stateless protocol.
//
// The law of this package is pinned by passthrough_test.go and not by this
// comment: a handler with logic of its own is a capability no other surface
// can reach.
package mcpplug

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/thellmwhisperer/la-roca/internal/distribution/axi"
	"github.com/thellmwhisperer/la-roca/internal/distribution/logfile"
	"github.com/thellmwhisperer/la-roca/internal/provider/service"
)

// ServerName is how La Roca announces itself in the handshake, and it is the
// same name the entry in an agent's configuration carries.
const ServerName = "roca"

// instructions is what a client reads before choosing a tool. It says what this
// server is for and what it is not, because the alternative is an agent calling
// `roca_sql` to answer a question `roca_query` answers whole.
const instructions = "La Roca is local semantic memory for agent fleets: it answers " +
	"natural-language questions about what the agents on this machine have left " +
	"behind. Ask with roca_query. Use roca_sql only to compile SQL, then roca_exec " +
	"to run it under the read-only gate. Write back what is worth remembering with roca_store."

// Build is what the linker put inside the binary. The version the handshake
// declares is the product's, never the SDK's: an agent that reports a library
// version has told you nothing about what answered it.
type Build struct {
	Version string
	Commit  string
}

// plug is the service, held so that every handler is a method with one call in
// it.
type plug struct{ svc *service.Service }

// New builds the server with the five tools of the decided surface, in the
// order they are declared here.
//
// Every handler is wrapped in sanitizing so that an MCP tool error never carries
// the database file path. No agent surface reveals where the database lives on
// disk. The handlers
// themselves stay one-statement pass-throughs; the sanitization lives here, in
// the glue between the handlers and the SDK, where it cannot grow logic the
// other surfaces cannot reach.
func New(svc *service.Service, build Build) *mcp.Server {
	p := &plug{svc: svc}
	server := mcp.NewServer(&mcp.Implementation{
		Name:        ServerName,
		Title:       "La Roca",
		Description: "Local semantic memory for agent fleets",
		Version:     build.Version,
	}, &mcp.ServerOptions{Instructions: instructions})

	dbPath := svc.DB().Path()
	audit := logfile.New(svc.DataDir())
	mcp.AddTool(server, execTool, sanitizing(p.exec, dbPath))
	mcp.AddTool(server, healthTool, sanitizing(p.health, dbPath))
	mcp.AddTool(server, queryTool, sanitizing(p.query, dbPath))
	mcp.AddTool(server, sqlTool, sanitizing(p.sql, dbPath))
	mcp.AddTool(server, storeTool, sanitizing(p.store, dbPath))
	server.AddReceivingMiddleware(auditCalls(audit))
	return server
}

// sanitizing wraps a tool handler so that its error never carries the database
// file path. The handler is called first; the error it returns is scrubbed of
// every occurrence of dbPath, replaced with "the database", and the rest of the
// result passes through untouched.
func sanitizing[In, Out any](
	h func(context.Context, *mcp.CallToolRequest, In) (*mcp.CallToolResult, Out, error),
	dbPath string,
) func(context.Context, *mcp.CallToolRequest, In) (*mcp.CallToolResult, Out, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, in In) (*mcp.CallToolResult, Out, error) {
		res, out, err := h(ctx, req, in)
		return res, out, ScrubPath(err, dbPath)
	}
}

func auditCalls(audit *logfile.Writer) mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if method != "tools/call" {
				return next(ctx, method, req)
			}
			started := time.Now()
			tool, args := toolCall(req)
			result, err := next(ctx, method, req)
			ok := err == nil
			if callResult, isToolResult := result.(*mcp.CallToolResult); isToolResult {
				ok = ok && !callResult.IsError
			}
			_ = audit.Append(logfile.MCPAudit, logfile.MCPRecord{
				Timestamp: started.UTC(), Tool: tool, Args: args, OK: ok,
				DurationMS: time.Since(started).Milliseconds(), RowCount: resultRows(result),
			})
			return result, err
		}
	}
}

func toolCall(req mcp.Request) (string, any) {
	call, ok := req.(*mcp.CallToolRequest)
	if !ok || call.Params == nil {
		return "unknown", map[string]any{}
	}
	args := any(map[string]any{})
	if len(call.Params.Arguments) > 0 {
		if err := json.Unmarshal(call.Params.Arguments, &args); err != nil {
			args = string(call.Params.Arguments)
		}
	}
	return call.Params.Name, args
}

func resultRows(value any) int {
	switch result := value.(type) {
	case *mcp.CallToolResult:
		return resultRows(result.StructuredContent)
	case service.QueryResult:
		return result.RowCount
	case service.ExecResult:
		return result.RowCount
	case service.StoreResult:
		if result.ID != 0 {
			return 1
		}
	case service.HealthReport:
		rows := 0
		for _, check := range result.Checks {
			rows += len(check.Rows)
		}
		return rows
	case map[string]any:
		if count, ok := result["row_count"].(float64); ok {
			return int(count)
		}
	case json.RawMessage:
		var decoded any
		if json.Unmarshal(result, &decoded) == nil {
			return resultRows(decoded)
		}
	}
	return 0
}

// rendered paints the AXI TOON text for a service answer and lets the SDK
// attach the structured envelope beside it. A handler stays one statement that
// calls one of the typed wrappers below; this is where the readable half is
// shaped, so a row-shaped answer reaches an agent as compact rows and not as
// the raw envelope that once drowned the tool-result budget.
//
// Content is the AXI text the agent reads; the SDK keeps filling
// StructuredContent from the returned value, so a caller that reads JSON still
// gets the machine-readable envelope. On error the result stays nil and the
// service's error flows out for the sanitizing wrapper to scrub.
func rendered[T any](res T, err error, paint func(T) string) (*mcp.CallToolResult, T, error) {
	if err != nil {
		return nil, res, err
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: paint(res)}},
	}, res, nil
}

// queryText shapes a natural-language answer. The query tool runs the question
// and the sql tool compiles it without running; both produce a QueryResult, and
// axi.Query shows the rows for one and the SQL for the other, so they share a
// wrapper.
func queryText(res service.QueryResult, err error) (*mcp.CallToolResult, service.QueryResult, error) {
	return rendered(res, err, func(r service.QueryResult) string { return axi.Query(r, "") })
}

func execText(res service.ExecResult, err error) (*mcp.CallToolResult, service.ExecResult, error) {
	return rendered(res, err, axi.Exec)
}

func storeText(res service.StoreResult, err error) (*mcp.CallToolResult, service.StoreResult, error) {
	return rendered(res, err, axi.Store)
}

func healthText(res service.HealthReport, err error) (*mcp.CallToolResult, service.HealthReport, error) {
	return rendered(res, err, axi.Health)
}

// ScrubPath replaces every occurrence of the database path in an error message
// with "the database", so that MCP agents never see the filesystem location.
func ScrubPath(err error, dbPath string) error {
	if err == nil || dbPath == "" {
		return err
	}
	msg := err.Error()
	if !strings.Contains(msg, dbPath) {
		return err
	}
	cleaned := strings.ReplaceAll(msg, dbPath, "the database")
	return errors.New(cleaned)
}

// Serve runs the server over stdio in the foreground until the client closes
// the pipe. There is no daemon: this process is the session.
func Serve(ctx context.Context, svc *service.Service, build Build) error {
	return serveOver(ctx, svc, build, &mcp.StdioTransport{})
}

// serveOver is Serve with the transport injected, which is what lets a test
// drive the same code path over a pipe it owns.
func serveOver(ctx context.Context, svc *service.Service, build Build,
	transport mcp.Transport) error {
	err := New(svc, build).Run(ctx, transport)
	// A closed pipe is how this server ends its life, not a failure: the agent
	// that launched it went away, which is exactly the contract.
	if err == io.EOF || err == os.ErrClosed {
		return nil
	}
	return err
}
