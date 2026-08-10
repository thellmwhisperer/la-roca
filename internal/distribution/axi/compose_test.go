package axi_test

import (
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/distribution/axi"
	"github.com/thellmwhisperer/la-roca/internal/provider/service"
)

// The composers are the one owner of each result type's text. These tests pin
// the AXI shape so a second renderer cannot grow on either surface: the route
// line, the rows[N]{cols}: table and the help all come out here, byte for byte.

func TestQueryRendersTheRouteLineTOONRowsAndHelp(t *testing.T) {
	res := service.QueryResult{
		Question:  "what do we know about axi",
		Path:      service.PathLLM,
		Engine:    "ollama",
		Model:     "qwen",
		LatencyMS: 4,
		Match:     service.MatchFound,
		RowCount:  1,
		Columns:   []string{"source", "id", "text"},
		Rows: []map[string]any{{
			"source": "memory", "id": int64(1), "text": "AXI uses TOON rows, stable fields.",
		}},
	}
	got := axi.Query(res, "")

	wantRoute := "route llm_fallback · provider ollama · model qwen · 4 ms"
	if !strings.Contains(got, wantRoute) {
		t.Errorf("the route line is wrong:\n%s", got)
	}
	if !strings.Contains(got, "rows[1]{source,id,text}:") {
		t.Errorf("the TOON row header is missing:\n%s", got)
	}
	if !strings.Contains(got, `memory,1,"AXI uses TOON rows, stable fields."`) {
		t.Errorf("the row values are missing:\n%s", got)
	}
	if !strings.Contains(got, "help[2]:") {
		t.Errorf("the contextual help is missing:\n%s", got)
	}
	if strings.HasPrefix(strings.TrimSpace(got), "{") {
		t.Errorf("the query text is a JSON dump, not AXI TOON:\n%s", got)
	}
}

func TestQueryFallsToProseWhenTheCallerPassesIt(t *testing.T) {
	res := service.QueryResult{
		Question: "count memories", Path: service.PathLLM, Engine: "ollama",
		Model: "qwen", Match: service.MatchFound, RowCount: 2,
		Columns: []string{"n"}, Rows: []map[string]any{{"n": int64(2)}},
	}
	got := axi.Query(res, "there are two memories")
	if !strings.Contains(got, "route ") {
		t.Errorf("the preamble was dropped under prose:\n%s", got)
	}
	if !strings.Contains(got, "there are two memories") {
		t.Errorf("the prose rendering was dropped:\n%s", got)
	}
	if strings.Contains(got, "rows[") {
		t.Errorf("the row table replaced the prose:\n%s", got)
	}
}

func TestASqlOnlyQueryRendersTheSQLUnderTheRouteLine(t *testing.T) {
	res := service.QueryResult{
		Question: "count memories", Path: service.PathLLM, Engine: "ollama",
		Model: "qwen", LatencyMS: 4, SQL: "SELECT COUNT(*) AS n FROM memories",
	}
	got := axi.Query(res, "")
	if !strings.Contains(got, "route ") {
		t.Errorf("the route line is missing:\n%s", got)
	}
	if !strings.Contains(got, "SELECT COUNT(*) AS n FROM memories") {
		t.Errorf("the compiled SQL is missing:\n%s", got)
	}
	if strings.Contains(got, "rows[") {
		t.Errorf("a compile-only answer painted rows:\n%s", got)
	}
}

func TestExecRendersTheSQLRowsCountAndHelp(t *testing.T) {
	res := service.ExecResult{
		SQL:     "SELECT layer, COUNT(*) AS n FROM memories GROUP BY layer",
		Columns: []string{"layer", "n"}, RowCount: 2, LatencyMS: 7,
		Rows: []map[string]any{
			{"layer": "discovery", "n": int64(12)},
			{"layer": "project", "n": int64(107)},
		},
	}
	got := axi.Exec(res)

	if !strings.HasPrefix(got, res.SQL) {
		t.Errorf("exec does not lead with the SQL it ran:\n%s", got)
	}
	if !strings.Contains(got, "rows[2]{layer,n}:") {
		t.Errorf("the TOON row header is missing:\n%s", got)
	}
	if !strings.Contains(got, "2 rows · 7 ms") {
		t.Errorf("the row count and latency are missing:\n%s", got)
	}
	if !strings.Contains(got, "help[2]:") {
		t.Errorf("the contextual help is missing:\n%s", got)
	}
}

func TestExecOmitsHelpForABareScalar(t *testing.T) {
	res := service.ExecResult{
		SQL: "SELECT COUNT(*) AS n FROM memories", RowCount: 1, LatencyMS: 2,
		Columns: []string{"n"}, Rows: []map[string]any{{"n": int64(1908)}},
	}
	got := axi.Exec(res)
	// A single scalar is its number; help around it is ceremony.
	if strings.Contains(got, "help[") {
		t.Errorf("help was painted around a bare scalar:\n%s", got)
	}
}

func TestHealthRendersTheStatusLineAndCheckTable(t *testing.T) {
	res := service.HealthReport{
		Status: "ok",
		Checks: map[string]service.HealthCheck{
			"orphan_supersedes": {Status: "ok", Count: 0, Summary: "No dangling pointers."},
			"ghost_sessions":    {Status: "warn", Count: 3, Summary: "Sessions left open."},
		},
	}
	got := axi.Health(res)

	if !strings.HasPrefix(got, "health: ok") {
		t.Errorf("health does not lead with its status:\n%s", got)
	}
	if !strings.Contains(got, "rows[2]{status,check,count,summary}:") {
		t.Errorf("the check table header is missing:\n%s", got)
	}
	for _, want := range []string{"orphan_supersedes", "ghost_sessions", "No dangling pointers."} {
		if !strings.Contains(got, want) {
			t.Errorf("a check row was lost (%q):\n%s", want, got)
		}
	}
}

func TestStoreRendersTheIdentityLine(t *testing.T) {
	created := axi.Store(service.StoreResult{ID: 42, Layer: "discovery"})
	if created != "stored: memory 42 in layer discovery" {
		t.Errorf("created store line = %q", created)
	}
	skipped := axi.Store(service.StoreResult{ID: 42, Layer: "discovery", Skipped: true})
	if skipped != "already stored: memory 42 in layer discovery" {
		t.Errorf("skipped store line = %q", skipped)
	}
}
