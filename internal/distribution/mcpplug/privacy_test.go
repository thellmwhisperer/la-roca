package mcpplug_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/thellmwhisperer/la-roca/internal/distribution/mcpplug"
	"github.com/thellmwhisperer/la-roca/internal/provider/service"
)

// MCP tool errors must never carry the database file path. A broken tool that
// names a path on the operator's
// machine is information an agent should not have.

func TestMCPToolErrorsMustNotCarryTheDatabasePath(t *testing.T) {
	svc := seededService(t)
	dbPath := svc.DB().Path()
	session := connect(t, svc)

	// roca_exec is the tool that actually fails over a table that is not there.
	// The fixture used to be roca_sql, which answers "there is no model to ask"
	// with no error at all, and the assertion below was skipped by a bare return:
	// the privacy check had never run.
	result, err := session.CallTool(context.Background(),
		&mcp.CallToolParams{Name: "roca_exec", Arguments: map[string]any{"sql": "SELECT * FROM nowhere"}})
	if err != nil {
		t.Fatalf("call roca_exec: %v", err)
	}
	if !result.IsError {
		t.Fatalf("roca_exec over a table that does not exist did not return an error: %s",
			renderedText(result))
	}
	text := renderedText(result)
	if strings.Contains(text, dbPath) {
		t.Errorf("the tool error carries the database path %q: %s", dbPath, text)
	}
	if len(strings.TrimSpace(text)) < 5 {
		t.Error("the tool error was scrubbed to nothing")
	}
}

// Through the service the error still carries the path (human reads it).
// Through the plug it must not (agent reads it).
func TestPlugStripsThePathThatTheServiceStillCarries(t *testing.T) {
	svc := readOnlyService(t)
	dbPath := svc.DB().Path()

	_, svcErr := svc.Store(context.Background(), service.StoreRequest{
		Layer: "discovery", Content: "should be refused", Surface: service.SurfaceCLI,
	})
	if svcErr == nil || !strings.Contains(svcErr.Error(), "read-only") {
		t.Fatalf("unexpected service error: %v", svcErr)
	}

	refused := callToolExpectingError(t, connect(t, svc), "roca_store",
		map[string]any{"layer": "discovery", "content": "should be refused"})

	if strings.Contains(refused, dbPath) {
		t.Errorf("the plug error carries the database path %q: %s", dbPath, refused)
	}
	if !strings.Contains(refused, "read-only") {
		t.Errorf("the plug error %q does not name the reason", refused)
	}
}

func TestScrubPath(t *testing.T) {
	dbPath := "/home/user/.roca/roca.db"

	// Leaves the message alone when the path is not there or is empty.
	msg := "something went wrong with the template"
	result := mcpplug.ScrubPath(errors.New(msg), dbPath)
	if result == nil || result.Error() != msg {
		t.Errorf("scrub changed a plain message: %v", result)
	}
	if mcpplug.ScrubPath(errors.New(msg), "").Error() != msg {
		t.Error("scrub touched a message when dbPath is empty")
	}

	// Replaces the path and keeps the rest.
	withPath := errors.New("could not open /home/user/.roca/roca.db because it is locked")
	result = mcpplug.ScrubPath(withPath, dbPath)
	if result == nil {
		t.Fatal("scrub returned nil for a non-nil error")
	}
	scrubbed := result.Error()
	if strings.Contains(scrubbed, dbPath) {
		t.Errorf("scrubbed message still carries the path: %q", scrubbed)
	}
	if !strings.Contains(scrubbed, "locked") {
		t.Errorf("scrubbed message lost the reason: %q", scrubbed)
	}
	if !strings.Contains(scrubbed, "the database") {
		t.Errorf("scrubbed message does not replace the path: %q", scrubbed)
	}
}
