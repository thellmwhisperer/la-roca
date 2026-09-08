package vector

import (
	"context"
	"fmt"
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

func TestReportVectorizationRecommendsCompactForReclaimableEmbeddingPages(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	database := vectorDatabase{
		Plugin: "roca-corpus", Database: "corpus", Path: "roca-corpus.db", Alias: "corpus",
		Tables: []vectorTable{{Name: "notes", IDColumn: "id", TextColumns: []string{"body"}}},
	}
	writeRegistry(t, root, vectorRegistry{Schema: 2, Databases: []vectorDatabase{database}})
	path := filepath.Join(root, database.Plugin, database.Path)
	writeSourceRows(t, path, `CREATE TABLE notes(id TEXT PRIMARY KEY, body TEXT);
		INSERT INTO notes VALUES ('a','alpha');`)
	sources := make([]sourceRow, 4097)
	for i := range sources {
		sources[i] = sourceRow{kind: "sessions", sessionID: fmt.Sprint(i), text: "alpha"}
	}
	corpus := &memoryCorpus{sources: sources[:1]}
	index := Index{Corpus: corpus, VectorPath: SidecarPath(path), Model: DefaultModel,
		Embedder: &recordingEmbedder{}, Database: "corpus"}
	check := func(stage string, chunks, pages int64, recommended bool) {
		t.Helper()
		report, err := ReportVectorization(ctx, StatusRequest{PluginRoot: root})
		if err != nil {
			t.Fatal(err)
		}
		row := report.Databases[0]
		if row.EmbeddedChunks == nil || *row.EmbeddedChunks != chunks ||
			row.EmbeddingPages == nil || *row.EmbeddingPages != pages ||
			row.CompactRecommended != recommended {
			t.Fatalf("%s: chunks=%v pages=%v recommended=%v; want %d, %d, %v",
				stage, valueOrZero(row.EmbeddedChunks), valueOrZero(row.EmbeddingPages),
				row.CompactRecommended, chunks, pages, recommended)
		}
	}
	ingest := func() {
		t.Helper()
		if _, err := index.Ingest(ctx); err != nil {
			t.Fatal(err)
		}
	}
	compact := func() CompactReport {
		t.Helper()
		report, err := Compact(ctx, index.VectorPath)
		if err != nil {
			t.Fatal(err)
		}
		return report
	}
	ingest()
	check("fresh single chunk", 1, 1, false)
	compact()
	check("compacted single chunk", 1, 1, false)
	corpus.sources = sources
	ingest()
	check("dense multi-page index", 4097, 5, false)
	corpus.sources = sources[:1]
	ingest()
	check("sparse index", 1, 5, true)
	if report := compact(); report.BytesReclaimed <= 0 {
		t.Fatalf("sparse compaction reclaimed no bytes: %+v", report)
	}
	check("compacted sparse index", 1, 1, false)
	corpus.sources = nil
	ingest()
	check("deleted all chunks", 0, 1, true)
	if report := compact(); report.BytesReclaimed <= 0 {
		t.Fatalf("empty compaction reclaimed no bytes: %+v", report)
	}
	check("compacted empty index", 0, 0, false)
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
	t.Logf("Query API with 20000 unrelated stored chunks: scan=%s query=%s results=%+v", scan, query, results)
	if len(results) == 0 {
		t.Fatal("query returned no hits")
	}
	if scan >= 30*time.Millisecond && query >= scan {
		t.Fatalf("query %s loaded chunk state that took %s", query, scan)
	}
}

// A non-retrieved row must not be decoded just to initialize a query. This
// sentinel makes the regression deterministic even on fast machines where
// the timing comparison above is below its noise threshold.
func TestQueryDoesNotDecodeUnrelatedChunkMetadata(t *testing.T) {
	corpus := &memoryCorpus{sources: []sourceRow{
		{kind: "memories", sourceID: "keep", text: "alpha memory"},
	}}
	index := Index{Corpus: corpus, VectorPath: filepath.Join(t.TempDir(), "vector.db"),
		Model: DefaultModel, Embedder: &recordingEmbedder{}, Database: "corpus"}
	if _, err := index.Ingest(context.Background()); err != nil {
		t.Fatal(err)
	}
	db := openTestSQLite(t, index.VectorPath)
	_, err := db.Exec(`INSERT INTO chunks(source_kind,source_id,chunk_index,fingerprint,locator)
		VALUES('notes','unrelated','not-an-integer','fp','{}')`)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	hits, err := index.Query(context.Background(), "alpha", 5)
	if err != nil || len(hits) != 1 || hits[0].Text != "alpha memory" {
		t.Fatalf("query decoded unrelated metadata: hits=%+v err=%v", hits, err)
	}
	t.Logf("Query API returned the intact matching source despite unrelated malformed metadata: %+v", hits)
}

func valueOrZero(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}
