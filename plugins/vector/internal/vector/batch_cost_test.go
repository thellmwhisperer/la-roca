package vector

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// Pin a WAL reader and mark a fixture-only page from a chunks trigger. Each
// committed transaction containing that page is an embedding transaction;
// counting Embed calls alone would miss a writeBatch regression.
func embeddingTransactionCounter(t *testing.T, path string) func() int {
	t.Helper()
	db := openTestSQLite(t, path)
	t.Cleanup(func() { _ = db.Close() })
	if err := ensureBaseSchema(db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE batch_audit(n INTEGER); INSERT INTO batch_audit VALUES(0);
 CREATE TRIGGER batch_audit_insert AFTER INSERT ON chunks BEGIN UPDATE batch_audit SET n=n+1; END;
 CREATE TRIGGER batch_audit_update AFTER UPDATE ON chunks BEGIN UPDATE batch_audit SET n=n+1; END;
 PRAGMA wal_checkpoint(TRUNCATE);`); err != nil {
		t.Fatal(err)
	}
	var page uint32
	if err := db.QueryRow(`SELECT rootpage FROM sqlite_master WHERE name='batch_audit'`).Scan(&page); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback() })
	var n int
	if err := tx.QueryRow(`SELECT n FROM batch_audit`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return func() int {
		raw, err := os.ReadFile(path + "-wal")
		if err != nil {
			t.Fatal(err)
		}
		if len(raw) < 32 {
			t.Fatal("missing WAL header")
		}
		size := int(binary.BigEndian.Uint32(raw[8:12]))
		count, touched := 0, false
		for off := 32; off+24+size <= len(raw); off += 24 + size {
			if string(raw[off+8:off+16]) != string(raw[16:24]) {
				t.Fatal("WAL reset despite pinned reader")
			}
			touched = touched || binary.BigEndian.Uint32(raw[off:off+4]) == page
			if binary.BigEndian.Uint32(raw[off+4:off+8]) != 0 {
				if touched {
					count++
				}
				touched = false
			}
		}
		return count
	}
}

func TestCostFederationBatchTransactionsAndResume(t *testing.T) {
	federation, corpus, ops, embedder := federationFixture(t)
	federation.WorkerStateDir = t.TempDir()
	holdTestWorkerClaim(t, federation.WorkerStateDir, "batch-cost")
	// Two uneven databases exercise full batches, tails and ordering by batch head.
	for i, path := range []string{corpus, ops} {
		table := &federation.databases[i].Tables[0]
		table.TextColumns = []string{"body"}
		if i == 1 {
			table.TextColumns = []string{"content"}
		}
		table.Chunking = nil
		mutateSourceDatabase(t, path, "DELETE FROM "+table.Name)
		for n := 0; n < 130+i; n++ {
			statement := fmt.Sprintf(`INSERT INTO articles VALUES ('%04d','','fixture text','')`, n*2)
			if i == 1 {
				statement = fmt.Sprintf(`INSERT INTO memories VALUES (%d,'fixture text','active','','')`, n*2+1)
			}
			mutateSourceDatabase(t, path, statement)
		}
	}
	counts := []func() int{embeddingTransactionCounter(t, SidecarPath(corpus)), embeddingTransactionCounter(t, SidecarPath(ops))}
	var mu sync.Mutex
	var progress []int
	federation.Progress = func(p IngestProgress) {
		mu.Lock()
		defer mu.Unlock()
		progress = append(progress, p.Chunks)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	federation.Embedder = &batchInterruptEmbedder{cancel: cancel}
	if _, err := federation.Ingest(ctx, ""); err == nil {
		t.Fatal("expected interrupted batch")
	}
	committed := 0
	for _, path := range []string{corpus, ops} {
		db := openTestSQLite(t, SidecarPath(path))
		var chunks, sources int
		if err := db.QueryRow(`SELECT COUNT(*) FROM chunks`).Scan(&chunks); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow(`SELECT COUNT(*) FROM sources`).Scan(&sources); err != nil {
			t.Fatal(err)
		}
		_ = db.Close()
		if chunks != sources {
			t.Fatalf("partial source progress: %d chunks, %d sources", chunks, sources)
		}
		committed += chunks
	}
	if committed != 64 {
		t.Fatalf("interrupted pass committed %d chunks, want 64", committed)
	}
	federation.Embedder = embedder
	result, err := federation.Ingest(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if result.Added != 261-64 || result.Unchanged != 64 {
		t.Fatalf("resume = %+v", result)
	}
	transactions := counts[0]() + counts[1]()
	if limit := (261+63)/64 + 2; transactions > limit {
		t.Fatalf("%d embedding transactions, limit %d", transactions, limit)
	}
	for _, batch := range embedder.snapshot() {
		if len(batch) > 64 {
			t.Fatalf("oversized batch: %d", len(batch))
		}
	}
	if len(progress) > transactions+4 {
		t.Fatalf("progress callbacks=%d, transactions=%d", len(progress), transactions)
	}
	activity, err := readWorkerActivity(federation.WorkerStateDir)
	if err != nil || activity.Database != "" {
		t.Fatalf("activity not cleared: %+v, %v", activity, err)
	}
	if _, err := os.Stat(filepath.Join(federation.WorkerStateDir, workerActivityFile)); err != nil {
		t.Fatal(err)
	}
	t.Logf("chunks=261 databases=2 embedding_transactions=%d limit=7 resumed_chunks=%d", transactions, result.Added)
}

type batchInterruptEmbedder struct {
	haltingEmbedder
	cancel context.CancelFunc
}

func (e *batchInterruptEmbedder) Embed(ctx context.Context, model string, input []string) ([][]float32, error) {
	vectors, err := e.haltingEmbedder.Embed(ctx, model, input)
	if err != nil {
		e.cancel()
	}
	return vectors, err
}

func TestBatchProgressFailureRollsBackEmbeddings(t *testing.T) {
	federation, corpus, _, _ := federationFixture(t)
	sidecar := SidecarPath(corpus)
	db := openTestSQLite(t, sidecar)
	if err := ensureBaseSchema(db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TRIGGER fail_progress BEFORE INSERT ON sources
 BEGIN SELECT RAISE(ABORT, 'fixture progress failure'); END;`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	if _, err := federation.Ingest(context.Background(), ""); err == nil {
		t.Fatal("expected progress failure")
	}
	db = openTestSQLite(t, sidecar)
	defer db.Close()
	for _, table := range []string{"chunks", "embeddings", "ann_embeddings", "sources"} {
		var count int
		if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("failed batch left %d rows in %s", count, table)
		}
	}
}
