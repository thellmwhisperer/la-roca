package vector

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDeclaredWalkEmitsPerColumnChunkIdentity(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "roca-corpus.db")
	createSourceDatabase(t, dbPath, `
		CREATE TABLE sessions(session_id TEXT PRIMARY KEY, title TEXT, project TEXT, started_at TEXT);
		CREATE TABLE exchanges(
			id INTEGER PRIMARY KEY, session_id TEXT, exchange_number INTEGER,
			human_text TEXT, agent_text TEXT, human_timestamp TEXT, agent_timestamp TEXT);
		INSERT INTO sessions VALUES ('lab-session','Night rest','health','2026-03-18');
		INSERT INTO exchanges VALUES (7,'lab-session',1,'short human worry',
			'`+strings.Repeat("long agent answer word ", 40)+`','2026-03-18T04:12:00Z','2026-03-18T04:20:00Z');`)
	writeRegistry(t, root, vectorRegistry{Schema: 1, Databases: []vectorDatabase{{
		Plugin: "roca-corpus", Database: "corpus", Path: "roca-corpus.db", Alias: "plugin_roca_corpus",
		Tables: []vectorTable{
			{Name: "sessions", IDColumn: "session_id", TextColumns: []string{"title", "project"}},
			{Name: "exchanges", IDColumn: "id", TextColumns: []string{"human_text", "agent_text"}},
		},
	}}})
	runner := sqliteExecRunner(t, map[string]string{"plugin_roca_corpus": dbPath})
	federation, err := LoadFederation(CoreCLI{Executable: "roca", Run: runner}, root,
		DefaultModel, "v-test", &recordingEmbedder{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var rows []sourceRow
	declared := DeclaredCorpus{Core: federation.Core, Database: federation.databases[0]}
	if err := declared.WalkSources(context.Background(), "exchanges", func(row sourceRow) error {
		rows = append(rows, row)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("column rows = %d, want 2: %+v", len(rows), rows)
	}
	seen := map[string]string{}
	sourceID := rows[0].stableID()
	for _, row := range rows {
		if row.stableID() != sourceID {
			t.Fatalf("column rows split the source identity: %q vs %q", sourceID, row.stableID())
		}
		if row.column == "" || row.text == "" {
			t.Fatalf("missing column identity: %+v", row)
		}
		seen[row.column] = row.text
	}
	if seen["human_text"] != "short human worry" || !strings.Contains(seen["agent_text"], "long agent answer") {
		t.Fatalf("column texts = %v", seen)
	}
	if rows[0].identity() != rows[1].identity() {
		t.Fatal("per-column rows must share row identity so query dedupe collapses them")
	}
}

func TestEmbeddingInputPrependsHeaderWithoutStoringIt(t *testing.T) {
	embedder := &recordingEmbedder{}
	human := sourceRow{kind: "exchanges", sourceID: "7", column: "human_text",
		text: "short human worry", rowText: "short human worry\n\nlong agent answer",
		title: "Night rest", occurredAt: "2026-03-18T04:12:00Z"}
	index := Index{Corpus: &memoryCorpus{sources: []sourceRow{human}},
		VectorPath: filepath.Join(t.TempDir(), "vector.db"), Model: DefaultModel, Embedder: embedder}
	if _, err := index.Ingest(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(embedder.inputs) == 0 || len(embedder.inputs[0]) == 0 {
		t.Fatal("no embedding inputs")
	}
	got := embedder.inputs[0][0]
	wantPrefix := DocumentPrefix + "[Night rest · 2026-03] short human worry"
	if got != wantPrefix {
		t.Fatalf("embedding input = %q, want %q", got, wantPrefix)
	}
	results, err := index.Query(context.Background(), "short human worry", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || strings.Contains(results[0].Text, "[Night rest") {
		t.Fatalf("resolved text leaked the header: %+v", results)
	}
}

func TestReembedIsIdempotentAcrossInterruptAndRerun(t *testing.T) {
	sources := []sourceRow{
		{kind: "exchanges", sourceID: "1", column: "human_text", text: "alpha question", rowText: "alpha question\n\nbeta answer"},
		{kind: "exchanges", sourceID: "1", column: "agent_text", text: "beta answer", rowText: "alpha question\n\nbeta answer"},
		{kind: "exchanges", sourceID: "2", column: "human_text", text: "gamma question", rowText: "gamma question\n\ndelta answer"},
	}
	embedder := &failAfterEmbedder{limit: 1}
	vectorPath := filepath.Join(t.TempDir(), "vector.db")
	index := Index{Corpus: &memoryCorpus{sources: sources}, VectorPath: vectorPath,
		Model: DefaultModel, Embedder: embedder, Reembed: true, BatchSize: 1}
	if _, err := index.Ingest(context.Background()); err == nil {
		t.Fatal("interrupted ingest succeeded")
	}
	first := chunkCount(t, vectorPath)
	if first == 0 || first >= len(sources) {
		t.Fatalf("interrupted chunk count = %d", first)
	}
	embedder.limit = 100
	second, err := index.Ingest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if chunkCount(t, vectorPath) != len(sources) {
		t.Fatalf("resumed chunk count = %d, want %d (delta %+v)", chunkCount(t, vectorPath), len(sources), second)
	}
	embedder.calls = 0
	again, err := index.Ingest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if again.Added != 0 || again.Updated != 0 || again.Removed != 0 || again.Unchanged != len(sources) || embedder.calls != 0 {
		t.Fatalf("repeated reembed = %+v calls=%d", again, embedder.calls)
	}
}

func TestDeclaredPagesNewestFirst(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "memories.db")
	createSourceDatabase(t, dbPath, `CREATE TABLE memories(
		id TEXT PRIMARY KEY, content TEXT, project TEXT, created_at TEXT);`)
	db := openTestSQLite(t, dbPath)
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	want := make([]string, 0, walkPageSize+3)
	for i := 0; i < walkPageSize+3; i++ {
		id := fmt.Sprintf("z%03d", walkPageSize+2-i)
		if i == 0 {
			id = "a-newest"
		}
		if _, err := tx.Exec(`INSERT INTO memories VALUES (?,?,?,?)`, id,
			fmt.Sprintf("memory %d", i), "project", base.Add(-time.Duration(i)*time.Minute).Format(time.RFC3339)); err != nil {
			tx.Rollback()
			db.Close()
			t.Fatal(err)
		}
		want = append(want, id)
	}
	if err := tx.Commit(); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	corpus := DeclaredCorpus{
		Core: CoreCLI{Executable: "roca", Run: sqliteExecRunner(t, map[string]string{
			"plugin_roca_ops": dbPath,
		})},
		Database: vectorDatabase{Plugin: "roca-ops", Database: "ops", Alias: "plugin_roca_ops",
			Tables: []vectorTable{{Name: "memories", IDColumn: "id", TextColumns: []string{"content"}}}},
	}
	got := make([]string, 0, len(want))
	if err := corpus.WalkSources(context.Background(), "memories", func(row sourceRow) error {
		got = append(got, row.sourceID)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("walked %d rows, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("row %d = %q, want newest-first %q", i, got[i], want[i])
		}
	}
}

func TestIngestReportsProgressWithETA(t *testing.T) {
	sources := []sourceRow{
		{kind: "memories", sourceID: "1", text: "alpha recent", createdAt: "2026-08-01", occurredAt: "2026-08-01"},
		{kind: "memories", sourceID: "2", text: "beta later", createdAt: "2026-07-01", occurredAt: "2026-07-01"},
	}
	var updates []IngestProgress
	index := Index{Corpus: &countingCorpus{memoryCorpus: memoryCorpus{sources: sources}, total: 2},
		VectorPath: filepath.Join(t.TempDir(), "vector.db"), Model: DefaultModel,
		Embedder: &recordingEmbedder{}, Progress: func(update IngestProgress) {
			updates = append(updates, update)
		}, BatchSize: 1}
	if _, err := index.Ingest(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(updates) == 0 {
		t.Fatal("no progress updates")
	}
	last := updates[len(updates)-1]
	if last.Sources != 2 || last.Total != 2 || last.Chunks != 2 {
		t.Fatalf("progress = %+v", last)
	}
}

type failAfterEmbedder struct {
	calls int
	limit int
}

func (e *failAfterEmbedder) Pull(context.Context, string) error { return nil }

func (e *failAfterEmbedder) Embed(_ context.Context, _ string, input []string) ([][]float32, error) {
	e.calls++
	if e.calls > e.limit {
		return nil, fmt.Errorf("synthetic interrupt")
	}
	vectors := make([][]float32, len(input))
	for i := range input {
		vectors[i] = []float32{1, 0, 0, 0, 0, 0, 0, 0}
	}
	return vectors, nil
}

type countingCorpus struct {
	memoryCorpus
	total int
}

func (c countingCorpus) CountSources(context.Context, string) (int, error) { return c.total, nil }

func chunkCount(t *testing.T, path string) int {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM chunks`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestLocatorRecordsTextColumn(t *testing.T) {
	row := sourceRow{kind: "exchanges", sourceID: "7", column: "human_text", text: "hello",
		rowText: "hello\n\nworld"}
	raw, err := json.Marshal(row.locator())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"text_column":"human_text"`) {
		t.Fatalf("locator JSON = %s", raw)
	}
}
