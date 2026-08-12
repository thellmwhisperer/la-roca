package ingest

import (
	"context"
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
	if result.Scanned["chatgpt_web_export_files"] != 2 || result.FilesRead != 2 {
		t.Fatalf("sharded files scanned/read = %d/%d, want 2/2",
			result.Scanned["chatgpt_web_export_files"], result.FilesRead)
	}
	if result.FilesExcluded != 0 || len(result.Warnings) != 0 {
		t.Fatalf("expected companions were reported: excluded=%d warnings=%v",
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

func TestOverlappingChatGPTExportsKeepTheRicherLegacyRows(t *testing.T) {
	db := rocaDatabase(t)
	sharded := filepath.Join("testdata", "openai-export-sharded")
	legacy := filepath.Join("testdata", "openai-export-v1")
	result, err := Run(context.Background(), db, registry(t), Options{
		Roots: Roots{ChatGPTWebExports: []string{sharded, legacy}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Delta != (Tables{Sessions: 2, Exchanges: 3}) {
		t.Fatalf("overlapping export delta = %+v", result.Delta)
	}
	var sessions, exchanges int
	if err := db.SQL().QueryRow(`SELECT COUNT(*) FROM sessions
		WHERE session_id = '40000000-0000-4000-8000-000000000001'`).Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	if err := db.SQL().QueryRow(`SELECT COUNT(*) FROM exchanges
		WHERE session_id = '40000000-0000-4000-8000-000000000001'`).Scan(&exchanges); err != nil {
		t.Fatal(err)
	}
	if sessions != 1 || exchanges != 2 {
		t.Fatalf("overlap stored sessions/exchanges = %d/%d, want 1/2", sessions, exchanges)
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
			t.Errorf("%q kept model %q, want richer legacy model %q", text, model, wantModel)
		}
	}
}

func TestDeclaredChatGPTExportWarnsAboutAnUnrecognizedLayout(t *testing.T) {
	root := t.TempDir()
	plan := Scan(Roots{ChatGPTWebExports: []string{root}})
	if len(plan.Warnings) != 1 || !strings.Contains(plan.Warnings[0], root) ||
		!strings.Contains(plan.Warnings[0], "unrecognized OpenAI export layout") {
		t.Fatalf("unrecognized layout warnings = %v", plan.Warnings)
	}
}
