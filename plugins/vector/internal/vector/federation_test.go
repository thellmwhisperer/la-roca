/*
*
@overview Contract tests for declared vector surfaces. ~320 lines, no public symbols, proves sidecar ownership and generic delta behavior.

	READING GUIDE
	-------------
	1. Start at TestFederationBuildsOwnedSidecarsAndGarbageCollectsByDelta
	2. Read federationFixture for the synthetic registry and databases
	3. Read sqliteExecRunner for the public roca-exec boundary

	MAIN FLOW
	---------
	federationFixture -> Federation.Ingest -> inspect sidecars -> mutate sources -> repeat delta

	PUBLIC API
	----------
	None; this file is executable contract coverage.

	INTERNALS
	---------
	federationFixture, sqliteExecRunner, writeRegistry, createSourceDatabase, sidecarMeta

@exports
@deps testing; database/sql; JSON; internal vector package
*/
package vector

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// -- 1/4 CORE · Federated generation, metadata, incrementality, and GC <- START HERE --

func TestFederationBuildsOwnedSidecarsAndGarbageCollectsByDelta(t *testing.T) {
	federation, corpusPath, opsPath, embedder := federationFixture(t)
	ctx := context.Background()

	first, err := federation.Ingest(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if first.Added < 4 || first.Sources != 4 || len(first.Databases) != 2 {
		t.Fatalf("first federation delta = %+v", first)
	}
	corpusSidecar, opsSidecar := SidecarPath(corpusPath), SidecarPath(opsPath)
	for path, owner := range map[string]string{
		corpusSidecar: "roca-corpus/corpus",
		opsSidecar:    "roca-ops/ops",
	} {
		metadata := sidecarMeta(t, path)
		if metadata["owner"] != owner || metadata["model"] != DefaultModel ||
			metadata["dimensions"] != "8" || metadata["version"] != "v-test" ||
			metadata["contract"] == "" || metadata["source_fingerprint"] == "" {
			t.Fatalf("sidecar %s metadata = %+v", owner, metadata)
		}
	}
	allInputs := strings.Join(flattenInputs(embedder.inputs), "\n")
	if strings.Contains(allInputs, "raw-counter") || !strings.Contains(allInputs, "remembered body") ||
		!strings.Contains(allInputs, "Operational decision") {
		t.Fatalf("declared embedding inputs = %q", allInputs)
	}

	embeddingCalls := len(embedder.inputs)
	steady, err := federation.Ingest(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if steady.Added != 0 || steady.Updated != 0 || steady.Removed != 0 ||
		steady.Unchanged != first.Chunks || len(embedder.inputs) != embeddingCalls {
		t.Fatalf("unchanged federation delta = %+v, embedding calls %d -> %d",
			steady, embeddingCalls, len(embedder.inputs))
	}

	mutateSourceDatabase(t, corpusPath,
		`UPDATE articles SET body='A changed remembered body' WHERE id='article-1'`)
	mutateSourceDatabase(t, opsPath, `DELETE FROM memories WHERE id=2`)
	changed, err := federation.Ingest(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if changed.Updated == 0 || changed.Removed == 0 {
		t.Fatalf("changed federation delta = %+v", changed)
	}
	ops := openTestSQLite(t, opsSidecar)
	defer ops.Close()
	var deleted int
	if err := ops.QueryRow(`SELECT COUNT(*) FROM chunks WHERE source_kind='memories' AND source_id='memories/2'`).Scan(&deleted); err != nil {
		t.Fatal(err)
	}
	if deleted != 0 {
		t.Fatalf("deleted declared row retained %d chunks", deleted)
	}
}

func TestFederationTargetedDeltaPreservesOtherTablesAndCorpusQueryCompatibility(t *testing.T) {
	federation, corpusPath, _, embedder := federationFixture(t)
	if _, err := federation.Ingest(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	corpusSidecar := SidecarPath(corpusPath)
	store := openTestSQLite(t, corpusSidecar)
	if _, err := store.Exec(`INSERT INTO chunks(source_kind,source_id,chunk_index,fingerprint,locator)
		VALUES ('sentinel','sentinel/1',0,'sentinel','{}')`); err != nil {
		store.Close()
		t.Fatal(err)
	}
	store.Close()

	mutateSourceDatabase(t, corpusPath,
		`UPDATE articles SET title='A revised title' WHERE id='article-1'`)
	if delta, err := federation.Ingest(context.Background(), "articles"); err != nil || delta.Updated == 0 {
		t.Fatalf("targeted declared delta = %+v, err=%v", delta, err)
	}
	store = openTestSQLite(t, corpusSidecar)
	defer store.Close()
	var sentinel int
	if err := store.QueryRow(`SELECT COUNT(*) FROM chunks WHERE source_kind='sentinel'`).Scan(&sentinel); err != nil {
		t.Fatal(err)
	}
	if sentinel != 1 {
		t.Fatal("targeted declared delta removed an unrelated source kind")
	}

	index, err := federation.CorpusIndex()
	if err != nil {
		t.Fatal(err)
	}
	results, err := index.Query(context.Background(), "remembered", 5)
	if err != nil {
		t.Fatal(err)
	}
	foundRemembered := false
	for _, result := range results {
		foundRemembered = foundRemembered || strings.Contains(result.Text, "remembered body")
	}
	if len(results) == 0 || !foundRemembered {
		t.Fatalf("corpus compatibility query results = %+v", results)
	}
	if len(embedder.inputs) == 0 || !strings.HasPrefix(embedder.inputs[len(embedder.inputs)-1][0], QueryPrefix) {
		t.Fatalf("query embedding inputs = %q", embedder.inputs)
	}
}

// -/ 1/4

// -- 2/4 HELPER · Synthetic registry and source databases --

func federationFixture(t *testing.T) (Federation, string, string, *recordingEmbedder) {
	t.Helper()
	root := t.TempDir()
	corpusDir, opsDir := filepath.Join(root, "roca-corpus"), filepath.Join(root, "roca-ops")
	for _, directory := range []string{corpusDir, opsDir} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	corpusPath, opsPath := filepath.Join(corpusDir, "roca-corpus.db"), filepath.Join(opsDir, "roca-ops.db")
	createSourceDatabase(t, corpusPath, `
		CREATE TABLE articles(id TEXT PRIMARY KEY,title TEXT,body TEXT,telemetry TEXT);
		INSERT INTO articles VALUES ('article-1','Remembered title','A remembered body','raw-counter');
		INSERT INTO articles VALUES ('article-2','Second title','Second body','raw-counter');`)
	createSourceDatabase(t, opsPath, `
		CREATE TABLE memories(id INTEGER PRIMARY KEY,content TEXT,status TEXT);
		INSERT INTO memories VALUES (1,'Operational decision','active');
		INSERT INTO memories VALUES (2,'Temporary handoff','active');`)
	writeRegistry(t, root, vectorRegistry{Schema: 1, Databases: []vectorDatabase{
		{Plugin: "roca-corpus", Database: "corpus", Path: "roca-corpus.db", Alias: "plugin_roca_corpus",
			Tables: []vectorTable{{Name: "articles", IDColumn: "id", TextColumns: []string{"title", "body"},
				Chunking: &chunkingHints{MaxChars: intPointer(24), OverlapChars: intPointer(4)}}}},
		{Plugin: "roca-ops", Database: "ops", Path: "roca-ops.db", Alias: "plugin_roca_ops",
			Tables: []vectorTable{{Name: "memories", IDColumn: "id", TextColumns: []string{"content"}}}},
	}})

	runner := sqliteExecRunner(t, map[string]string{
		"plugin_roca_corpus": corpusPath,
		"plugin_roca_ops":    opsPath,
	})
	embedder := &recordingEmbedder{}
	federation, err := LoadFederation(CoreCLI{Executable: "roca", Run: runner}, root,
		DefaultModel, "v-test", embedder, nil)
	if err != nil {
		t.Fatal(err)
	}
	return federation, corpusPath, opsPath, embedder
}

func writeRegistry(t *testing.T, root string, registry vectorRegistry) {
	t.Helper()
	raw, err := json.Marshal(registry)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, vectorRegistryFilename), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func createSourceDatabase(t *testing.T, path, schema string) {
	t.Helper()
	db := openTestSQLite(t, path)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

func mutateSourceDatabase(t *testing.T, path, statement string) {
	t.Helper()
	db := openTestSQLite(t, path)
	if _, err := db.Exec(statement); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

// -/ 2/4

// -- 3/4 HELPER · Public roca exec boundary over attached synthetic SQLite --

func sqliteExecRunner(t *testing.T, databases map[string]string) CommandRunner {
	t.Helper()
	db := openTestSQLite(t, ":memory:")
	t.Cleanup(func() { _ = db.Close() })
	for alias, path := range databases {
		if _, err := db.Exec(`ATTACH DATABASE ? AS `+quoteIdentifier(alias), path); err != nil {
			t.Fatal(err)
		}
	}
	return func(_ context.Context, _ string, args ...string) ([]byte, error) {
		statement := args[len(args)-1]
		rows, err := db.Query(statement)
		if err != nil {
			return nil, fmt.Errorf("query %q: %w", statement, err)
		}
		defer rows.Close()
		columns, err := rows.Columns()
		if err != nil {
			return nil, err
		}
		result := make([]map[string]any, 0)
		for rows.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for index := range values {
				pointers[index] = &values[index]
			}
			if err := rows.Scan(pointers...); err != nil {
				return nil, err
			}
			item := make(map[string]any, len(columns))
			for index, column := range columns {
				item[column] = values[index]
			}
			result = append(result, item)
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return json.Marshal(map[string]any{"rows": result})
	}
}

// -/ 3/4

// -- 4/4 HELPER · Sidecar assertions --

func sidecarMeta(t *testing.T, path string) map[string]string {
	t.Helper()
	db := openTestSQLite(t, path)
	defer db.Close()
	rows, err := db.Query(`SELECT key,value FROM meta`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	result := map[string]string{}
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			t.Fatal(err)
		}
		result[key] = value
	}
	return result
}

func openTestSQLite(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	return db
}

func flattenInputs(batches [][]string) []string {
	var result []string
	for _, batch := range batches {
		result = append(result, batch...)
	}
	return result
}

func intPointer(value int) *int { return &value }

// -/ 4/4
