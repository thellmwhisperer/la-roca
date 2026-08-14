package vector

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
)

func TestCoreCLIWalksEverySourceThroughRocaExec(t *testing.T) {
	queries := []string{}
	runner := func(_ context.Context, executable string, args ...string) ([]byte, error) {
		if executable != "/synthetic/roca" {
			t.Fatalf("executable = %q", executable)
		}
		if !slices.Equal(args[:5], []string{"--json", "--db-path", "/synthetic/roca.db", "exec", "--max-chars"}) {
			t.Fatalf("prefix = %q", args)
		}
		statement := args[len(args)-1]
		queries = append(queries, statement)
		var rows []map[string]any
		switch {
		case strings.Contains(statement, "FROM "+corpusTable("memories")):
			rows = []map[string]any{{"id": 1, "content": "alpha memory", "source_session": "",
				"source_sequence": nil, "source_agent": "synthetic-agent",
				"metadata": `{"file_path":"notes.md"}`, "layer": "discovery",
				"origin": "agent", "created_at": "2026-08-14"}}
		case strings.Contains(statement, "FROM "+corpusTable("exchanges")):
			rows = []map[string]any{{"id": 2, "session_id": "s1", "exchange_number": 4,
				"text": "beta question\n\nbeta answer"}}
		case strings.Contains(statement, "FROM "+corpusTable("thinking_blocks")):
			rows = []map[string]any{{"id": 3, "session_id": "s1", "exchange_number": nil,
				"position_in_session": nil, "text": "gamma reasoning"}}
		case strings.Contains(statement, "FROM "+corpusTable("sessions")):
			rows = []map[string]any{{"session_id": "s1", "text": "delta session"}}
		default:
			return nil, fmt.Errorf("unexpected statement %s", statement)
		}
		return json.Marshal(map[string]any{"rows": rows})
	}
	core := CoreCLI{Executable: "/synthetic/roca", DBPath: "/synthetic/roca.db", Run: runner}
	var sources []sourceRow
	if err := core.WalkSources(context.Background(), func(source sourceRow) error {
		sources = append(sources, source)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(queries) != 4 || len(sources) != 4 {
		t.Fatalf("queries=%d sources=%+v", len(queries), sources)
	}
	if sources[0].filePath != "notes.md" || sources[1].stableID() != "exchanges/s1/4" ||
		!strings.Contains(sources[2].stableID(), "/unkeyed/") || sources[3].stableID() != "sessions/s1" {
		t.Fatalf("decoded sources = %+v", sources)
	}
}

func TestCoreCLIResolvesLiveTextAndQuotesStoredLocators(t *testing.T) {
	var statement string
	core := CoreCLI{Executable: "roca", Run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		statement = args[len(args)-1]
		return json.Marshal(map[string]any{"rows": []map[string]any{{"text": "current answer"}}})
	}}
	text, err := core.ResolveSource(context.Background(), "exchanges", locator{
		SessionID: "operator's-session", Ordinal: 7, HasOrdinal: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if text != "current answer" {
		t.Fatalf("text = %q", text)
	}
	if !strings.Contains(statement, "session_id='operator''s-session'") {
		t.Fatalf("stored locator was not SQL-quoted: %s", statement)
	}
}

func TestLargeCoreIdentifiersRemainExactAcrossJSON(t *testing.T) {
	const identifier int64 = 1152921504606846988
	core := CoreCLI{Executable: "roca", Run: func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		return []byte(`{"rows":[{"id":1152921504606846988,"content":"large id","source_session":"","source_sequence":null,"source_agent":"synthetic","metadata":"{}","layer":"discovery","origin":"agent","created_at":"2026-08-14"}]}`), nil
	}}
	page := corePages()[0]
	rows, err := core.query(context.Background(), page.query("0"))
	if err != nil {
		t.Fatal(err)
	}
	_, next, err := page.decode(rows[0])
	if err != nil {
		t.Fatal(err)
	}
	if next != fmt.Sprint(identifier) {
		t.Fatalf("large id cursor = %s, want %d", next, identifier)
	}
}
