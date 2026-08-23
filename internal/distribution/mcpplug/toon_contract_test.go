package mcpplug_test

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/thellmwhisperer/la-roca/internal/provider/service"
)

// An MCP tool answer renders in the compact AXI shape the shell uses, never as
// the raw JSON envelope. There is no structured half for a client to prefer:
// the agent consumes the route line, the rows[N]{cols}: table and the help.
// These tests pin that contract and the size win it buys.

// looksLikeJSONDump reports whether the readable half reads as a serialized
// object or array rather than AXI text, which is the regression these tests
// exist to catch.
func looksLikeJSONDump(text string) bool {
	trimmed := strings.TrimSpace(text)
	return strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[")
}

func assertNoStructuredEnvelope(t *testing.T, result any) {
	t.Helper()
	call, ok := result.(*mcp.CallToolResult)
	if !ok {
		t.Fatalf("result type = %T, want *mcp.CallToolResult", result)
	}
	if call.StructuredContent != nil {
		t.Fatalf("structured JSON envelope shipped: %s", asJSON(t, call.StructuredContent))
	}
	wire := asJSON(t, call)
	if strings.Contains(wire, `"structuredContent"`) || strings.Contains(wire, `"rows":`) ||
		strings.Contains(wire, `"columns":`) {
		t.Fatalf("raw rows JSON shipped on the wire: %s", wire)
	}
}

// A row-shaped answer comes back only as a TOON table.
func TestRowShapedResultsRenderAsTOONNotAJSONDump(t *testing.T) {
	session := connect(t, seededService(t))

	result := callTool(t, session, "roca_exec", map[string]any{
		"sql": "SELECT layer, COUNT(*) AS n FROM memories GROUP BY layer ORDER BY layer",
	})
	text := renderedText(result)

	if !strings.Contains(text, "rows[2]{layer,n}:") {
		t.Errorf("the readable half is not the TOON table:\n%s", text)
	}
	if !strings.Contains(text, "rows ·") {
		t.Errorf("the row count and latency are missing:\n%s", text)
	}
	if looksLikeJSONDump(text) {
		t.Errorf("the readable half is a JSON dump, not AXI TOON:\n%s", text)
	}
	assertNoStructuredEnvelope(t, result)
}

// The natural-language tool carries the route narration above its answer, and
// is never the raw envelope either.
func TestQueryThroughThePlugRendersTheRouteLineNotAJSONDump(t *testing.T) {
	session := connect(t, seededServiceWithModel(t))

	result := callTool(t, session, "roca_query", map[string]any{
		"query": "how many memories are there",
	})
	text := renderedText(result)

	if !strings.Contains(text, "search ") {
		t.Errorf("the hybrid search narration is missing:\n%s", text)
	}
	if looksLikeJSONDump(text) {
		t.Errorf("the readable half is a JSON dump, not AXI TOON:\n%s", text)
	}
	assertNoStructuredEnvelope(t, result)
	if strings.Contains(text, "Run `roca ") {
		t.Errorf("MCP help points a shell-less agent at shell commands:\n%s", text)
	}
}

// The compile-only tool answers with its SQL under the route line, not with the
// envelope that carries the route provenance.
func TestSQLThroughThePlugRendersTheRouteLineAndSQLNotAJSONDump(t *testing.T) {
	session := connect(t, seededServiceWithModel(t))

	result := callTool(t, session, "roca_sql", map[string]any{
		"query": "how many memories are there",
	})
	text := renderedText(result)

	if !strings.Contains(text, "route ") {
		t.Errorf("the route narration is missing:\n%s", text)
	}
	if !strings.Contains(text, "SELECT") {
		t.Errorf("the compiled SQL is missing:\n%s", text)
	}
	if looksLikeJSONDump(text) {
		t.Errorf("the readable half is a JSON dump, not AXI TOON:\n%s", text)
	}
	if strings.Contains(text, "Run `roca ") {
		t.Errorf("MCP exec help points a shell-less agent at shell commands:\n%s", text)
	}
	assertNoStructuredEnvelope(t, result)
}

// The health tool answers with its status line and the check table the shell
// prints, not the diagnosis envelope.
func TestHealthThroughThePlugRendersTheStatusAndCheckTable(t *testing.T) {
	session := connect(t, seededService(t))

	result := callTool(t, session, "roca_health", nil)
	text := renderedText(result)

	if !strings.HasPrefix(text, "health: ") {
		t.Errorf("health does not lead with its status:\n%s", text)
	}
	if !strings.Contains(text, "rows[") || !strings.Contains(text, "{status,check,count,summary}") {
		t.Errorf("the check table is missing:\n%s", text)
	}
	if looksLikeJSONDump(text) {
		t.Errorf("the readable half is a JSON dump, not AXI TOON:\n%s", text)
	}
	assertNoStructuredEnvelope(t, result)
}

// The TOON answer inherits the service budget, and the former JSON envelope is
// used only inside the test to pin the size reduction it bought.
func TestAWideResultBudgetsTOONAndShipsNoRowsEnvelope(t *testing.T) {
	svc := seededService(t)
	wide := strings.Repeat("abcdefghij", 250) // ~2500 chars per memory
	for i := range 40 {
		if _, err := svc.Store(context.Background(), service.StoreRequest{
			Layer: "discovery", Content: wide + " " + strconv.Itoa(i),
			Authorship: service.Authorship{Surface: service.SurfaceCLI},
		}); err != nil {
			t.Fatalf("seed a wide memory: %v", err)
		}
	}
	session := connect(t, svc)

	result := callTool(t, session, "roca_exec", map[string]any{
		"sql":       "SELECT 'memory' AS source, id, content AS text FROM memories",
		"max_chars": 48,
	})
	text := renderedText(result)

	if !strings.Contains(text, "rows[") {
		t.Errorf("the readable half is not the TOON table:\n%s", text)
	}
	if looksLikeJSONDump(text) {
		t.Errorf("the readable half is a JSON dump, not AXI TOON:\n%s", text)
	}
	if strings.Contains(text, strings.Repeat("abcdefghij", 5)) || !strings.Contains(text, "…") {
		t.Errorf("max_chars did not clip the TOON cells to 48 characters:\n%s", text)
	}
	assertNoStructuredEnvelope(t, result)

	statement := "SELECT 'memory' AS source, id, content AS text FROM memories"
	former, err := svc.Exec(context.Background(), service.ExecRequest{SQL: statement, MaxChars: 3000})
	if err != nil {
		t.Fatalf("build the former JSON response: %v", err)
	}
	formerEnvelope, err := json.Marshal(former)
	if err != nil {
		t.Fatalf("marshal the former JSON response: %v", err)
	}
	wideResult := callTool(t, session, "roca_exec", map[string]any{
		"sql":       statement,
		"max_chars": 3000,
	})
	wideText := renderedText(wideResult)
	t.Logf("wide exec over %d memories: TOON readable %d bytes vs former JSON response %d bytes",
		42, len(wideText), len(formerEnvelope))
	if len(wideText)*10 >= len(formerEnvelope) {
		t.Errorf("TOON response (%d bytes) is not an order of magnitude under the former JSON response (%d bytes)",
			len(wideText), len(formerEnvelope))
	}
	assertNoStructuredEnvelope(t, wideResult)
}
