package vector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReportVectorizationReadsSidecarFactsAndNeverInventZero(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "state")
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	holdTestWorkerClaim(t, state, "status-facts")
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
	opsPath := filepath.Join(root, ops.Plugin, ops.Path)
	writeSourceRows(t, opsPath,
		`CREATE TABLE memories(id TEXT PRIMARY KEY, content TEXT);
		 INSERT INTO memories VALUES ('a','alpha');`)
	writeSidecarWithChunks(t, SidecarPath(filepath.Join(root, corpus.Plugin, corpus.Path)),
		corpus.owner(), 7, map[string]string{"contract": corpus.contractFingerprint()})
	opsMarker, err := sourceFileMarker(opsPath)
	if err != nil {
		t.Fatal(err)
	}
	writeSidecarWithChunks(t, SidecarPath(opsPath), ops.owner(), 4, map[string]string{
		"contract": ops.contractFingerprint(), "source_fingerprint": "sealed-ops",
		sourceMarkerMetaKey: opsMarker,
	})
	activeDatabase := corpus.owner()
	if err := updateWorkerActivity(state, "metal", &activeDatabase); err != nil {
		t.Fatal(err)
	}
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
		PluginRoot: root, StateDir: state,
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
	if report.Worker.Database == nil || *report.Worker.Database != corpus.owner() {
		t.Fatalf("worker database = %v, want %s", report.Worker.Database, corpus.owner())
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
	if opsRow.CandidateChunks == nil || *opsRow.CandidateChunks != 1 {
		t.Fatalf("ops candidates = %v, want 1", opsRow.CandidateChunks)
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
	countDeclaredChunks = func(context.Context, vectorDatabase, string) (candidateChunkSnapshot, error) {
		time.Sleep(200 * time.Millisecond)
		zero := int64(0)
		return candidateChunkSnapshot{Chunks: &zero, SourceMarker: "unreadable"}, nil
	}
	t.Cleanup(func() { countDeclaredChunks = readDeclaredChunkCount })

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

func TestEmbeddingSchedulerRecordsTheDatabaseItIsEmbedding(t *testing.T) {
	state := t.TempDir()
	holdTestWorkerClaim(t, state, "scheduler")
	base := &delayedEmbedder{delay: 100 * time.Millisecond}
	scheduler := newEmbeddingScheduler(context.Background(), base, 1)
	runDone := make(chan error, 1)
	go func() { runDone <- scheduler.run() }()
	embedDone := make(chan error, 1)
	go func() {
		ctx := context.WithValue(context.Background(), sourceOrderKey{}, sourceOrder{id: "a"})
		_, err := (scheduledEmbedder{base: base, id: 0, scheduler: scheduler,
			database: "roca-corpus/corpus", stateDir: state}).Embed(ctx, DefaultModel, []string{"alpha"})
		embedDone <- err
	}()
	deadline := time.Now().Add(time.Second)
	for {
		status := readWorkerStatus(state)
		if status.Database != nil && *status.Database == "roca-corpus/corpus" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("worker database was not recorded: %+v", status)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := <-embedDone; err != nil {
		t.Fatal(err)
	}
	if status := readWorkerStatus(state); status.Database != nil {
		t.Fatalf("finished embedding still reports database %q", *status.Database)
	}
	scheduler.finished <- 0
	if err := <-runDone; err != nil {
		t.Fatal(err)
	}
}

func TestEmbeddingSchedulerStopsWhenDatabaseClearFails(t *testing.T) {
	state := t.TempDir()
	holdTestWorkerClaim(t, state, "clear-failure")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	scheduler := newEmbeddingScheduler(ctx, activityClearFailEmbedder{state: state}, 1)
	runDone := make(chan error, 1)
	go func() { runDone <- scheduler.run() }()
	embedCtx := context.WithValue(ctx, sourceOrderKey{}, sourceOrder{id: "a"})
	_, embedErr := (scheduledEmbedder{base: activityClearFailEmbedder{state: state}, id: 0,
		scheduler: scheduler, database: "roca-corpus/corpus", stateDir: state}).Embed(
		embedCtx, DefaultModel, []string{"alpha"})
	if embedErr == nil {
		cancel()
		<-runDone
		t.Fatal("scheduler continued after failing to clear the current database")
	}
	if !strings.Contains(embedErr.Error(), "clear current vector database") {
		t.Fatalf("embed error = %v", embedErr)
	}
	if runErr := <-runDone; runErr == nil || !strings.Contains(runErr.Error(), "clear current vector database") {
		t.Fatalf("scheduler error = %v", runErr)
	}
}

func TestReportVectorizationCountsDeclaredChunksExactly(t *testing.T) {
	root := t.TempDir()
	maxChars, overlap := 4, 1
	database := vectorDatabase{
		Plugin: "roca-corpus", Database: "corpus", Path: "roca-corpus.db", Alias: "corpus",
		Tables: []vectorTable{
			{Name: "notes", IDColumn: "id", TextColumns: []string{"body", "answer"},
				Chunking: &chunkingHints{MaxChars: &maxChars, OverlapChars: &overlap}},
			{Name: "entries", IDColumn: "id", TextColumns: []string{"body"}},
		},
	}
	writeRegistry(t, root, vectorRegistry{Schema: 2, Databases: []vectorDatabase{database}})
	path := filepath.Join(root, database.Plugin, database.Path)
	writeSourceRows(t, path, `CREATE TABLE notes(id TEXT PRIMARY KEY, body TEXT, answer TEXT);
		CREATE TABLE entries(id TEXT PRIMARY KEY, body TEXT);`)
	store := openTestSQLite(t, path)
	if _, err := store.Exec(`INSERT INTO notes VALUES ('a','abcdef','xy')`); err != nil {
		store.Close()
		t.Fatal(err)
	}
	longText := strings.TrimSpace(strings.Repeat("token ", defaultChunkTokens+1))
	if _, err := store.Exec(`INSERT INTO entries VALUES ('a',?)`, longText); err != nil {
		store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	report, err := ReportVectorization(context.Background(), StatusRequest{PluginRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	candidate := report.Databases[0].CandidateChunks
	if candidate == nil || *candidate != 5 {
		t.Fatalf("candidate chunks = %v, want 5", candidate)
	}
}

func TestReportVectorizationHidesCandidateCountFromOlderSourceSnapshot(t *testing.T) {
	root := t.TempDir()
	database := vectorDatabase{
		Plugin: "roca-corpus", Database: "corpus", Path: "roca-corpus.db", Alias: "corpus",
		Tables: []vectorTable{{Name: "notes", IDColumn: "id", TextColumns: []string{"body"}}},
	}
	writeRegistry(t, root, vectorRegistry{Schema: 2, Databases: []vectorDatabase{database}})
	path := filepath.Join(root, database.Plugin, database.Path)
	writeSourceRows(t, path, `CREATE TABLE notes(id TEXT PRIMARY KEY, body TEXT);
		INSERT INTO notes VALUES ('a','alpha');`)
	marker, err := sourceFileMarker(path)
	if err != nil {
		t.Fatal(err)
	}
	initialMarker := marker
	writeSidecarWithChunks(t, SidecarPath(path), database.owner(), 1, map[string]string{
		"contract": database.contractFingerprint(), "source_fingerprint": "sealed",
		sourceMarkerMetaKey: marker,
	})

	type countResult struct {
		snapshot candidateChunkSnapshot
		err      error
	}
	counted := make(chan countResult, 1)
	release := make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()
	countDeclaredChunks = func(ctx context.Context, database vectorDatabase, path string) (candidateChunkSnapshot, error) {
		snapshot, err := readDeclaredChunkCount(ctx, database, path)
		counted <- countResult{snapshot: snapshot, err: err}
		<-release
		return snapshot, err
	}
	t.Cleanup(func() { countDeclaredChunks = readDeclaredChunkCount })
	type reportResult struct {
		report Vectorization
		err    error
	}
	reported := make(chan reportResult, 1)
	go func() {
		report, err := ReportVectorization(context.Background(), StatusRequest{PluginRoot: root})
		reported <- reportResult{report: report, err: err}
	}()
	count := <-counted
	if count.err != nil {
		t.Fatal(count.err)
	}
	if count.snapshot.Chunks == nil || *count.snapshot.Chunks != 1 {
		t.Fatalf("candidate snapshot = %+v, want 1", count.snapshot)
	}
	source := openTestSQLite(t, path)
	if _, err := source.Exec(`INSERT INTO notes VALUES ('b','bravo')`); err != nil {
		source.Close()
		t.Fatal(err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	stamp := info.ModTime().Add(time.Second)
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	marker, err = sourceFileMarker(path)
	if err != nil {
		t.Fatal(err)
	}
	if marker == initialMarker {
		t.Fatal("source marker did not change after the source commit")
	}
	sidecar := openTestSQLite(t, SidecarPath(path))
	tx, err := sidecar.Begin()
	if err != nil {
		sidecar.Close()
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO chunks(source_kind,source_id,text_column,chunk_index,fingerprint,locator)
		VALUES('notes','id-1','body',0,'fp','loc-1')`); err != nil {
		tx.Rollback()
		sidecar.Close()
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT OR REPLACE INTO meta(key,value) VALUES(?,?)`, sourceMarkerMetaKey, marker); err != nil {
		tx.Rollback()
		sidecar.Close()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		sidecar.Close()
		t.Fatal(err)
	}
	if err := sidecar.Close(); err != nil {
		t.Fatal(err)
	}
	close(release)
	released = true
	result := <-reported
	if result.err != nil {
		t.Fatal(result.err)
	}
	row := result.report.Databases[0]
	if row.State != StateComplete || row.EmbeddedChunks == nil || *row.EmbeddedChunks != 2 {
		t.Fatalf("updated sidecar snapshot = %+v, want complete with 2 chunks", row)
	}
	if row.CandidateChunks != nil {
		t.Fatalf("older candidate snapshot was reported: %+v", row)
	}
}

func TestReportVectorizationMarksChangedCompletedSourceOutdated(t *testing.T) {
	root := t.TempDir()
	database := vectorDatabase{
		Plugin: "roca-corpus", Database: "corpus", Path: "roca-corpus.db", Alias: "corpus",
		Tables: []vectorTable{{Name: "notes", IDColumn: "id", TextColumns: []string{"body"}}},
	}
	writeRegistry(t, root, vectorRegistry{Schema: 2, Databases: []vectorDatabase{database}})
	path := filepath.Join(root, database.Plugin, database.Path)
	writeSourceRows(t, path, `CREATE TABLE notes(id TEXT PRIMARY KEY, body TEXT);
		INSERT INTO notes VALUES ('a','alpha');`)
	marker, err := sourceFileMarker(path)
	if err != nil {
		t.Fatal(err)
	}
	writeSidecarWithChunks(t, SidecarPath(path), database.owner(), 1, map[string]string{
		"contract": database.contractFingerprint(), "source_fingerprint": "sealed",
		sourceMarkerMetaKey: marker,
	})

	report, err := ReportVectorization(context.Background(), StatusRequest{PluginRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	if report.Databases[0].State != StateComplete {
		t.Fatalf("unchanged state = %q, want complete", report.Databases[0].State)
	}
	store := openTestSQLite(t, path)
	if _, err := store.Exec(`INSERT INTO notes VALUES ('b','bravo')`); err != nil {
		store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	report, err = ReportVectorization(context.Background(), StatusRequest{PluginRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	if report.Databases[0].State != StateOutdated {
		t.Fatalf("changed state = %q, want outdated", report.Databases[0].State)
	}
}

func TestReadSidecarSnapshotReturnsUnknownInsteadOfHighestID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.db")
	writeSidecarWithChunks(t, path, "roca-corpus/corpus", 3, nil)
	store := openTestSQLite(t, path)
	if _, err := store.Exec(`DELETE FROM chunks WHERE id=2`); err != nil {
		store.Close()
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if snapshot, readable := readSidecarSnapshot(ctx, store); readable {
		store.Close()
		t.Fatalf("unreadable sidecar snapshot = %+v, want unknown", snapshot)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestReportVectorizationReturnsUnknownWhenSidecarSnapshotCannotBeRead(t *testing.T) {
	root := t.TempDir()
	database := vectorDatabase{
		Plugin: "roca-corpus", Database: "corpus", Path: "roca-corpus.db", Alias: "corpus",
		Tables: []vectorTable{{Name: "notes", IDColumn: "id", TextColumns: []string{"body"}}},
	}
	writeRegistry(t, root, vectorRegistry{Schema: 2, Databases: []vectorDatabase{database}})
	sidecar := SidecarPath(filepath.Join(root, database.Plugin, database.Path))
	writeSidecarWithChunks(t, sidecar, database.owner(), 2, nil)
	store := openTestSQLite(t, sidecar)
	if _, err := store.Exec(`DROP TABLE meta`); err != nil {
		store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	report, err := ReportVectorization(context.Background(), StatusRequest{PluginRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	row := report.Databases[0]
	if row.State != StateUnknown || row.EmbeddedChunks != nil {
		t.Fatalf("partial sidecar snapshot was reported: %+v", row)
	}
}

func TestReportVectorizationTreatsSidecarStatErrorsAsUnknown(t *testing.T) {
	root := t.TempDir()
	database := vectorDatabase{
		Plugin: "roca-corpus", Database: "corpus", Path: "roca-corpus.db", Alias: "corpus",
		Tables: []vectorTable{{Name: "notes", IDColumn: "id", TextColumns: []string{"body"}}},
	}
	writeRegistry(t, root, vectorRegistry{Schema: 2, Databases: []vectorDatabase{database}})
	sidecar := SidecarPath(filepath.Join(root, database.Plugin, database.Path))
	if err := os.MkdirAll(filepath.Dir(sidecar), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Base(sidecar), sidecar); err != nil {
		t.Skipf("cannot create a looping sidecar symlink: %v", err)
	}
	report, err := ReportVectorization(context.Background(), StatusRequest{PluginRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	if report.Databases[0].State != StateUnknown {
		t.Fatalf("unreadable sidecar state = %q, want unknown", report.Databases[0].State)
	}
}

func TestReportVectorizationRejectsFileFactsFromOlderSidecarGeneration(t *testing.T) {
	root := t.TempDir()
	database := vectorDatabase{
		Plugin: "roca-corpus", Database: "corpus", Path: "roca-corpus.db", Alias: "corpus",
		Tables: []vectorTable{{Name: "notes", IDColumn: "id", TextColumns: []string{"body"}}},
	}
	writeRegistry(t, root, vectorRegistry{Schema: 2, Databases: []vectorDatabase{database}})
	sidecar := SidecarPath(filepath.Join(root, database.Plugin, database.Path))
	writeSidecarWithChunks(t, sidecar, database.owner(), 2, nil)
	writer := openTestSQLite(t, sidecar)
	t.Cleanup(func() { _ = writer.Close() })

	previous := countDeclaredChunks
	countDeclaredChunks = func(context.Context, vectorDatabase, string) (candidateChunkSnapshot, error) {
		_, err := writer.Exec(`INSERT INTO chunks(source_kind,source_id,text_column,chunk_index,fingerprint,locator)
			VALUES('notes','id-new','body',0,'fp-new','loc-new')`)
		return candidateChunkSnapshot{}, err
	}
	t.Cleanup(func() { countDeclaredChunks = previous })

	report, err := ReportVectorization(context.Background(), StatusRequest{PluginRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	row := report.Databases[0]
	if row.EmbeddedChunks == nil || *row.EmbeddedChunks != 3 {
		t.Fatalf("embedded chunks = %v, want current snapshot count 3", row.EmbeddedChunks)
	}
	if row.SidecarBytes != nil || row.LastWrite != nil {
		t.Fatalf("older sidecar file facts were published: bytes=%v last_write=%v", row.SidecarBytes, row.LastWrite)
	}
}

func TestSidecarFileFactsRejectCheckpointGenerationMix(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.db")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+"-wal", []byte("pending"), 0o600); err != nil {
		t.Fatal(err)
	}
	previous := statVectorFile
	checkpointed := false
	statVectorFile = func(name string) (os.FileInfo, error) {
		if name == path+"-wal" && !checkpointed {
			checkpointed = true
			if err := os.WriteFile(path, []byte("checkpointed"), 0o600); err != nil {
				return nil, err
			}
			if err := os.Remove(path + "-wal"); err != nil {
				return nil, err
			}
		}
		return previous(name)
	}
	t.Cleanup(func() { statVectorFile = previous })

	facts, err := sidecarFileFacts(path)
	if !errors.Is(err, errSourceChanged) {
		t.Fatalf("checkpointed sidecar facts error = %v, want %v", err, errSourceChanged)
	}
	if facts.Bytes != nil || facts.LastWrite != nil {
		t.Fatalf("checkpointed sidecar facts = %v, %v, want unknown", facts.Bytes, facts.LastWrite)
	}
}

func TestSourceFileMarkerRejectsCheckpointGenerationMix(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.db")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+"-wal", []byte("pending"), 0o600); err != nil {
		t.Fatal(err)
	}
	previous := statVectorFile
	checkpointed := false
	statVectorFile = func(name string) (os.FileInfo, error) {
		if name == path+"-wal" && !checkpointed {
			checkpointed = true
			if err := os.WriteFile(path, []byte("checkpointed"), 0o600); err != nil {
				return nil, err
			}
			if err := os.Remove(path + "-wal"); err != nil {
				return nil, err
			}
		}
		return previous(name)
	}
	t.Cleanup(func() { statVectorFile = previous })

	if _, err := sourceFileMarker(path); !errors.Is(err, errSourceChanged) {
		t.Fatalf("checkpointed source marker error = %v, want %v", err, errSourceChanged)
	}
}

func TestStableDatabaseIdentityRejectsAChangedSourceSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.db")
	if err := os.WriteFile(path, []byte("before"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := fingerprintVectorSource
	fingerprintVectorSource = func(string, string) (string, error) {
		if err := os.WriteFile(path, []byte("after-state"), 0o600); err != nil {
			return "", err
		}
		return "fingerprint", nil
	}
	t.Cleanup(func() { fingerprintVectorSource = old })
	if _, _, err := stableDatabaseIdentity(path, "contract"); !errors.Is(err, errSourceChanged) {
		t.Fatalf("changed source identity error = %v, want %v", err, errSourceChanged)
	}
}

func TestWorkerStatusRejectsActivityFromAnotherRun(t *testing.T) {
	state := t.TempDir()
	holdTestWorkerClaim(t, state, "current-run")
	activity := workerActivity{PID: os.Getpid(), RunID: "previous-run", Backend: "metal",
		Database: "roca-corpus/corpus"}
	raw, err := json.Marshal(activity)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, workerActivityFile), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	status := readWorkerStatus(state)
	if !status.Running || status.Backend != nil || status.Database != nil {
		t.Fatalf("stale worker activity was reported: %+v", status)
	}
}

func holdTestWorkerClaim(t *testing.T, state, runID string) {
	t.Helper()
	path := filepath.Join(state, WorkerClaimFilename)
	writeTestWorkerClaim(t, path, runID)
	release, err := lockFile(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = release() })
}

type activityClearFailEmbedder struct {
	state string
}

func (activityClearFailEmbedder) Pull(context.Context, string) error { return nil }

func (e activityClearFailEmbedder) Embed(_ context.Context, _ string, input []string) ([][]float32, error) {
	if err := os.Mkdir(filepath.Join(e.state, ".worker-status.tmp"), 0o700); err != nil {
		return nil, err
	}
	return make([][]float32, len(input)), nil
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
