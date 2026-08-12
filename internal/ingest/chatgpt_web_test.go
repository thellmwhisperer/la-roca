package ingest

import (
	"context"
	"path/filepath"
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
