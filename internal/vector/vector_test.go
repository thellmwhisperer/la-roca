package vector

import (
	"context"
	"database/sql"
	"math"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

type recordingEmbedder struct {
	inputs [][]string
}

func (e *recordingEmbedder) Pull(context.Context, string) error { return nil }

func (e *recordingEmbedder) Embed(_ context.Context, _ string, input []string) ([][]float32, error) {
	e.inputs = append(e.inputs, append([]string(nil), input...))
	vectors := make([][]float32, len(input))
	for i, text := range input {
		text = strings.ToLower(text)
		switch {
		case strings.Contains(text, "alpha"):
			vectors[i] = []float32{1, 0, 0, 0, 0, 0, 0, 0}
		case strings.Contains(text, "beta"):
			vectors[i] = []float32{0, 1, 0, 0, 0, 0, 0, 0}
		default:
			vectors[i] = []float32{0, 0, 1, 0, 0, 0, 0, 0}
		}
	}
	return vectors, nil
}

func TestChunksOverlapWithoutRepeatingTerminalChunk(t *testing.T) {
	got := chunks("abcdefghij", 6, 2)
	want := []string{"abcdef", "efghij"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("chunks = %q, want %q", got, want)
	}
	unicode := chunks("a🙂bc", 3, 1)
	if strings.Join(unicode, "|") != "a🙂b|bc" {
		t.Fatalf("unicode chunks = %q", unicode)
	}
}

func TestStableSourceIDsUseCoreNaturalKeys(t *testing.T) {
	memory := sourceRow{kind: "memories", sessionID: "session/a", ordinal: 3}
	if got, want := memory.stableID(), "memories/session/session%2Fa/3"; got != want {
		t.Fatalf("memory stable id = %q, want %q", got, want)
	}
	exchange := sourceRow{kind: "exchanges", sessionID: "session/a", ordinal: 7}
	if got, want := exchange.stableID(), "exchanges/session%2Fa/7"; got != want {
		t.Fatalf("exchange stable id = %q, want %q", got, want)
	}
	thinking := sourceRow{kind: "thinking_blocks", sessionID: "session/a", ordinal: 7, position: "1.5"}
	if got, want := thinking.stableID(), "thinking_blocks/session%2Fa/7/1.5"; got != want {
		t.Fatalf("thinking stable id = %q, want %q", got, want)
	}
	direct := sourceRow{kind: "memories", text: "stored memory", layer: "discovery", origin: "human", createdAt: "2026-08-14"}
	if got := direct.stableID(); !strings.HasPrefix(got, "memories/direct/") || strings.Contains(got, "/id/") {
		t.Fatalf("direct memory stable id = %q", got)
	}
}

func TestDeltaIndexIsIdempotentAndMapsResultsBackToCore(t *testing.T) {
	ctx := context.Background()
	corePath := createCoreFixture(t)
	vectorPath := filepath.Join(t.TempDir(), "vector.db")
	embedder := &recordingEmbedder{}
	index := Index{CorePath: corePath, VectorPath: vectorPath, Model: DefaultModel, Embedder: embedder}

	first, err := index.Ingest(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if first.Added != 4 || first.Updated != 0 || first.Removed != 0 {
		t.Fatalf("first delta = %+v", first)
	}
	second, err := index.Ingest(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if second.Added != 0 || second.Updated != 0 || second.Removed != 0 || second.Unchanged != 4 {
		t.Fatalf("idempotent delta = %+v", second)
	}

	results, err := index.Query(ctx, "alpha topic", 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 {
		t.Fatalf("results = %d, want 3", len(results))
	}
	if results[0].Source != "memories" || results[0].Text != "alpha memory" {
		t.Fatalf("first result = %+v", results[0])
	}
	if math.Abs(results[0].Score-1) > 0.0001 {
		t.Fatalf("first score = %f, want 1", results[0].Score)
	}
	if !strings.HasPrefix(embedder.inputs[len(embedder.inputs)-1][0], QueryPrefix) {
		t.Fatalf("query was not embedded with %q: %q", QueryPrefix, embedder.inputs[len(embedder.inputs)-1][0])
	}

	core, err := sql.Open("sqlite", corePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := core.Exec("UPDATE exchanges SET agent_text = 'alpha changed' WHERE exchange_number = 2"); err != nil {
		t.Fatal(err)
	}
	if _, err := core.Exec("DELETE FROM thinking_blocks"); err != nil {
		t.Fatal(err)
	}
	if err := core.Close(); err != nil {
		t.Fatal(err)
	}

	changed, err := index.Ingest(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if changed.Updated != 1 || changed.Removed != 1 || changed.Added != 0 {
		t.Fatalf("changed delta = %+v", changed)
	}
	assertVectorStoreHasNoCorpusText(t, vectorPath)
}

func TestQueryDeduplicatesChunksByStableSource(t *testing.T) {
	corePath := createCoreFixture(t)
	core, err := sql.Open("sqlite", corePath)
	if err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("alpha ", defaultChunkSize/3)
	if _, err := core.Exec("UPDATE memories SET content = ? WHERE id = 1", long); err != nil {
		t.Fatal(err)
	}
	if err := core.Close(); err != nil {
		t.Fatal(err)
	}
	index := Index{
		CorePath: corePath, VectorPath: filepath.Join(t.TempDir(), "vector.db"),
		Model: DefaultModel, Embedder: &recordingEmbedder{},
	}
	if _, err := index.Ingest(context.Background()); err != nil {
		t.Fatal(err)
	}
	results, err := index.Query(context.Background(), "alpha", 20)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, result := range results {
		if seen[result.SourceID] {
			t.Fatalf("source %q returned more than once", result.SourceID)
		}
		seen[result.SourceID] = true
	}
}

func createCoreFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "core.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ddl := `
		CREATE TABLE sessions(session_id TEXT PRIMARY KEY, source_agent TEXT, project TEXT, started_at TEXT, ended_at TEXT, duration_minutes INTEGER, title TEXT, metadata TEXT);
		CREATE TABLE memories(id INTEGER PRIMARY KEY, layer TEXT, content TEXT, metadata TEXT, origin TEXT, source_agent TEXT, source_model TEXT, source_surface TEXT, source_session TEXT, source_sequence INTEGER, project TEXT, status TEXT, supersedes INTEGER, created_at TEXT);
		CREATE TABLE exchanges(id INTEGER PRIMARY KEY, session_id TEXT, exchange_number INTEGER, human_text TEXT, agent_text TEXT);
		CREATE TABLE thinking_blocks(id INTEGER PRIMARY KEY, session_id TEXT, exchange_number INTEGER, position_in_session REAL, full_text TEXT);
		INSERT INTO sessions VALUES ('s1', 'synthetic-agent', 'synthetic-project', '2026-01-01', '2026-01-01', 1, 'gamma session', '{}');
		INSERT INTO memories VALUES (1, 'discovery', 'alpha memory', '{}', 'agent', 'synthetic-agent', NULL, NULL, NULL, NULL, 'synthetic-project', 'active', NULL, '2026-01-01');
		INSERT INTO exchanges VALUES (1, 's1', 2, 'beta question', 'gamma answer');
		INSERT INTO thinking_blocks VALUES (1, 's1', 2, 1.5, 'beta reasoning');
	`
	if _, err := db.Exec(ddl); err != nil {
		t.Fatal(err)
	}
	return path
}

func assertVectorStoreHasNoCorpusText(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query("PRAGMA table_info(chunks)")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notnull, pk int
		var name, kind string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &kind, &notnull, &defaultValue, &pk); err != nil {
			t.Fatal(err)
		}
		if name == "text" || name == "content" {
			t.Fatalf("vector store duplicates corpus in column %q", name)
		}
	}
}
