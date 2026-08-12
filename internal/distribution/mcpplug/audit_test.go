package mcpplug

import (
	"bytes"
	"context"
	"encoding/json"
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

func TestAuditCorrelatesADegradedAnswerOnlyWhenItReachesTheAgentAsAnError(t *testing.T) {
	cases := []struct {
		name       string
		isError    bool
		correlated bool
	}{
		{"a degraded failure is a tool error", true, true},
		{"a degraded answer is still an answer", false, false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			root := t.TempDir()
			writer := logfile.New(root)
			if err := writer.Prepare(); err != nil {
				t.Fatal(err)
			}
			next := func(context.Context, string, mcp.Request) (mcp.Result, error) {
				return &mcp.CallToolResult{
					IsError: testCase.isError,
					Content: []mcp.Content{&mcp.TextContent{Text: "synthetic degraded answer"}},
					Meta: mcp.Meta{
						"degraded": "invalid_sql", "error": "the generated SQL was rejected",
						"error_type": "invalid_sql", "row_count": 0,
					},
				}, nil
			}
			request := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Name: "roca_query"}}
			result, err := auditCalls(writer, &bytes.Buffer{})(next)(
				context.Background(), "tools/call", request)
			if err != nil {
				t.Fatal(err)
			}
			text := result.(*mcp.CallToolResult).Content[0].(*mcp.TextContent).Text
			matches, err := filepath.Glob(filepath.Join(root, logfile.DirName, "mcp-audit-*.jsonl"))
			if err != nil || len(matches) != 1 {
				t.Fatalf("audit files = %v, err=%v", matches, err)
			}
			raw, err := os.ReadFile(matches[0])
			if err != nil {
				t.Fatal(err)
			}
			logged := strings.Contains(string(raw), `"correlation_id":"`)
			if logged != testCase.correlated || strings.Contains(text, "correlation_id") != testCase.correlated {
				t.Fatalf("correlated on screen=%v, in the log=%v, want %v: %s",
					strings.Contains(text, "correlation_id"), logged, testCase.correlated, raw)
			}
			if !strings.Contains(string(raw), `"error_type":"invalid_sql"`) {
				t.Fatalf("the degraded reason is not the error type: %s", raw)
			}
		})
	}
}

func TestAuditPersistsTheFullQueryFailureAndCorrelatesToolErrors(t *testing.T) {
	root := t.TempDir()
	writer := logfile.New(root)
	if err := writer.Prepare(); err != nil {
		t.Fatal(err)
	}
	middleware := auditCalls(writer, &bytes.Buffer{})
	next := func(context.Context, string, mcp.Request) (mcp.Result, error) {
		return &mcp.CallToolResult{
			IsError: true,
			Content: []mcp.Content{&mcp.TextContent{Text: "synthetic query engine explosion"}},
			Meta: mcp.Meta{
				"question": "find the synthetic lighthouse", "sql": "SELECT 1 LIMIT 1000",
				"raw_sql": "```sql\nSELECT 1\n```", "sql_provider": "codex",
				"sql_model": "gpt-synthetic", "row_count": 0,
			},
		}, nil
	}
	request := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{
		Name: "roca_query", Arguments: json.RawMessage(`{"query":"find the synthetic lighthouse"}`),
	}}
	result, err := middleware(next)(context.Background(), "tools/call", request)
	if err != nil {
		t.Fatal(err)
	}
	call := result.(*mcp.CallToolResult)
	if !strings.Contains(call.Content[0].(*mcp.TextContent).Text, "correlation_id") {
		t.Fatalf("tool error does not expose its correlation id: %+v", call.Content)
	}
	matches, err := filepath.Glob(filepath.Join(root, logfile.DirName, "mcp-audit-*.jsonl"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("audit files = %v, err=%v", matches, err)
	}
	raw, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"source":"mcp"`, `"tool":"roca_query"`, `"ok":false`,
		`"error":"synthetic query engine explosion"`, `"error_type":"tool_error"`,
		`"question":"find the synthetic lighthouse"`, `"sql":"SELECT 1 LIMIT 1000"`,
		"\"raw_sql\":\"```sql\\nSELECT 1\\n```\"", `"sql_provider":"codex"`,
		`"sql_model":"gpt-synthetic"`, `"correlation_id":"`,
	} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("audit record lacks %q: %s", want, raw)
		}
	}
}
