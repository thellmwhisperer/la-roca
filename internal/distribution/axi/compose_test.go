package axi_test

import (
	"encoding/json"
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
		Question:       "what do we know about axi",
		Path:           service.PathLLM,
		Engine:         "ollama",
		Model:          "qwen",
		SQLInferenceMS: 4, ExecutionMS: 2,
		Match:    service.MatchFound,
		RowCount: 1,
		Columns:  []string{"source", "id", "text"},
		Rows: []map[string]any{{
			"source": "memory", "id": int64(1), "text": "AXI uses TOON rows, stable fields.",
		}},
	}
	got := axi.Query(res, "")

	for _, want := range []string{
		"route model",
		"SQL · provider ollama · model qwen · 4 ms",
		"search · 2 ms",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the phase header lacks %q:\n%s", want, got)
		}
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

func TestQueryAndExecDeclareConsultedDatabases(t *testing.T) {
	queryText := axi.Query(service.QueryResult{
		Path: service.PathLLM, Match: service.MatchFound,
		Databases: []string{"core", "plugin:receipts"},
		Columns:   []string{"text", "database"}, RowCount: 1,
		Rows: []map[string]any{{"text": "synthetic", "database": "plugin:receipts"}},
	}, "")
	if !strings.Contains(queryText, "databases: core, plugin:receipts") {
		t.Fatalf("query databases are absent:\n%s", queryText)
	}
	execText := axi.Exec(service.ExecResult{
		SQL: "SELECT 1", Databases: []string{"core", "plugin:receipts"},
	})
	if !strings.Contains(execText, "databases: core, plugin:receipts") {
		t.Fatalf("exec databases are absent:\n%s", execText)
	}
}

func TestQueryDeclaresEveryModelSQLRepair(t *testing.T) {
	got := axi.Query(service.QueryResult{
		Question: "synthetic query", Path: service.PathLLM, Engine: "codex", Model: "test",
		SQL: "SELECT id FROM memories LIMIT 5", Repaired: []string{"code_fence", "union_order_by"},
		RetriedSQL: true, SQLRetryInferenceMS: 6,
	}, "")
	if !strings.Contains(got, "repaired: code_fence, union_order_by") {
		t.Fatalf("repair declaration is missing:\n%s", got)
	}
	if !strings.Contains(got, "SQL retry after gate rejection · 6 ms") {
		t.Fatalf("retry latency declaration is missing:\n%s", got)
	}
}

func TestQueryWithProseOmitsEveryEvidenceRow(t *testing.T) {
	res := service.QueryResult{
		Question: "recent memories", Path: service.PathLLM, Engine: "ollama",
		Model: "qwen", Match: service.MatchFound, RowCount: 6,
		Columns: []string{"source", "id", "text", "created_at"},
		Rows: []map[string]any{
			{"source": "memory", "id": int64(1), "text": "first\nrow", "created_at": "2026-08-11"},
			{"source": "exchange", "id": int64(2), "text": strings.Repeat("x", 90)},
			{"source": "memory", "id": int64(3), "text": "third row"},
			{"source": "memory", "id": int64(4), "text": "must stay hidden"},
		},
	}
	got := axi.Query(res, "the recent memories agree")
	if !strings.Contains(got, "route ") {
		t.Errorf("the preamble was dropped under prose:\n%s", got)
	}
	if !strings.Contains(got, "the recent memories agree") {
		t.Errorf("the prose rendering was dropped:\n%s", got)
	}
	if strings.Contains(got, "evidence:") || strings.Contains(got, "rows[") ||
		strings.Contains(got, "first row") || strings.Contains(got, "must stay hidden") ||
		strings.Contains(got, "rows total") {
		t.Errorf("full mode dumped the rows table:\n%s", got)
	}
}

