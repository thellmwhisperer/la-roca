package store_test

import (
	"context"
	"os"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/store"
)

func TestAdoptDropsOnlyTheDeadEmbeddingsWeightAndVacuums(t *testing.T) {
	db := withDeadEmbeddings(t)
	ctx := context.Background()
	execute(t, db, `CREATE INDEX idx_embeddings_model ON embeddings(id)`)
	execute(t, db, `INSERT INTO embeddings(vector) VALUES (zeroblob(1048576))`)

	report, err := store.Adopt(ctx, db, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if report.Diet == nil || !report.Diet.EmbeddingsDropped || !report.Diet.Vacuumed {
		t.Fatalf("database diet = %#v", report.Diet)
	}
	if report.Diet.EmbeddingIndexesDropped != 1 {
		t.Errorf("dropped indexes = %d, want 1", report.Diet.EmbeddingIndexesDropped)
	}
	if report.Diet.BytesBefore <= report.Diet.BytesAfter {
		t.Errorf("database bytes %d -> %d, want a measured reduction",
			report.Diet.BytesBefore, report.Diet.BytesAfter)
	}
	if report.BackupPath == "" {
		t.Fatal("destructive migration did not take a backup")
	}
	if _, err := os.Stat(report.BackupPath); err != nil {
		t.Fatalf("backup is not readable: %v", err)
	}
	var count int
	if err := db.SQL().QueryRow(`SELECT COUNT(*) FROM sqlite_master
		WHERE type='table' AND name='embeddings'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("embeddings table count = %d, err %v", count, err)
	}

	second, err := store.Adopt(ctx, db, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if second.Diet != nil || second.BackupPath != "" {
		t.Errorf("idempotent adoption repeated work: %#v", second)
	}
}

func TestAdoptRefusesToDropEmbeddingsWhenTheDatabaseStillReferencesIt(t *testing.T) {
	db := withDeadEmbeddings(t)
	ctx := context.Background()
	execute(t, db, `CREATE VIEW retained_vectors AS SELECT id FROM embeddings`)

	if _, err := store.Adopt(ctx, db, t.TempDir()); err == nil {
		t.Fatal("adoption dropped a table that a live schema object still reads")
	}
	var count int
	if err := db.SQL().QueryRow(`SELECT COUNT(*) FROM sqlite_master
		WHERE type='table' AND name='embeddings'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("embeddings table count = %d, err %v", count, err)
	}
}

func withDeadEmbeddings(t *testing.T) *store.DB {
	t.Helper()
	db := openFresh(t)
	if err := store.ApplySchema(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	execute(t, db, `CREATE TABLE embeddings (id INTEGER PRIMARY KEY, vector BLOB)`)
	return db
}
