package ingest

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDeclaredChatGPTExportIsIdempotentAndNewerExportAddsOnlyDelta(t *testing.T) {
	db := rocaDatabase(t)
	ctx := context.Background()
	firstRoot := filepath.Join("testdata", "openai-export-v1")
	newerRoot := filepath.Join("testdata", "openai-export-v2")

	first, err := Run(ctx, db, registry(t), Options{Roots: Roots{ChatGPTWebExports: []string{firstRoot}}})
	if err != nil {
		t.Fatal(err)
	}
	if first.Delta != (Tables{Sessions: 1, Exchanges: 2}) {
		t.Fatalf("first delta = %+v", first.Delta)
	}
	if first.RecordsExcluded != 5 || first.RecordsDiscarded != 0 {
		t.Fatalf("first exclusions/discards = %d/%d: %+v", first.RecordsExcluded,
			first.RecordsDiscarded, first.DiscardSummary)
	}
	var project string
	if err := db.SQL().QueryRow(
		"SELECT COALESCE(project, '') FROM sessions WHERE session_id = ?",
		"40000000-0000-4000-8000-000000000001").Scan(&project); err != nil {
		t.Fatal(err)
	}
	if project != "" {
		t.Fatalf("ChatGPT web project = %q, want unset", project)
	}

	second, err := Run(ctx, db, registry(t), Options{Roots: Roots{ChatGPTWebExports: []string{firstRoot}}})
	if err != nil {
		t.Fatal(err)
	}
	if second.FilesSkipped != 1 || second.Delta != (Tables{}) {
		t.Fatalf("same export rerun = %+v", second)
	}

	newer, err := Run(ctx, db, registry(t), Options{Roots: Roots{ChatGPTWebExports: []string{newerRoot}}})
	if err != nil {
		t.Fatal(err)
	}
	if newer.Delta != (Tables{Sessions: 1, Exchanges: 2}) {
		t.Fatalf("newer export delta = %+v", newer.Delta)
	}
	counts := newer.Sources["chatgpt-web"]
	if counts.Sessions != 1 || counts.SessionsUpdated != 1 || counts.Exchanges != 2 ||
		counts.ExchangesUnchanged != 2 {
		t.Fatalf("newer export counts = %+v", counts)
	}
	if got := countRows(t, db.SQL(), "sessions"); got != 2 {
		t.Fatalf("sessions = %d, want 2", got)
	}
	if got := countRows(t, db.SQL(), "exchanges"); got != 4 {
		t.Fatalf("exchanges = %d, want 4", got)
	}
	var model, provider string
	if err := db.SQL().QueryRow(`SELECT model, provider FROM exchanges
		WHERE session_id = ? AND agent_text = ?`,
		"40000000-0000-4000-8000-000000000001", "Call it Amber Kestrel.").Scan(&model, &provider); err != nil {
		t.Fatal(err)
	}
	if model != "gpt-synthetic-message" || provider != "openai" {
		t.Fatalf("stored provenance = %q/%q", model, provider)
	}
}

func TestChatGPTExportIsNeverDiscoveredWithoutADeclaration(t *testing.T) {
	roots := ResolveRoots(Environment{GOOS: "linux", Home: t.TempDir()}, Settings{})
	plan := Scan(roots)
	if plan.Scanned["chatgpt_web_export_files"] != 0 {
		t.Fatalf("undeclared export scan = %+v", plan.Scanned)
	}
	for _, target := range plan.Targets {
		if target.SourceAgent == "chatgpt-web" {
			t.Fatalf("undeclared export target = %+v", target)
		}
	}
}

