package service_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/ingest"
	"github.com/thellmwhisperer/la-roca/internal/provider/service"
)

// What the ingest wrote has to be answerable in the same command. A memory that is
// in the database and cannot be found is worse than one that is not there: it
// reads as data loss.
func TestWhatTheIngestWritesIsSearchableAtOnce(t *testing.T) {
	home := t.TempDir()
	svc := serviceOverTheSources(t, home)
	ctx := context.Background()

	if _, err := svc.Init(ctx); err != nil {
		t.Fatalf("init: %v", err)
	}
	// The transcript lands after the bootstrap, which is the shape of the real
	// flow: init reads the disk once and the agents keep writing to it
	// afterwards. Seeding it first would leave this ingest nothing to do,
	// because the bootstrap would already have read it.
	seedATranscript(t, home)
	result, err := svc.Ingest(ctx, service.IngestRequest{})
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if result.Errors != 0 {
		t.Fatalf("errors = %d: %+v", result.Errors, result.ErrorDetails)
	}
	if result.Delta.Exchanges == 0 || result.Delta.Memories == 0 {
		t.Fatalf("delta = %+v", result.Delta)
	}
	if result.Index == nil {
		t.Fatal("the ingest did not refresh the index")
	}

	// The full-text index has the exchange the transcript carried.
	var hits int
	if err := svc.DB().SQL().QueryRow(
		`SELECT COUNT(*) FROM exchanges_fts WHERE exchanges_fts MATCH 'sextant'`).
		Scan(&hits); err != nil {
		t.Fatalf("search the index: %v", err)
	}
	if hits == 0 {
		t.Error("what the ingest wrote is not in the full-text index")
	}

}

// A dry run through the service writes nothing and refreshes no index.
func TestTheDryRunThroughTheServiceTouchesNothing(t *testing.T) {
	home := t.TempDir()
	svc := serviceOverTheSources(t, home)
	ctx := context.Background()
	if _, err := svc.Init(ctx); err != nil {
		t.Fatalf("init: %v", err)
	}
	seedATranscript(t, home)

	result, err := svc.Ingest(ctx, service.IngestRequest{DryRun: true})
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if !result.DryRun || result.Index != nil {
		t.Errorf("result = %+v", result)
	}
	var exchanges int
	if err := svc.DB().SQL().QueryRow(`SELECT COUNT(*) FROM exchanges`).Scan(&exchanges); err != nil {
		t.Fatalf("count: %v", err)
	}
	if exchanges != 0 {
		t.Errorf("exchanges = %d, want none: a dry run writes nothing", exchanges)
	}
	var memories int
	if err := svc.DB().SQL().QueryRow(`SELECT COUNT(*) FROM memories`).Scan(&memories); err != nil {
		t.Fatalf("count memories: %v", err)
	}
	if memories != 0 {
		t.Errorf("memories = %d, want none: a dry run persists no memory either", memories)
	}
}

// serviceOverTheSources opens an installation whose sources are the sandbox home's,
// with no model cascade: the ingest never needs one.
func serviceOverTheSources(t *testing.T, home string) *service.Service {
	t.Helper()
	paths := freshPaths(t)
	svc, err := service.Open(service.Options{
		DBPath:    paths.db,
		BackupDir: paths.backups,
		DataDir:   paths.data,
		Version:   "0.0.0-test",
		Commit:    "0123456789abcdef",
		Sources: ingest.ResolveRoots(
			ingest.Environment{GOOS: "darwin", Home: home},
			ingest.Settings{WorkspaceRoots: []string{filepath.Join(home, "w")}}),
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { svc.Close() })
	return svc
}

// seedATranscript writes one Claude transcript and one memory file, both invented.
func seedATranscript(t *testing.T, home string) {
	t.Helper()
	demo := filepath.Join(home, "w", "demo")
	encoded := "-" + strings.ReplaceAll(strings.TrimPrefix(demo, "/"), "/", "-")
	project := filepath.Join(home, ".claude", "projects", encoded)

	write := func(path, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatalf("create %s: %v", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	write(filepath.Join(project, "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee.jsonl"), `
{"type":"user","timestamp":"2026-08-01T10:00:00Z","cwd":"`+demo+`","message":{"content":"what is the sextant for"}}
{"type":"assistant","timestamp":"2026-08-01T10:00:01Z","message":{"content":[{"type":"text","text":"to know where you are"}]}}
`)
	write(filepath.Join(project, "memory", "sextant.md"),
		"---\nname: the-sextant\ntype: project\n---\nThe sextant measures the angle, not the position.\n")
}
