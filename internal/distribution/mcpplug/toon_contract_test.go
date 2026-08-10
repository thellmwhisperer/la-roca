package mcpplug_test

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/provider/service"
)

// An MCP tool answer renders in the compact AXI shape the shell uses, never as
// the raw JSON envelope. The
// structured half stays for a caller that reads JSON; the readable half a
// token-budgeted agent actually consumes is the route line, the rows[N]{cols}:
// table and the help. These tests pin that contract and the size win it buys.

// looksLikeJSONDump reports whether the readable half reads as a serialized
// object or array rather than AXI text, which is the regression these tests
// exist to catch.
func looksLikeJSONDump(text string) bool {
	trimmed := strings.TrimSpace(text)
	return strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[")
}

// A row-shaped answer comes back as a TOON table, and the structured envelope is
// still attached for a caller that wants it.
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
	if result.StructuredContent == nil {
		t.Error("the structured envelope was dropped: a JSON reader lost its machine-readable half")
	}
}

// The natural-language tool carries the route narration above its answer, and
// is never the raw envelope either.
func TestQueryThroughThePlugRendersTheRouteLineNotAJSONDump(t *testing.T) {
	session := connect(t, seededServiceWithModel(t))

	result := callTool(t, session, "roca_query", map[string]any{
		"query": "how many memories are there",
	})
	text := renderedText(result)

	if !strings.Contains(text, "route ") {
		t.Errorf("the route narration is missing:\n%s", text)
	}
	if looksLikeJSONDump(text) {
		t.Errorf("the readable half is a JSON dump, not AXI TOON:\n%s", text)
	}
	if result.StructuredContent == nil {
		t.Error("the structured envelope was dropped")
	}
	if strings.Contains(text, "Run `roca ") {
		t.Errorf("MCP help points a shell-less agent at shell commands:\n%s", text)
	}
	envelope, _ := json.Marshal(result.StructuredContent)
	t.Logf("roca_query: TOON readable %d bytes vs JSON envelope %d bytes", len(text), len(envelope))
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
}

// Both MCP halves inherit the service budget. The readable half may clip more
// tightly for presentation, but StructuredContent must never bypass max_chars.
func TestAWideResultBudgetsReadableAndStructuredContent(t *testing.T) {
	svc := seededService(t)
	wide := strings.Repeat("abcdefghij", 250) // ~2500 chars per memory
	for i := range 40 {
		if _, err := svc.Store(context.Background(), service.StoreRequest{
			Layer: "discovery", Content: wide + " " + strconv.Itoa(i), Surface: service.SurfaceCLI,
		}); err != nil {
			t.Fatalf("seed a wide memory: %v", err)
		}
	}
	session := connect(t, svc)

	result := callTool(t, session, "roca_exec", map[string]any{
		"sql": "SELECT 'memory' AS source, id, content AS text FROM memories",
	})
	text := renderedText(result)
	envelope, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatalf("marshal the structured envelope: %v", err)
	}

	t.Logf("wide exec over %d memories: TOON readable %d bytes vs JSON envelope %d bytes",
		42, len(text), len(envelope))

	if !strings.Contains(text, "rows[") {
		t.Errorf("the readable half is not the TOON table:\n%s", text)
	}
	if looksLikeJSONDump(text) {
		t.Errorf("the readable half is a JSON dump, not AXI TOON:\n%s", text)
	}
	var structured service.ExecResult
	decode(t, result, &structured)
	marked := 0
	for _, row := range structured.Rows {
		value := row["text"].(string)
		if len([]rune(value)) > service.DefaultMaxChars {
			t.Fatalf("structured text bypassed the default budget: %d runes", len([]rune(value)))
		}
		if strings.HasSuffix(value, "…") {
			marked++
		}
	}
	if marked != 40 {
		t.Fatalf("marked truncated rows = %d, want 40", marked)
	}
	if len(envelope) > 30000 {
		t.Errorf("budgeted structured envelope is still unexpectedly wide: %d bytes", len(envelope))
	}
}
