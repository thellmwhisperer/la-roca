//go:build acceptance

package acceptance

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/cucumber/godog"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func registerDistributionMCPSteps(ctx *godog.ScenarioContext, w *distributionWorld) {
	ctx.When(`^an agent opens the MCP stdio surface and requests its tools and health$`, w.requestMCPToolsAndHealth)
	ctx.Then(`^every declared MCP tool is described and the health answer is healthy$`, w.mcpToolsAndHealthAreSound)
	ctx.Given(`^a memory that identifies the TOON parity check$`, w.seedTOONParityMemory)
	ctx.When(`^the terminal and MCP execute the same row query$`, w.queryRowsOnBothSurfaces)
	ctx.Then(`^both readable answers carry the same TOON row properties$`, w.toonRowsHaveParity)
	ctx.When(`^an agent stores a distinctive memory and immediately queries for it over MCP$`, w.storeThenQueryOverMCP)
	ctx.Then(`^the query returns the memory stored by the preceding tool call$`, w.queryFindsMCPMemory)
}

func (w *distributionWorld) requestMCPToolsAndHealth() error {
	if err := w.openMCP(); err != nil {
		return err
	}
	tools, err := w.session.ListTools(context.Background(), nil)
	if err != nil {
		return err
	}
	w.tools = tools
	return w.callTool("roca_health", map[string]any{"max_rows": 2})
}

func (w *distributionWorld) mcpToolsAndHealthAreSound() error {
	want := []string{"roca_exec", "roca_health", "roca_query", "roca_sql", "roca_store"}
	var got []string
	for _, tool := range w.tools.Tools {
		got = append(got, tool.Name)
		if strings.TrimSpace(tool.Description) == "" || tool.InputSchema == nil {
			return fmt.Errorf("tool %q lacks a description or input schema", tool.Name)
		}
	}
	sort.Strings(got)
	if !reflect.DeepEqual(got, want) {
		return fmt.Errorf("MCP tools = %v, want %v", got, want)
	}
	doc, err := structuredDocument(w.tool)
	if err != nil {
		return err
	}
	if doc["status"] != "pass" || !strings.Contains(renderedText(w.tool), "health: pass") {
		return fmt.Errorf("health is not a passing structured and readable answer: %v %q", doc, renderedText(w.tool))
	}
	return nil
}

const toonParityContent = "distribution TOON parity sentinel"

func (w *distributionWorld) seedTOONParityMemory() error {
	result := w.run("store", "--layer", "discovery", "--content", toonParityContent, "--origin", "agent")
	if result.code != 0 {
		return fmt.Errorf("seed parity memory: %s", result.stderr)
	}
	return nil
}

func (w *distributionWorld) queryRowsOnBothSurfaces() error {
	statement := "SELECT id, content FROM memories WHERE content = '" + toonParityContent + "'"
	w.human = w.run("exec", statement)
	if w.human.code != 0 {
		return fmt.Errorf("terminal exec: %s", w.human.stderr)
	}
	return w.callTool("roca_exec", map[string]any{"sql": statement})
}

func (w *distributionWorld) toonRowsHaveParity() error {
	doc, err := structuredDocument(w.tool)
	if err != nil {
		return err
	}
	terminal, agent := w.human.stdout, renderedText(w.tool)
	for _, answer := range []string{terminal, agent} {
		if !strings.Contains(answer, "rows[1]{id,content}") || !strings.Contains(answer, toonParityContent) {
			return fmt.Errorf("readable answer lacks the expected TOON shape and cell: %q", answer)
		}
	}
	columns, _ := doc["columns"].([]any)
	rows, _ := doc["rows"].([]any)
	if len(columns) != 2 || fmt.Sprint(columns[0]) != "id" || fmt.Sprint(columns[1]) != "content" || len(rows) != 1 {
		return fmt.Errorf("structured MCP rows do not describe the terminal table: %v", doc)
	}
	row, _ := rows[0].(map[string]any)
	if row["content"] != toonParityContent || !strings.Contains(terminal, fmt.Sprint(row["id"])) {
		return fmt.Errorf("MCP row cells are absent from the terminal TOON: %v %q", row, terminal)
	}
	return nil
}

const mcpStoredContent = "distribution beacon acceptance memory"

func (w *distributionWorld) storeThenQueryOverMCP() error {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/tags", func(out http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(out).Encode(map[string]any{
			"models": []map[string]string{{"name": "qwen3.5:4b", "model": "qwen3.5:4b"}},
		})
	})
	mux.HandleFunc("/api/chat", func(out http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(out, `{"message":{"content":"SELECT 'memory' AS source, id, content AS text, created_at FROM memories WHERE content LIKE '%distribution beacon%'"}}`)
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	if err := w.ensurePrepared(); err != nil {
		return err
	}
	config := fmt.Sprintf("[models]\norder = [\"ollama\"]\n\n[models.ollama]\nbase_url = %q\nmodel = \"qwen3.5:4b\"\n", server.URL)
	if err := os.WriteFile(filepath.Join(w.home, ".roca", "config.toml"), []byte(config), 0o600); err != nil {
		return err
	}
	if err := w.callTool("roca_store", map[string]any{
		"layer": "discovery", "content": mcpStoredContent, "origin": "agent",
	}); err != nil {
		return err
	}
	return w.callTool("roca_query", map[string]any{"query": "distribution beacon"})
}

func (w *distributionWorld) queryFindsMCPMemory() error {
	doc, err := structuredDocument(w.tool)
	if err != nil {
		return err
	}
	if nestedTextContains(doc["rows"], mcpStoredContent) {
		return nil
	}
	return fmt.Errorf("the next MCP query did not return the stored memory: %v", doc)
}

func nestedTextContains(value any, wanted string) bool {
	switch item := value.(type) {
	case []any:
		for _, child := range item {
			if nestedTextContains(child, wanted) {
				return true
			}
		}
	case map[string]any:
		for _, child := range item {
			if nestedTextContains(child, wanted) {
				return true
			}
		}
	default:
		return strings.Contains(fmt.Sprint(item), wanted)
	}
	return false
}

func structuredDocument(result *mcp.CallToolResult) (map[string]any, error) {
	document := structuredOf(result)
	if document == nil {
		return nil, fmt.Errorf("MCP answer has no structured document")
	}
	return document, nil
}
