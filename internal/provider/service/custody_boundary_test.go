package service

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/ingest"
)

func TestCorpusCustodyCanOnlyBeWrittenByIngest(t *testing.T) {
	svc := corpusResidentService(t)
	if _, err := svc.Init(t.Context()); err != nil {
		t.Fatal(err)
	}
	want := custodyCounts(t, svc)

	for _, req := range []StoreRequest{
		{Layer: "project", Content: "a core memory", Origin: "human"},
		{Layer: "project", Content: "a plugin-origin memory", Origin: "plugin:fixture"},
	} {
		if _, err := svc.Store(t.Context(), req); err != nil {
			t.Fatalf("store origin %q: %v", req.Origin, err)
		}
	}
	for _, statement := range []string{
		`INSERT INTO plugin_roca_corpus.sessions (session_id, source_agent) VALUES ('forbidden', 'fixture')`,
		`DELETE FROM sessions`,
	} {
		if _, err := svc.Exec(t.Context(), ExecRequest{SQL: statement}); err == nil {
			t.Fatalf("exec accepted corpus write %q", statement)
		}
	}
	if got := custodyCounts(t, svc); got != want {
		t.Fatalf("store/exec changed corpus custody: got %+v, want %+v", got, want)
	}

	home := t.TempDir()
	project := filepath.Join(home, ".claude", "projects", "-fixture")
	if err := os.MkdirAll(project, 0o700); err != nil {
		t.Fatal(err)
	}
	transcript := `{"type":"user","timestamp":"2026-08-16T10:00:00Z","cwd":"/fixture","message":{"content":"where is the exact boundary"}}
{"type":"assistant","timestamp":"2026-08-16T10:00:01Z","message":{"model":"fixture-model","content":[{"type":"thinking","thinking":"follow the custody law"},{"type":"text","text":"inside ingest"}]}}
`
	if err := os.WriteFile(filepath.Join(project, "11111111-2222-3333-4444-555555555555.jsonl"),
		[]byte(transcript), 0o600); err != nil {
		t.Fatal(err)
	}
	svc.opts.Sources = ingest.ResolveRoots(ingest.Environment{GOOS: "darwin", Home: home}, ingest.Settings{})
	if _, err := svc.Ingest(t.Context(), IngestRequest{}); err != nil {
		t.Fatal(err)
	}
	got := custodyCounts(t, svc)
	if got.sessions <= want.sessions || got.exchanges <= want.exchanges || got.thinking <= want.thinking {
		t.Fatalf("ingest did not populate corpus custody: before %+v, after %+v", want, got)
	}
	for table := range map[string]bool{"sessions": true, "exchanges": true, "thinking_blocks": true} {
		var rows int
		if err := svc.db.SQL().QueryRow("SELECT COUNT(*) FROM " + table).Scan(&rows); err != nil {
			t.Fatal(err)
		}
		if rows != 0 {
			t.Fatalf("ingest wrote %d %s rows to core instead of corpus", rows, table)
		}
	}
}

type custodyRowCounts struct {
	sessions, exchanges, thinking int
}

func custodyCounts(t *testing.T, svc *Service) custodyRowCounts {
	t.Helper()
	var result custodyRowCounts
	for table, destination := range map[string]*int{
		"sessions": &result.sessions, "exchanges": &result.exchanges, "thinking_blocks": &result.thinking,
	} {
		if err := svc.corpus.SQL().QueryRow("SELECT COUNT(*) FROM " + table).Scan(destination); err != nil {
			t.Fatal(err)
		}
	}
	return result
}