func TestDeclaredShardedChatGPTExportIngestsEveryShard(t *testing.T) {
	db := rocaDatabase(t)
	root := filepath.Join("testdata", "openai-export-sharded")
	result, err := Run(context.Background(), db, registry(t), Options{
		Roots: Roots{ChatGPTWebExports: []string{root}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Delta != (Tables{Sessions: 2, Exchanges: 3}) {
		t.Fatalf("sharded export delta = %+v", result.Delta)
	}
	// The two shards are read; codex.json is the third file the scan counts, left
	// out by design and reported as such rather than warned about or hidden.
	if result.Scanned["chatgpt_web_export_files"] != 3 || result.FilesRead != 2 {
		t.Fatalf("sharded files scanned/read = %d/%d, want 3/2",
			result.Scanned["chatgpt_web_export_files"], result.FilesRead)
	}
	if result.FilesExcluded != 1 || len(result.Warnings) != 0 {
		t.Fatalf("companion accounting: excluded=%d warnings=%v, want 1 and none",
			result.FilesExcluded, result.Warnings)
	}
	for text, wantModel := range map[string]string{
		"Call it Amber Kestrel.":                      "gpt-synthetic-sharded-message",
		"The alternate branch calls it Silver Heron.": "gpt-synthetic-sharded-default",
		"Calibrate the synthetic star tracker.":       "gpt-synthetic-second-default",
	} {
		var model, provider string
		if err := db.SQL().QueryRow(`SELECT model, provider FROM exchanges
			WHERE agent_text = ?`, text).Scan(&model, &provider); err != nil {
			t.Fatal(err)
		}
		if model != wantModel || provider != "openai" {
			t.Errorf("%q provenance = %q/%q, want %q/openai", text, model, provider, wantModel)
		}
	}
}

// The richer legacy row wins however the two snapshots reach the corpus: in one
// directory, in one run from two directories, or in a run months later. The last
// case is the real one, so the preference cannot live in the read order alone.
func TestOverlappingChatGPTExportsKeepTheRicherLegacyRows(t *testing.T) {
	sharded := filepath.Join("testdata", "openai-export-sharded")
	legacy := filepath.Join("testdata", "openai-export-v1")
	for name, runs := range map[string][][]string{
		"both shapes in one declared directory":   {{chatGPTExportWithBothShapes(t)}},
		"a legacy export declared beside shards":  {{sharded, legacy}},
		"a legacy export ingested in a later run": {{sharded}, {legacy}},
	} {
		t.Run(name, func(t *testing.T) {
			db := rocaDatabase(t)
			for _, declared := range runs {
				if _, err := Run(context.Background(), db, registry(t), Options{
					Roots: Roots{ChatGPTWebExports: declared},
				}); err != nil {
					t.Fatal(err)
				}
			}
			if got := countRows(t, db.SQL(), "sessions"); got != 2 {
				t.Fatalf("sessions = %d, want 2", got)
			}
			if got := countRows(t, db.SQL(), "exchanges"); got != 3 {
				t.Fatalf("exchanges = %d, want 3", got)
			}
			for text, wantModel := range map[string]string{
				"Call it Amber Kestrel.":                      "gpt-synthetic-message",
				"The alternate branch calls it Silver Heron.": "gpt-synthetic-default",
			} {
				var model string
				if err := db.SQL().QueryRow(`SELECT model FROM exchanges WHERE agent_text = ?`, text).
					Scan(&model); err != nil {
					t.Fatal(err)
				}
				if model != wantModel {
					t.Errorf("%q kept model %q, want the richer legacy model %q", text, model, wantModel)
				}
			}
		})
	}
}

func TestDeclaredChatGPTExportDiagnosesEveryDeclarationItCannotRead(t *testing.T) {
	present := t.TempDir()
	for root, want := range map[string]string{
		present: "unrecognized OpenAI export layout",
		filepath.Join(present, "never-extracted"): "cannot be read",
	} {
		plan := Scan(Roots{ChatGPTWebExports: []string{root}})
		if len(plan.Warnings) != 1 || !strings.Contains(plan.Warnings[0], root) ||
			!strings.Contains(plan.Warnings[0], want) {
			t.Errorf("warnings for %q = %v, want one naming %q", root, plan.Warnings, want)
		}
	}
}

// chatGPTExportWithBothShapes is the directory an operator ends up with after
// extracting a legacy export and a sharded one over each other.
func chatGPTExportWithBothShapes(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, fixture := range []string{
		filepath.Join("openai-export-v1", "conversations.json"),
		filepath.Join("openai-export-sharded", "conversations-000.json"),
		filepath.Join("openai-export-sharded", "conversations-001.json"),
	} {
		raw, err := os.ReadFile(filepath.Join("testdata", fixture))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, filepath.Base(fixture)), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}
