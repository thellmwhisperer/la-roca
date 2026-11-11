package vector

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestQueryNamesTheCauseWhenTheIndexHasNoMetaTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vector.db")
	db := openTestSQLite(t, path)
	if _, err := db.Exec(`CREATE TABLE unrelated(x)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	index := Index{Corpus: &memoryCorpus{}, VectorPath: path, Model: DefaultModel, Embedder: &recordingEmbedder{}}
	_, err := index.Query(context.Background(), "alpha", 10)
	if err == nil || !strings.HasPrefix(err.Error(), "read vector index:") || !strings.Contains(err.Error(), "no such table: meta") {
		t.Fatalf("Query error = %v, want read vector index: ... no such table: meta", err)
	}
}

func TestReadIndexMetaReadsAFreshIndexAsNoModelAndRejectsBadDimensions(t *testing.T) {
	db := openTestSQLite(t, filepath.Join(t.TempDir(), "vector.db"))
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE meta(key TEXT PRIMARY KEY, value TEXT)`); err != nil {
		t.Fatal(err)
	}
	if model, dimensions, err := readIndexMeta(db); err != nil || model != "" || dimensions != 0 {
		t.Fatalf("fresh meta = %q, %d, %v; want empty, 0, nil", model, dimensions, err)
	}
	if _, err := db.Exec(`INSERT INTO meta(key,value) VALUES ('model','m'),('dimensions','eight')`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readIndexMeta(db); err == nil || !strings.Contains(err.Error(), "eight") {
		t.Fatalf("bad dimensions error = %v, want one naming the value", err)
	}
}
