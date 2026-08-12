package service_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/ingest"
	"github.com/thellmwhisperer/la-roca/internal/provider/service"
)

func TestFirstOrdinaryCallUpgradesTheTokenizerWithProgress(t *testing.T) {
	paths := freshPaths(t)
	old := serviceOn(t, paths)
	if _, err := old.Init(t.Context()); err != nil {
		t.Fatal(err)
	}
	db := old.DB().SQL()
	if _, err := db.Exec(`INSERT INTO memories (layer, content, origin)
		VALUES ('project', 'qué pasó during the synthetic year', 'agent')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO memories_fts(memories_fts) VALUES ('delete-all')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM search_state
		WHERE key = 'lexical_index' OR key LIKE 'lexical_index:%';
		UPDATE search_state SET value = 'rebuilding-unicode61-remove-diacritics-2'
		WHERE key = 'lexical_tokenizer'`); err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}

	var progress []string
	upgraded := serviceOn(t, paths, func(options *service.Options) {
		options.Progress = func(line string) { progress = append(progress, line) }
	})
	if _, err := upgraded.Health(t.Context(), service.HealthRequest{}); err != nil {
		t.Fatal(err)
	}
	if !containsProgress(progress, "rebuilding for accent-insensitive search") {
		t.Fatalf("upgrade progress is missing: %v", progress)
	}
	var matches int
	if err := upgraded.DB().SQL().QueryRow(
		`SELECT COUNT(*) FROM memories_fts WHERE memories_fts MATCH '"que" AND "paso"'`,
	).Scan(&matches); err != nil {
		t.Fatal(err)
	}
	if matches != 1 {
		t.Errorf("matches after automatic upgrade = %d, want 1", matches)
	}
}

func TestIngestNarratesEachSourceAndItsDelta(t *testing.T) {
	var progress []string
	home := t.TempDir()
	paths := freshPaths(t)
	svc := serviceOn(t, paths, func(options *service.Options) {
		options.Progress = func(line string) { progress = append(progress, line) }
		options.Sources = ingest.ResolveRoots(
			ingest.Environment{GOOS: "darwin", Home: home},
			ingest.Settings{WorkspaceRoots: []string{filepath.Join(home, "w")}})
	})
	if _, err := svc.Init(t.Context()); err != nil {
		t.Fatal(err)
	}
	seedATranscript(t, home)
	progress = nil

	if _, err := svc.Ingest(t.Context(), service.IngestRequest{}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"ingest: reading claude",
		"ingest: claude complete ·",
		"sessions=", "exchanges=", "memories=",
	} {
		if !containsProgress(progress, want) {
			t.Errorf("progress does not carry %q:\n%s", want, strings.Join(progress, "\n"))
		}
	}
}

func TestInitReportsRowsAfterItsBootstrapIngest(t *testing.T) {
	home := t.TempDir()
	seedATranscript(t, home)
	paths := freshPaths(t)
	svc := serviceOn(t, paths, func(options *service.Options) {
		options.Sources = ingest.ResolveRoots(
			ingest.Environment{GOOS: "darwin", Home: home},
			ingest.Settings{WorkspaceRoots: []string{filepath.Join(home, "w")}})
	})

	result, err := svc.Init(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if result.Rows.Memories == 0 || result.Rows.Exchanges == 0 {
		t.Fatalf("final rows are stale after bootstrap ingest: %+v", result.Rows)
	}
	if result.Rows != result.Ingest.After {
		t.Fatalf("init rows = %+v, ingest left database at %+v", result.Rows, result.Ingest.After)
	}
}

// containsProgress says whether any progress line carries the wanted text.
func containsProgress(lines []string, want string) bool {
	for _, line := range lines {
		if strings.Contains(line, want) {
			return true
		}
	}
	return false
}
