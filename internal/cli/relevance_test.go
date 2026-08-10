package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/service"
)

func TestSearchExcerptKeepsTheMatchVisible(t *testing.T) {
	full := strings.Repeat("irrelevant preamble ", 30) +
		"the resonancia result is genuinely relevant " + strings.Repeat("tail ", 30)
	row := map[string]any{"source": "exchange", "text": full}

	line := rowOutput([]string{"source", "text"}, []map[string]any{row}, "resonancia")
	if !strings.Contains(line, "resonancia") {
		t.Fatalf("the visible excerpt hides the match:\n%s", line)
	}
	if !strings.Contains(line, ",…") {
		t.Fatalf("the visible excerpt still starts at the beginning:\n%s", line)
	}
	if row["text"] != full {
		t.Fatal("human rendering changed the full text kept for JSON")
	}
}

// The keyword rescue renders its route and the rows it found, and never reaches
// for a teach command that no longer exists.
func TestKeywordRescueRendersItsRouteAndRows(t *testing.T) {
	var output strings.Builder
	render(&cliEnv{out: &output}, service.QueryResult{
		Question: "what is v1 scope", Path: service.PathKeyword,
		Match: service.MatchFound, LatencyMS: 3,
		Columns: []string{"source", "id", "text", "created_at"}, RowCount: 1,
		Rows: []map[string]any{{"source": "memory", "id": int64(2),
			"created_at": "2026-08-07 10:23:35",
			"text":       "La Roca v1 scope is memory and query"}},
	}, "")
	got := output.String()
	if !strings.Contains(got, "route keyword_fallback") {
		t.Fatalf("the route line is missing:\n%s", got)
	}
	if !strings.Contains(got, "La Roca v1 scope is memory and query") {
		t.Fatalf("the row text is missing:\n%s", got)
	}
	if strings.Contains(got, "roca teach") {
		t.Fatalf("the render suggests a teach command that no longer exists:\n%s", got)
	}
}

func TestEmptySearchSuggestsHowToBroadenIt(t *testing.T) {
	var output strings.Builder
	render(&cliEnv{out: &output}, service.QueryResult{
		Question: "fotosintesis de los tulipanes holandeses", Path: service.PathKeyword,
		Match:   service.MatchEmpty,
		Message: "no matches in memory for that search",
	}, "")
	got := output.String()
	if !strings.Contains(got, "no matches in memory for that search") {
		t.Fatalf("the honest empty message is missing:\n%s", got)
	}
	if !strings.Contains(got, "broaden the search") {
		t.Fatalf("the broaden suggestion is missing:\n%s", got)
	}
}

// The JSON envelope always carries version and source_sha so a program knows
// which build answered, and it never carries the database path.
func TestJSONCarriesVersionAndSourceSHAAndNotTheDBPath(t *testing.T) {
	var output strings.Builder
	result := service.QueryResult{
		Question: "what is v1 scope", Path: service.PathKeyword,
		Columns: []string{"source", "id", "text", "created_at"},
		Rows: []map[string]any{{"source": "memory", "id": int64(2),
			"created_at": "2026-08-07 10:23:35", "text": "v1 scope"}},
		RowCount: 1, Match: service.MatchFound, LatencyMS: 3,
		Version: "v1", SourceSHA: "abc",
	}
	if err := (&cliEnv{out: &output}).printJSON(result); err != nil {
		t.Fatal(err)
	}
	body := output.String()
	for _, want := range []string{
		`"path": "keyword_fallback"`,
		`"version": "v1"`,
		`"source_sha": "abc"`,
		`"row_count": 1`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("JSON is missing %q:\n%s", want, body)
		}
	}
	// A program that marshals the result must not see a field that could hold a
	// classifier confidence the model path does not produce.
	var loose map[string]any
	if err := json.Unmarshal([]byte(body), &loose); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, banned := range []string{"confidence", "fallback_reason", "refusal"} {
		if _, ok := loose[banned]; ok {
			t.Errorf("JSON carries the removed field %q", banned)
		}
	}
}

// When the second inference call answered, the prose replaces the raw rows: a
// human reads the answer, not the table it was built from.
func TestRenderPrefersProseOverRows(t *testing.T) {
	var output strings.Builder
	render(&cliEnv{out: &output}, service.QueryResult{
		Question: "que se decidió sobre el formato", Path: service.PathLLM, Engine: "codex",
		Model: "codex-model", Match: service.MatchFound, RowCount: 1,
		Columns: []string{"text"}, Rows: []map[string]any{{"text": "fila cruda"}},
	}, "El formato se decidió en una memoria.")
	got := output.String()
	if !strings.Contains(got, "El formato se decidió en una memoria.") {
		t.Fatalf("the prose answer is missing:\n%s", got)
	}
	if strings.Contains(got, "fila cruda") {
		t.Fatalf("the raw rows leaked past the prose:\n%s", got)
	}
}

// When the second call did not answer (empty prose), the row renderer is the
// floor the prose sits on, and it still shows the rows.
func TestRenderFallsBackToRowsWhenThereIsNoProse(t *testing.T) {
	var output strings.Builder
	render(&cliEnv{out: &output}, service.QueryResult{
		Question: "what is v1 scope", Path: service.PathLLM, Engine: "codex",
		Match: service.MatchFound, RowCount: 1,
		Columns: []string{"text"}, Rows: []map[string]any{{"text": "v1 scope is memory"}},
	}, "")
	if !strings.Contains(output.String(), "v1 scope is memory") {
		t.Fatalf("the rows disappeared when the prose was empty:\n%s", output.String())
	}
}
