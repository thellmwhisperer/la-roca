package ingest

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
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

func TestDeclaredChatGPTExportAssignsOnlySnorlaxVirtualProjects(t *testing.T) {
	db := rocaDatabase(t)
	result, err := Run(context.Background(), db, registry(t), Options{
		Roots: Roots{ChatGPTWebExports: []string{filepath.Join("testdata", "openai-export-projects")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Delta.Sessions != 4 || result.Delta.Exchanges != 4 {
		t.Fatalf("project export delta = %+v", result.Delta)
	}
	rows, err := db.SQL().Query(`SELECT session_id, COALESCE(project, '') FROM sessions
		WHERE source_agent = 'chatgpt-web' ORDER BY session_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string]string{}
	for rows.Next() {
		var id, project string
		if err := rows.Scan(&id, &project); err != nil {
			t.Fatal(err)
		}
		got[id] = project
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"50000000-0000-4000-8000-000000000001": "g-p-syntheticorchard000000000000",
		"50000000-0000-4000-8000-000000000002": "g-p-syntheticorchard000000000000",
		"50000000-0000-4000-8000-000000000003": "",
		"50000000-0000-4000-8000-000000000004": "",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("session projects = %v, want %v", got, want)
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
	result := ingestShardedChatGPTExport(t, db)
	if result.Delta != (Tables{Sessions: 3, Exchanges: 4}) {
		t.Fatalf("sharded export delta = %+v", result.Delta)
	}
	// The two shards and codex.json are all read; companions that are still out
	// of scope are ignored outright rather than excluded.
	if result.Scanned["chatgpt_web_export_files"] != 3 || result.FilesRead != 3 {
		t.Fatalf("sharded files scanned/read = %d/%d, want 3/3",
			result.Scanned["chatgpt_web_export_files"], result.FilesRead)
	}
	if result.FilesExcluded != 0 || len(result.Warnings) != 0 {
		t.Fatalf("companion accounting: excluded=%d warnings=%v, want 0 and none",
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
			if got := countRows(t, db.SQL(), "sessions WHERE source_agent = 'chatgpt-web'"); got != 2 {
				t.Fatalf("chatgpt-web sessions = %d, want 2", got)
			}
			if got := countRows(t, db.SQL(), "exchanges e JOIN sessions s ON s.session_id = e.session_id WHERE s.source_agent = 'chatgpt-web'"); got != 3 {
				t.Fatalf("chatgpt-web exchanges = %d, want 3", got)
			}
			expectRicherLegacyModels(t, db.SQL())
		})
	}
}

// A row a build before the signal record wrote says nothing about how rich the
// snapshot behind it was. Unrecorded is not zero: the row is filled and never
// overwritten, so an upgrade cannot cost a corpus the provenance it already had.
func TestChatGPTExchangesWithNoRecordedSignalAreOnlyFilled(t *testing.T) {
	db := rocaDatabase(t)
	ctx := context.Background()
	for _, declared := range []string{"openai-export-v1", "openai-export-sharded"} {
		if _, err := Run(ctx, db, registry(t), Options{Roots: Roots{
			ChatGPTWebExports: []string{filepath.Join("testdata", declared)},
		}}); err != nil {
			t.Fatal(err)
		}
		if declared == "openai-export-v1" {
			forgetRecordedSignals(t, db.SQL())
		}
	}
	expectRicherLegacyModels(t, db.SQL())
}

// forgetRecordedSignals leaves the session metadata a build before the signal
// record wrote: the exchange identities and their fingerprints, and no richness.
func forgetRecordedSignals(t *testing.T, db *sql.DB) {
	t.Helper()
	recording := `sessions WHERE metadata LIKE '%source_exchange_signal%'`
	if got := countRows(t, db, recording); got != 1 {
		t.Fatalf("sessions recording a signal = %d, want the one to forget", got)
	}
	if _, err := db.Exec(`UPDATE sessions
		SET metadata = json_remove(metadata, '$.chatgpt_web.source_exchange_signal')
		WHERE source_agent = 'chatgpt-web'`); err != nil {
		t.Fatal(err)
	}
	if got := countRows(t, db, recording); got != 0 {
		t.Fatalf("sessions still recording a signal = %d", got)
	}
}

// expectRicherLegacyModels is what the corpus shows once the legacy snapshot has
// had its say: the model that snapshot states per message, and the conversation
// default it falls back to.
func expectRicherLegacyModels(t *testing.T, db *sql.DB) {
	t.Helper()
	for text, wantModel := range map[string]string{
		"Call it Amber Kestrel.":                      "gpt-synthetic-message",
		"The alternate branch calls it Silver Heron.": "gpt-synthetic-default",
	} {
		var model string
		if err := db.QueryRow(`SELECT model FROM exchanges WHERE agent_text = ?`, text).
			Scan(&model); err != nil {
			t.Fatal(err)
		}
		if model != wantModel {
			t.Errorf("%q kept model %q, want the richer legacy model %q", text, model, wantModel)
		}
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

func ingestShardedChatGPTExport(t *testing.T, db Database) Result {
	t.Helper()
	root := filepath.Join("testdata", "openai-export-sharded")
	result, err := Run(context.Background(), db, registry(t), Options{
		Roots: Roots{ChatGPTWebExports: []string{root}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
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
