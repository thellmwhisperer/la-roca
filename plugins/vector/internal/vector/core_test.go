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
			if !strings.Contains(statement, "$.project_name") || strings.Contains(statement, "metadata AS") ||
				strings.Contains(statement, "COALESCE(project,") {
				t.Fatalf("session projection did not select only the project label: %s", statement)
			}
			rows = []map[string]any{{"session_id": "s1", "title": "delta session",
				"project_name": "Synthetic orchard"}}
		default:
			return nil, fmt.Errorf("unexpected statement %s", statement)
		}
		return json.Marshal(map[string]any{"rows": rows})
	}
	core := CoreCLI{Executable: "/synthetic/roca", DBPath: "/synthetic/roca.db", Run: runner}
	var sources []sourceRow
	if err := core.WalkSources(context.Background(), "", func(source sourceRow) error {
		sources = append(sources, source)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(queries) != 4 || len(sources) != 4 {
		t.Fatalf("queries=%d sources=%+v", len(queries), sources)
	}
	if sources[0].filePath != "notes.md" || sources[1].stableID() != "exchanges/s1/4" ||
		!strings.Contains(sources[2].stableID(), "/unkeyed/") || sources[3].stableID() != "sessions/s1" ||
		sources[3].text != "delta session\nSynthetic orchard" {
		t.Fatalf("decoded sources = %+v", sources)
	}
	queries, sources = nil, nil
	if err := core.WalkSources(context.Background(), "sessions", func(source sourceRow) error {
		sources = append(sources, source)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(queries) != 1 || len(sources) != 1 || !strings.Contains(queries[0], corpusTable("sessions")) {
		t.Fatalf("targeted session walk queried %d pages and returned %+v", len(queries), sources)
	}
}

func TestSessionEmbeddingTextKeepsOnlyHumanContent(t *testing.T) {
	const hash = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	const uuid = "123e4567-e89b-12d3-a456-426614174000"
	tests := []struct {
		name        string
		title       string
		project     string
		projectName string
		want        string
	}{
		{name: "human fields", title: "Scottish public health " + hash + " " + uuid,
			project: "health-research", want: "Scottish public health"},
		{name: "opaque project", title: "Personal wellbeing",
			project: "g-p-syntheticsextant000000000000", want: "Personal wellbeing"},
		{name: "paths and short hashes", title: "Useful /synthetic/work/health deadbeef",
			project: `C:\synthetic\work\health`, want: "Useful"},
		{name: "uppercase and mixed case hashes", title: "Useful DEADBEEF DeadBeef",
			want: "Useful"},
		{name: "natural hexadecimal letters", title: "Defaced artifact recovery",
			project: "", want: "Defaced artifact recovery"},
		{name: "slash-bearing acronyms", title: "CI/CD rollout plan",
			want: "CI/CD rollout plan"},
		{name: "slash-bearing protocol", title: "HTTP/2 investigation",
			want: "HTTP/2 investigation"},
		{name: "path with spaces", title: "/Users/synthetic/Health Research",
			project: "", want: ""},
		{name: "human metadata label", title: "Synthetic canvas", project: uuid,
			projectName: "Synthetic orchard", want: "Synthetic canvas\nSynthetic orchard"},
		{name: "json key fragment", title: `Useful "source_exchange_fingerprints":["deadbeef"]`,
			project: "", want: "Useful"},
		{name: "serialized suffix", title: `Useful session {"source_exchange_fingerprints":["` + hash + `"],"enabled":true}`,
			project: "/synthetic/work/health",
			want:    "Useful session"},
		{name: "serialized title", title: `{"source_exchange_fingerprints":["` + hash + `"],"enabled":true}`,
			project: "", want: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			row, _, err := decodeSession(map[string]any{
				"session_id": "synthetic-session", "title": test.title, "project": test.project,
				"project_name": test.projectName,
				"metadata":     `{"source_exchange_fingerprints":["` + hash + `"],"default":true}`,
			})
			if err != nil {
				t.Fatal(err)
			}
			if row.text != test.want {
				t.Fatalf("session embedding text = %q, want %q", row.text, test.want)
			}
			for _, contaminant := range []string{"source_exchange_fingerprints", "default", hash, uuid, "{", "}"} {
				if strings.Contains(row.text, contaminant) {
					t.Fatalf("session embedding text contains %q: %q", contaminant, row.text)
				}
			}
		})
	}
}

func TestCoreCLIResolvesSessionWithHumanProjectName(t *testing.T) {
	var statement string
	core := CoreCLI{Executable: "roca", Run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		statement = args[len(args)-1]
		return json.Marshal(map[string]any{"rows": []map[string]any{{
			"title":        "Synthetic canvas",
			"project_name": "Synthetic orchard",
		}}})
	}}
	text, err := core.ResolveSource(context.Background(), "sessions", locator{SessionID: "session-design"})
	if err != nil {
		t.Fatal(err)
	}
	if text != "Synthetic canvas\nSynthetic orchard" {
		t.Fatalf("session text = %q", text)
	}
	if !strings.Contains(statement, "$.project_name") || strings.Contains(statement, "metadata AS") ||
		strings.Contains(statement, "COALESCE(project,") {
		t.Fatalf("session resolution did not select only the project label: %s", statement)
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
