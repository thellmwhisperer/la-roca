package mcpplug

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/thellmwhisperer/la-roca/internal/distribution/logfile"
)

func TestAuditAppendFailureWarnsOnceWithoutFailingCalls(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, logfile.DirName), []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	var warnings bytes.Buffer
	middleware := auditCalls(logfile.New(root), &warnings)
	next := func(context.Context, string, mcp.Request) (mcp.Result, error) {
		return &mcp.CallToolResult{}, nil
	}
	request := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Name: "roca_query"}}
	for range 2 {
		if _, err := middleware(next)(context.Background(), "tools/call", request); err != nil {
			t.Fatalf("audit failure became a tool failure: %v", err)
		}
	}
	if got := strings.Count(warnings.String(), "warning:"); got != 1 {
		t.Fatalf("warnings = %d, want one: %s", got, warnings.String())
	}
}

func TestAuditDoesNotRecreateARemovedLogDirectory(t *testing.T) {
	root := t.TempDir()
	logs := filepath.Join(root, logfile.DirName)
	if err := os.Mkdir(logs, 0o700); err != nil {
		t.Fatal(err)
	}
	middleware := auditCalls(logfile.New(root), &bytes.Buffer{})
	if err := os.Remove(logs); err != nil {
		t.Fatal(err)
	}
	next := func(context.Context, string, mcp.Request) (mcp.Result, error) {
		return &mcp.CallToolResult{}, nil
	}
	request := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Name: "roca_query"}}
	if _, err := middleware(next)(context.Background(), "tools/call", request); err != nil {
		t.Fatalf("audit failure became a tool failure: %v", err)
	}
	if _, err := os.Stat(logs); !os.IsNotExist(err) {
		t.Fatalf("the removed log directory was recreated: %v", err)
	}
}
