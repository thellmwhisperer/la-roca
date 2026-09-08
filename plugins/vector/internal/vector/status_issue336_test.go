package vector

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestReportVectorizationTreatsSealedSidecarWithoutMarkerAsComplete(t *testing.T) {
	root := t.TempDir()
	database := vectorDatabase{
		Plugin: "roca-corpus", Database: "corpus", Path: "roca-corpus.db", Alias: "corpus",
		Tables: []vectorTable{{Name: "notes", IDColumn: "id", TextColumns: []string{"body"}}},
	}
	writeRegistry(t, root, vectorRegistry{Schema: 2, Databases: []vectorDatabase{database}})
	path := filepath.Join(root, database.Plugin, database.Path)
	writeSourceRows(t, path, `CREATE TABLE notes(id TEXT PRIMARY KEY, body TEXT);
		INSERT INTO notes VALUES ('a','alpha');`)
	writeSidecarWithChunks(t, SidecarPath(path), database.owner(), 1, map[string]string{
		"contract": database.contractFingerprint(), "source_fingerprint": "sealed-without-marker",
	})

	report, err := ReportVectorization(context.Background(), StatusRequest{PluginRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	row := report.Databases[0]
	if row.State != StateComplete {
		t.Fatalf("sealed sidecar without source_marker = %q, want complete", row.State)
	}
	if row.EmbeddedChunks == nil || *row.EmbeddedChunks != 1 {
		t.Fatalf("embedded chunks = %v, want 1", row.EmbeddedChunks)
	}
}

func TestReportVectorizationReportsStaleAndLiveIndexLock(t *testing.T) {
	root := t.TempDir()
	staleDB := vectorDatabase{
		Plugin: "roca-ops", Database: "ops", Path: "roca-ops.db", Alias: "ops",
		Tables: []vectorTable{{Name: "memories", IDColumn: "id", TextColumns: []string{"content"}}},
	}
	liveDB := vectorDatabase{
		Plugin: "roca-corpus", Database: "corpus", Path: "roca-corpus.db", Alias: "corpus",
		Tables: []vectorTable{{Name: "notes", IDColumn: "id", TextColumns: []string{"body"}}},
	}
	writeRegistry(t, root, vectorRegistry{Schema: 2, Databases: []vectorDatabase{staleDB, liveDB}})
	stalePath := filepath.Join(root, staleDB.Plugin, staleDB.Path)
	livePath := filepath.Join(root, liveDB.Plugin, liveDB.Path)
	writeSourceRows(t, stalePath, `CREATE TABLE memories(id TEXT PRIMARY KEY, content TEXT);
		INSERT INTO memories VALUES ('a','alpha');`)
	writeSourceRows(t, livePath, `CREATE TABLE notes(id TEXT PRIMARY KEY, body TEXT);
		INSERT INTO notes VALUES ('a','alpha');`)
	staleSidecar := SidecarPath(stalePath)
	liveSidecar := SidecarPath(livePath)
	writeSidecarWithChunks(t, staleSidecar, staleDB.owner(), 1, nil)
	writeSidecarWithChunks(t, liveSidecar, liveDB.owner(), 1, nil)
	if err := os.WriteFile(staleSidecar+".index.lock", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	release, err := lockFile(liveSidecar + ".index.lock")
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	report, err := ReportVectorization(context.Background(), StatusRequest{PluginRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]DatabaseVectorization{}
	for _, row := range report.Databases {
		got[row.Plugin] = row
	}
	if got["roca-ops"].IndexLock != IndexLockStale {
		t.Fatalf("stale lock = %q, want stale", got["roca-ops"].IndexLock)
	}
	if got["roca-corpus"].IndexLock != IndexLockLive {
		t.Fatalf("live lock = %q, want live", got["roca-corpus"].IndexLock)
	}
}

func TestReportVectorizationRecommendsCompactWhenBytesPerChunkAreHigh(t *testing.T) {
	root := t.TempDir()
	database := vectorDatabase{
		Plugin: "roca-corpus", Database: "corpus", Path: "roca-corpus.db", Alias: "corpus",
		Tables: []vectorTable{{Name: "notes", IDColumn: "id", TextColumns: []string{"body"}}},
	}
	writeRegistry(t, root, vectorRegistry{Schema: 2, Databases: []vectorDatabase{database}})
	path := filepath.Join(root, database.Plugin, database.Path)
	writeSourceRows(t, path, `CREATE TABLE notes(id TEXT PRIMARY KEY, body TEXT);
		INSERT INTO notes VALUES ('a','alpha');`)
	sidecar := SidecarPath(path)
	writeSidecarWithChunks(t, sidecar, database.owner(), 1, map[string]string{
		"contract": database.contractFingerprint(), "source_fingerprint": "sealed",
	})
	store := openTestSQLite(t, sidecar)
	if _, err := store.Exec(`CREATE TABLE bloat(payload BLOB); INSERT INTO bloat VALUES (zeroblob(256*1024));`); err != nil {
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
	if !row.CompactRecommended {
		t.Fatalf("compact_recommended = false for %d bytes / %v chunks",
			valueOrZero(row.SidecarBytes), row.EmbeddedChunks)
	}
}

func TestQueryDoesNotLoadEveryStoredChunk(t *testing.T) {
	corpus := &memoryCorpus{sources: []sourceRow{
		{kind: "memories", sourceID: "keep", text: "alpha memory"},
	}}
	path := filepath.Join(t.TempDir(), "vector.db")
	index := Index{Corpus: corpus, VectorPath: path, Model: DefaultModel,
		Embedder: &recordingEmbedder{}, Database: "corpus"}
	if _, err := index.Ingest(context.Background()); err != nil {
		t.Fatal(err)
	}
	db := openTestSQLite(t, path)
	for i := 0; i < 20000; i++ {
		if _, err := db.Exec(`INSERT INTO chunks(source_kind,source_id,text_column,chunk_index,fingerprint,locator)
			VALUES('notes',?,?,0,'fp','loc')`, i, i); err != nil {
			db.Close()
			t.Fatal(err)
		}
	}
	started := time.Now()
	if _, _, _, err := readIndexState(db, nil); err != nil {
		db.Close()
		t.Fatal(err)
	}
	scan := time.Since(started)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	started = time.Now()
	results, err := index.Query(context.Background(), "alpha", 5)
	if err != nil {
		t.Fatal(err)
	}
	query := time.Since(started)
	if len(results) == 0 {
		t.Fatal("query returned no hits")
	}
	if scan >= 30*time.Millisecond && query >= scan {
		t.Fatalf("query %s loaded chunk state that took %s", query, scan)
	}
}

func valueOrZero(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}
