package vector

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestReportVectorizationReadsSidecarFactsAndNeverInventZero(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "state")
	dataDir := filepath.Join(root, "data")
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, WorkerClaimFilename), []byte(fmt.Sprintf("%d\n", os.Getpid())), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dataDir, "logs"), 0o700); err != nil {
		t.Fatal(err)
	}
	engineLine := `{"timestamp":"2026-09-01T20:20:17Z","kind":"embed","operation":"ingest","backend":"metal","fallback_reason":"bulk build default"}` + "\n"
	if err := os.WriteFile(filepath.Join(dataDir, "logs", "engine-2026-09-01.jsonl"), []byte(engineLine), 0o600); err != nil {
		t.Fatal(err)
	}

	corpus := vectorDatabase{
		Plugin: "roca-corpus", Database: "corpus", Path: "roca-corpus.db", Alias: "corpus",
		Tables: []vectorTable{{Name: "notes", IDColumn: "id", TextColumns: []string{"body"}}},
	}
	ops := vectorDatabase{
		Plugin: "roca-ops", Database: "ops", Path: "roca-ops.db", Alias: "ops",
		Tables: []vectorTable{{Name: "memories", IDColumn: "id", TextColumns: []string{"content"}}},
	}
	galactic := vectorDatabase{
		Plugin: "roca-galactic", Database: "galactic", Path: "roca-galactic.db", Alias: "galactic",
		Tables: []vectorTable{{Name: "messages", IDColumn: "id", TextColumns: []string{"body"}}},
	}
	notesPlugin := vectorDatabase{
		Plugin: "roca-notes", Database: "notes", Path: "notes.db", Alias: "notes",
		Tables: []vectorTable{{Name: "task_state_versions", IDColumn: "id", TextColumns: []string{"body"}}},
	}
	parser := vectorDatabase{
		Plugin: "claude-code-parser", Database: "corpus", Path: "claude-code-corpus.db", Alias: "parser",
		Tables: []vectorTable{
			{Name: "sessions", IDColumn: "session_id", TextColumns: []string{"title"}},
			{Name: "exchanges", IDColumn: "id", TextColumns: []string{"human_text"}},
		},
	}
	writeRegistry(t, root, vectorRegistry{Schema: 2, Databases: []vectorDatabase{parser, corpus, notesPlugin, galactic, ops}})

	writeSourceRows(t, filepath.Join(root, corpus.Plugin, corpus.Path),
		`CREATE TABLE notes(id TEXT PRIMARY KEY, body TEXT);
		 INSERT INTO notes VALUES ('a','alpha'),('b','bravo'),('c','charlie');`)
	writeSidecarWithChunks(t, SidecarPath(filepath.Join(root, corpus.Plugin, corpus.Path)),
		corpus.owner(), 7, map[string]string{"contract": corpus.contractFingerprint()})
	writeSidecarWithChunks(t, SidecarPath(filepath.Join(root, ops.Plugin, ops.Path)),
		ops.owner(), 4, map[string]string{
			"contract": ops.contractFingerprint(), "source_fingerprint": "sealed-ops",
		})
	writeSidecarWithChunks(t, SidecarPath(filepath.Join(root, notesPlugin.Plugin, notesPlugin.Path)),
		notesPlugin.owner(), 2, map[string]string{"contract": "stale-contract"})
	if err := os.MkdirAll(filepath.Join(root, galactic.Plugin), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(SidecarPath(filepath.Join(root, galactic.Plugin, galactic.Path)), []byte("not sqlite"), 0o600); err != nil {
		t.Fatal(err)
	}

	started := time.Now()
	report, err := ReportVectorization(context.Background(), StatusRequest{
		PluginRoot: root, StateDir: state, DataDir: dataDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(started) > 2*time.Second {
		t.Fatalf("status blocked for %s", time.Since(started))
	}

	if !report.Worker.Running {
		t.Fatal("worker is this test process and should report running")
	}
	if report.Worker.PID == nil || *report.Worker.PID != os.Getpid() {
		t.Fatalf("worker pid = %v, want %d", report.Worker.PID, os.Getpid())
	}
	if report.Worker.Backend == nil || *report.Worker.Backend != "metal" {
		t.Fatalf("worker backend = %v, want metal", report.Worker.Backend)
	}
	if report.Worker.Database != nil {
		t.Fatalf("worker database was deduced as %q; it was not recorded", *report.Worker.Database)
	}

	if len(report.Databases) != 5 {
		t.Fatalf("databases = %d, want 1 row per registry entry: %+v", len(report.Databases), report.Databases)
	}
	got := map[string]DatabaseVectorization{}
	for _, row := range report.Databases {
		got[row.Plugin+"/"+row.Database] = row
	}

	corpusRow := got["roca-corpus/corpus"]
	if corpusRow.EmbeddedChunks == nil || *corpusRow.EmbeddedChunks != 7 {
		t.Fatalf("corpus embedded = %v, want 7", corpusRow.EmbeddedChunks)
	}
	if corpusRow.CandidateChunks == nil || *corpusRow.CandidateChunks != 3 {
		t.Fatalf("corpus candidates = %v, want 3", corpusRow.CandidateChunks)
	}
	if corpusRow.SidecarBytes == nil || *corpusRow.SidecarBytes <= 0 {
		t.Fatalf("corpus sidecar bytes = %v", corpusRow.SidecarBytes)
	}
	if corpusRow.LastWrite == nil || *corpusRow.LastWrite == "" {
		t.Fatalf("corpus last write = %v", corpusRow.LastWrite)
	}
	if corpusRow.State != StateBuilding {
		t.Fatalf("unsealed corpus with a live worker = %q, want building", corpusRow.State)
	}
	if len(corpusRow.Tables) != 1 || corpusRow.Tables[0] != "notes" {
		t.Fatalf("corpus tables = %q", corpusRow.Tables)
	}

	opsRow := got["roca-ops/ops"]
	if opsRow.EmbeddedChunks == nil || *opsRow.EmbeddedChunks != 4 {
		t.Fatalf("ops embedded = %v, want 4", opsRow.EmbeddedChunks)
	}
	if opsRow.CandidateChunks != nil {
		t.Fatalf("ops candidates = %v, want unknown because the source database is absent", opsRow.CandidateChunks)
	}
	if opsRow.State != StateComplete {
		t.Fatalf("sealed ops = %q, want complete", opsRow.State)
	}

	parserRow := got["claude-code-parser/corpus"]
	if parserRow.EmbeddedChunks != nil {
		t.Fatalf("missing sidecar embedded = %v, want unknown not 0", parserRow.EmbeddedChunks)
	}
	if parserRow.CandidateChunks != nil {
		t.Fatalf("missing sidecar candidates = %v, want unknown not 0", parserRow.CandidateChunks)
	}
	if parserRow.SidecarBytes != nil {
		t.Fatalf("missing sidecar bytes = %v, want unknown not 0", parserRow.SidecarBytes)
	}
	if parserRow.State != StateEmpty {
		t.Fatalf("missing sidecar state = %q, want empty", parserRow.State)
	}

	notesRow := got["roca-notes/notes"]
	if notesRow.State != StateOutdated {
		t.Fatalf("stale contract = %q, want outdated", notesRow.State)
	}
	if notesRow.EmbeddedChunks == nil || *notesRow.EmbeddedChunks != 2 {
		t.Fatalf("outdated sidecar still has readable chunks = %v", notesRow.EmbeddedChunks)
	}

	galacticRow := got["roca-galactic/galactic"]
	if galacticRow.State != StateUnknown {
		t.Fatalf("unreadable sidecar state = %q, want unknown", galacticRow.State)
	}
	if galacticRow.EmbeddedChunks != nil {
		t.Fatalf("unreadable sidecar embedded = %v, want unknown not 0", galacticRow.EmbeddedChunks)
	}

	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	databases, _ := decoded["databases"].([]any)
	for _, item := range databases {
		row, _ := item.(map[string]any)
		if row["plugin"] == "claude-code-parser" {
			if row["embedded_chunks"] != nil {
				t.Fatalf("JSON encoded a missing count as %v, want null", row["embedded_chunks"])
			}
		}
	}
}

func TestReportVectorizationUnknownNeverBecomesZeroOnAHungSourceCount(t *testing.T) {
	root := t.TempDir()
	corpus := vectorDatabase{
		Plugin: "roca-corpus", Database: "corpus", Path: "roca-corpus.db", Alias: "corpus",
		Tables: []vectorTable{{Name: "notes", IDColumn: "id", TextColumns: []string{"body"}}},
	}
	writeRegistry(t, root, vectorRegistry{Schema: 2, Databases: []vectorDatabase{corpus}})
	writeSidecarWithChunks(t, SidecarPath(filepath.Join(root, corpus.Plugin, corpus.Path)),
		corpus.owner(), 5, nil)

	previous := candidateCountTimeout
	candidateCountTimeout = 30 * time.Millisecond
	t.Cleanup(func() { candidateCountTimeout = previous })
	countDeclaredSources = func(context.Context, vectorDatabase, string) (*int64, error) {
		time.Sleep(200 * time.Millisecond)
		zero := int64(0)
		return &zero, nil
	}
	t.Cleanup(func() { countDeclaredSources = readDeclaredSourceCount })

	started := time.Now()
	report, err := ReportVectorization(context.Background(), StatusRequest{PluginRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(started) > time.Second {
		t.Fatalf("status waited for a hung source count: %s", time.Since(started))
	}
	if report.Databases[0].CandidateChunks != nil {
		t.Fatalf("timed-out candidate count = %v, want unknown not 0", report.Databases[0].CandidateChunks)
	}
	if report.Databases[0].EmbeddedChunks == nil || *report.Databases[0].EmbeddedChunks != 5 {
		t.Fatalf("embedded chunks were lost when candidates timed out: %v", report.Databases[0].EmbeddedChunks)
	}
}

func writeSidecarWithChunks(t *testing.T, path, owner string, n int, extraMeta map[string]string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := InitOwnedSidecar(path, owner, DefaultModel); err != nil {
		t.Fatal(err)
	}
	db := openTestSQLite(t, path)
	defer db.Close()
	if _, err := db.Exec(`INSERT OR REPLACE INTO meta(key,value) VALUES('dimensions','8')`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		if _, err := db.Exec(`INSERT INTO chunks(source_kind,source_id,text_column,chunk_index,fingerprint,locator)
			VALUES('notes',?,?,0,'fp','loc')`, fmt.Sprintf("id-%d", i), fmt.Sprintf("id-%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	for key, value := range extraMeta {
		if _, err := db.Exec(`INSERT OR REPLACE INTO meta(key,value) VALUES(?,?)`, key, value); err != nil {
			t.Fatal(err)
		}
	}
}

func writeSourceRows(t *testing.T, path, schema string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	createSourceDatabase(t, path, schema)
}