func TestExploreAlwaysPrintsItsDeclaredModeGeneratedSQLAndProse(t *testing.T) {
	res := service.QueryResult{
		Question: "format", Mode: "explore_deep", Path: service.PathLLM,
		Engine: "codex", Model: "gpt", Match: service.MatchFound,
		SQL: "SELECT source, text FROM memories LIMIT 10", RowCount: 1,
		Columns:        []string{"source", "text"},
		Rows:           []map[string]any{{"source": "memory", "text": "raw evidence"}},
		Interpretation: "The rows support one format decision.",
	}
	got := axi.Explore(res)
	for _, want := range []string{
		"mode: explore_deep", "generated SQL:\n" + res.SQL,
		"The rows support one format decision.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("explore output lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "raw evidence") || strings.Contains(got, "rows[") {
		t.Fatalf("successful explore dumped evidence after prose:\n%s", got)
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

func TestQueryRefusalNamesItsRouteWithoutSuggestingASearch(t *testing.T) {
	result := service.QueryResult{
		Question: "what is the tallest mountain?", Path: service.PathRefused,
		Engine: "codex", Model: "synthetic-model",
		Message: "The question is outside the scope of the La Roca memory database.",
	}
	got := axi.Query(result, "")
	for _, want := range []string{"route refused", "SQL · provider codex", result.Message} {
		if !strings.Contains(got, want) {
			t.Errorf("refusal output lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "help[") || strings.Contains(got, "search ·") {
		t.Fatalf("refusal was rendered as a failed search:\n%s", got)
	}
}

func TestQueryAskReturnsOnlyTheClarifyingQuestion(t *testing.T) {
	result := service.QueryResult{
		Question: "what happened in a specific project?", Path: service.PathAsk,
		ClarificationRequired: true, MissingSlot: "project",
		Message: "Which project should I use? Please name it in the question.",
	}
	if got := axi.Query(result, ""); got != result.Message {
		t.Fatalf("ask output = %q, want %q", got, result.Message)
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

// Counted prose goes through Quantity and Number everywhere, including the
// renderer that owns them. Exec was the one line still formatting its own count:
// "1 rows" for a single row, and no thousands separator over a wide result.
func TestExecCountsRowsTheWayEveryOtherRendererDoes(t *testing.T) {
	one := axi.Exec(service.ExecResult{
		SQL: "SELECT id, layer FROM memories LIMIT 1", RowCount: 1,
		Columns: []string{"id", "layer"},
		Rows:    []map[string]any{{"id": int64(1), "layer": "pattern"}},
	})
	if strings.Contains(one, "1 rows") {
		t.Errorf("a single row is counted in the plural:\n%s", one)
	}
	if !strings.Contains(one, "1 row ") {
		t.Errorf("a single row is not counted at all:\n%s", one)
	}

	many := axi.Exec(service.ExecResult{
		SQL: "SELECT id FROM memories", RowCount: 12500,
		Columns: []string{"id"}, Rows: []map[string]any{{"id": int64(1)}},
	})
	if !strings.Contains(many, "12,500 rows") {
		t.Errorf("a wide count is not grouped:\n%s", many)
	}
}

// An installation that splits the two inferences says so above the evidence:
// the route line names the provider that wrote the SQL, and the line under it
// names the provider the rows were read by. A fall back to the SQL provider
// prints its note instead, because then there is nothing to distinguish.
func TestQueryNamesTheProviderThatReadTheRows(t *testing.T) {
	res := service.QueryResult{
		Question: "count memories", Path: service.PathLLM, Engine: "codex",
		Model: "gpt-5.6-sol", LatencyMS: 4, Match: service.MatchFound, RowCount: 1,
		Columns: []string{"n"}, Rows: []map[string]any{{"n": int64(2)}},
		SQLInferenceMS: 4, ExecutionMS: 2,
		InterpretEngine: "ollama", InterpretModel: "qwen3.5:4b", InterpretationMS: 9,
	}
	got := axi.Query(res, "there are two memories")
	if !strings.Contains(got, "answer · provider ollama · model qwen3.5:4b · 9 ms") {
		t.Errorf("the second inference's provenance is missing:\n%s", got)
	}

	res.InterpretEngine, res.InterpretModel = "codex", "gpt-5.6-sol"
	res.InterpretNote = "the interpretation provider was not available " +
		"(ollama: not running): the rows were read by codex"
	got = axi.Query(res, "there are two memories")
	if !strings.Contains(got, res.InterpretNote) {
		t.Errorf("the fall back to the SQL provider is not declared:\n%s", got)
	}
	if !strings.Contains(got, "answer · provider codex · model gpt-5.6-sol · 9 ms") {
		t.Errorf("the shared provider lost the answer's separate timing:\n%s", got)
	}
}

func TestQueryEnvelopeUsesHonestPhaseNames(t *testing.T) {
	raw, err := json.Marshal(service.QueryResult{
		Path: service.PathLLM, Engine: "codex", Model: "gpt",
		InterpretEngine: "ollama", InterpretModel: "qwen",
		RetriedSQL: true, FirstModelSQL: "SELECT missing FROM memories LIMIT 1",
		RetryReason: "the column missing does not exist", RetryType: service.RetryGateRejection,
		FirstRepaired:  []string{"code_fence", "trailing_semicolon"},
		SQLInferenceMS: 13, SQLRetryInferenceMS: 5, ExecutionMS: 2, InterpretationMS: 8,
		LLMLatencyMS: 11, SQLRetryProviderLatencyMS: 4,
		Repaired: []string{"code_fence", "union_order_by"},
	})
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, want := range []string{
		`"path":"model"`, `"sql_provider":"codex"`, `"sql_model":"gpt"`,
		`"interpretation_provider":"ollama"`, `"interpretation_model":"qwen"`,
		`"retried_sql":true`,
		`"first_model_sql":"SELECT missing FROM memories LIMIT 1"`,
		`"retry_reason":"the column missing does not exist"`,
		`"retry_type":"gate_rejection"`,
		`"first_repaired":["code_fence","trailing_semicolon"]`,
		`"sql_inference_ms":13`, `"sql_retry_inference_ms":5`,
		`"sql_provider_latency_ms":11`, `"sql_retry_provider_latency_ms":4`,
		`"execution_ms":2`, `"interpretation_ms":8`,
		`"repaired":["code_fence","union_order_by"]`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("phase envelope lacks %s: %s", want, text)
		}
	}
	for _, obsolete := range []string{`"engine"`, `"interpret_engine"`, "llm_fallback"} {
		if strings.Contains(text, obsolete) {
			t.Errorf("phase envelope kept compiler-era %q: %s", obsolete, text)
		}
	}
}
