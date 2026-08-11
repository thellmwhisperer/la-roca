package service_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/ingest"
	"github.com/thellmwhisperer/la-roca/internal/provider/service"
	"github.com/thellmwhisperer/la-roca/internal/store"
)

func TestPlainIngestAdoptsThePreviousSchemaAndFillsProvenance(t *testing.T) {
	home := t.TempDir()
	seedATranscript(t, home)
	paths := freshPaths(t)
	legacy, err := os.ReadFile(filepath.Join("testdata", "schema-v1.6.0.sql"))
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(paths.db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL().Exec(string(legacy)); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	svc, err := service.Open(service.Options{
		DBPath: paths.db, BackupDir: paths.backups, DataDir: paths.data,
		Sources: ingest.ResolveRoots(
			ingest.Environment{GOOS: "darwin", Home: home},
			ingest.Settings{WorkspaceRoots: []string{filepath.Join(home, "w")}}),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { svc.Close() })
	result, err := svc.Ingest(t.Context(), service.IngestRequest{})
	if err != nil {
		t.Fatalf("plain ingest: %v", err)
	}
	if result.Errors != 0 {
		t.Fatalf("ingest errors = %+v", result.ErrorDetails)
	}
	var model, provider string
	var tokensIn, tokensOut, reasoning int
	var cost float64
	err = svc.DB().SQL().QueryRow(`
		SELECT COALESCE(model, ''), COALESCE(provider, ''), COALESCE(tokens_in, -1),
		       COALESCE(tokens_out, -1), COALESCE(tokens_reasoning, -1), COALESCE(cost_usd, -1)
		FROM exchanges LIMIT 1`).Scan(&model, &provider, &tokensIn, &tokensOut, &reasoning, &cost)
	if err != nil {
		t.Fatal(err)
	}
	if model != "fixture-upgrade-model" || provider != "" || tokensIn != 3 || tokensOut != 2 ||
		reasoning != -1 || cost != -1 {
		t.Fatalf("adopted provenance = %q/%q %d/%d/%d/%v", model, provider,
			tokensIn, tokensOut, reasoning, cost)
	}
}

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

func TestInitBedrockIncludesTheDeclaredAnthropicExport(t *testing.T) {
	export := t.TempDir()
	for _, name := range []string{"conversations.json", "memories.json"} {
		raw, err := os.ReadFile(filepath.Join("..", "..", "ingest", "testdata", "anthropic-export", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(export, name), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	paths := freshPaths(t)
	svc, err := service.Open(service.Options{
		DBPath: paths.db, BackupDir: paths.backups, DataDir: paths.data,
		Sources: ingest.Roots{ClaudeWebExports: []string{export}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { svc.Close() })
	result, err := svc.Init(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Bedrock == nil || result.Bedrock.Timestamp != "2025-04-02T07:00:00.000Z" {
		t.Fatalf("init bedrock = %+v", result.Bedrock)
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
{"type":"assistant","timestamp":"2026-08-01T10:00:01Z","message":{"model":"fixture-upgrade-model","usage":{"input_tokens":3,"output_tokens":2},"content":[{"type":"text","text":"to know where you are"}]}}
`)
	write(filepath.Join(project, "memory", "sextant.md"),
		"---\nname: the-sextant\ntype: project\n---\nThe sextant measures the angle, not the position.\n")
}
