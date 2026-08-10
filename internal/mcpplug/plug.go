// Package mcpplug is La Roca's MCP surface: the plug, not the product.
//
// The CLI is the complete surface over the kernel. This package exists for the
// agents that have no shell, and it carries exactly six tools, each one a
// single call into the same service object `roca query` drives. There is no
// state between calls: the process is born when the agent launches it and dies
// when the agent closes the pipe, which is what makes the stateless 2026-07-28
// revision of the protocol the natural north star (TECH-SPEC 1.8).
//
// The law of this package is pinned by passthrough_test.go and not by this
// comment: a handler with logic of its own is a capability no shell, hook or
// script can reach.
package mcpplug

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/thellmwhisperer/la-roca/internal/service"
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

// New builds the server with the six tools of the decided surface, in the
// order they are declared here.
//
// Every handler is wrapped in sanitizing so that an MCP tool error never carries
// the database file path. That is the rule (adenda 2026-08-05 ~21:55):
// no agent surface ever reveals where the database lives on disk. The handlers
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
	mcp.AddTool(server, execTool, sanitizing(p.exec, dbPath))
	mcp.AddTool(server, healthTool, sanitizing(p.health, dbPath))
	mcp.AddTool(server, queryTool, sanitizing(p.query, dbPath))
	mcp.AddTool(server, sqlTool, sanitizing(p.sql, dbPath))
	mcp.AddTool(server, storeTool, sanitizing(p.store, dbPath))
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

// through forwards what the service answered, untouched, adding the nil result
// that tells the SDK to build the envelope itself.
//
// It exists so that a handler can be literally one statement. Go only splices a
// call into a return when it is the whole return, and the alternative is an
// assignment inside the handler, which is the first step of a handler growing a
// body of its own. One honest helper here is cheaper than a rule nobody
// enforces, and passthrough_test.go is what enforces it.
func through[T any](value T, err error) (*mcp.CallToolResult, T, error) {
	return nil, value, err
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
