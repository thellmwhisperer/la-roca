package vector

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"runtime"
	"testing"
	"time"
)

func TestCostUnchangedSourceAndStoredStatus(t *testing.T) {
	f, source, _, _ := federationFixture(t)
	// Undeclared ballast measures database reads independently of embedding cost.
	mutateSourceDatabase(t, source, `CREATE TABLE ballast(body BLOB); INSERT INTO ballast VALUES(zeroblob(32*1024*1024));`)
	ctx := context.Background()
	first, err := f.Ingest(ctx, "")
	if err != nil || first.Chunks == 0 {
		t.Fatalf("index: %+v %v", first, err)
	}
	info, err := os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}
	oldHash := hashVectorSource
	var bytes int64
	hashVectorSource = func(path, contract string) (string, error) {
		for _, suffix := range []string{"", "-wal"} {
			if info, err := os.Stat(path + suffix); err == nil {
				bytes += info.Size()
			}
		}
		return oldHash(path, contract)
	}
	t.Cleanup(func() { hashVectorSource = oldHash })
	run := f.Core.Run
	f.Core.Run = func(context.Context, string, ...string) ([]byte, error) {
		t.Error("unchanged pass queried source text")
		return nil, fmt.Errorf("source reads forbidden")
	}
	// Linux rchar counts logical read/pread bytes, including cached reads and
	// sidecar overhead. Elsewhere the hash seam counts its complete input bytes.
	readChars := func() int64 {
		if runtime.GOOS != "linux" {
			return 0
		}
		raw, err := os.ReadFile("/proc/self/io")
		if err != nil {
			t.Fatal(err)
		}
		var n int64
		if _, err := fmt.Sscanf(string(raw), "rchar: %d", &n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	before := readChars()
	steady, err := f.Ingest(ctx, "")
	ioBytes := readChars() - before
	if err != nil || steady.Unchanged != first.Chunks {
		t.Fatalf("steady: %+v %v", steady, err)
	}
	if bytes >= info.Size()/100 || ioBytes >= info.Size()/100 {
		t.Fatalf("unchanged bytes: hashed=%d process-read=%d database=%d", bytes, ioBytes, info.Size())
	}
	started := time.Now()
	report, err := ReportVectorization(ctx, StatusRequest{PluginRoot: f.PluginRoot})
	elapsed := time.Since(started)
	if err != nil {
		t.Fatal(err)
	}
	var total int64
	for _, row := range report.Databases {
		if row.CandidateChunks == nil || row.EmbeddedChunks == nil || *row.CandidateChunks != *row.EmbeddedChunks {
			t.Fatalf("stored exact counts: %+v", row)
		}
		total += *row.CandidateChunks
	}
	if total != int64(first.Chunks) || elapsed >= 300*time.Millisecond {
		t.Fatalf("status total=%d latency=%s", total, elapsed)
	}
	t.Logf("database bytes=%d hash bytes=%d process logical read bytes=%d status=%s exact chunks=%d", info.Size(), bytes, ioBytes, elapsed, total)
	f.Verify = true
	if _, err := f.Ingest(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if bytes < info.Size() {
		t.Fatal("explicit verification did not hash source")
	}
	f.Core.Run = run
	f.Verify = false
	mutateSourceDatabase(t, source, `UPDATE articles SET body='One source changed' WHERE id='article-1'`)
	for pass := 0; pass < 2; pass++ {
		delta, err := f.Ingest(ctx, "")
		if err != nil || delta.Sources != first.Sources {
			t.Fatalf("stored source count after mixed delta: %+v %v", delta, err)
		}
	}
}

func TestCompletedGenerationInvalidation(t *testing.T) {
	for _, change := range []string{"wal", "replace", "restore", "partial", "interrupted", "during-pass", "contract", "legacy-count"} {
		t.Run(change, func(t *testing.T) {
			f, source, _, embedder := federationFixture(t)
			ctx := context.Background()
			var writer *sql.DB
			if change == "wal" {
				writer = openTestSQLite(t, source)
				t.Cleanup(func() { writer.Close() })
				if _, err := writer.Exec(`PRAGMA journal_mode=WAL; PRAGMA wal_autocheckpoint=0;
					UPDATE articles SET body=body; PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := f.Ingest(ctx, ""); err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(source)
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "wal":
				if _, err := writer.Exec(`UPDATE articles SET body='WAL changed body'`); err != nil {
					t.Fatal(err)
				}
				after, err := os.Stat(source)
				if err != nil {
					t.Fatal(err)
				}
				if info.Size() != after.Size() || info.ModTime() != after.ModTime() || !os.SameFile(info, after) {
					t.Fatal("WAL-only fixture changed the main database")
				}
			case "replace", "restore":
				replacement := source + ".replacement"
				raw, err := os.ReadFile(source)
				if err != nil {
					t.Fatal(err)
				}
				if change == "restore" {
					mutateSourceDatabase(t, source, `UPDATE articles SET body='Modified body'`)
					raw, err = os.ReadFile(source)
					if err != nil {
						t.Fatal(err)
					}
					replacement = source
				}
				if err := os.WriteFile(replacement, raw, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chtimes(replacement, info.ModTime(), info.ModTime()); err != nil {
					t.Fatal(err)
				}
				if change == "replace" {
					if err := os.Rename(replacement, source); err != nil {
						t.Fatal(err)
					}
				}
			case "partial":
				if _, err := f.Ingest(ctx, "articles"); err != nil {
					t.Fatal(err)
				}
			case "interrupted", "during-pass":
				mutateSourceDatabase(t, source, `UPDATE articles SET body='Changed before pass'`)
				run := f.Core.Run
				changed := false
				f.Core.Run = func(ctx context.Context, exe string, args ...string) ([]byte, error) {
					if change == "interrupted" {
						return nil, context.Canceled
					}
					if !changed {
						changed = true
						mutateSourceDatabase(t, source, `UPDATE articles SET body='Changed during pass'`)
					}
					return run(ctx, exe, args...)
				}
				_, err := f.Ingest(ctx, "")
				if change == "interrupted" && err == nil {
					t.Fatal("interrupted pass succeeded")
				}
				if change == "during-pass" && err != nil {
					t.Fatal(err)
				}
				f.Core.Run = run
			case "legacy-count":
				store := openTestSQLite(t, SidecarPath(source))
				if _, err := store.Exec(`DELETE FROM meta WHERE key='completed_generation'; UPDATE meta SET value='999' WHERE key='completed_chunks'`); err != nil {
					t.Fatal(err)
				}
				store.Close()
			case "contract":
				f.databases[0].Tables[0].Chunking.MaxChars = intPointer(30)
				writeRegistry(t, f.PluginRoot, vectorRegistry{Schema: vectorRegistrySchema, Databases: f.databases})
			}
			report, err := ReportVectorization(ctx, StatusRequest{PluginRoot: f.PluginRoot})
			if err != nil {
				t.Fatal(err)
			}
			if row := report.Databases[0]; row.CandidateChunks != nil || (row.State == StateComplete && change != "legacy-count") {
				t.Fatalf("stale generation: %+v", row)
			}
			if _, err := f.Ingest(ctx, ""); err != nil {
				t.Fatal(err)
			}
			report, err = ReportVectorization(ctx, StatusRequest{PluginRoot: f.PluginRoot})
			if err != nil {
				t.Fatal(err)
			}
			if row := report.Databases[0]; row.CandidateChunks == nil || row.State != StateComplete {
				t.Fatalf("resumed generation: %+v", row)
			}
			if len(embedder.inputs) == 0 {
				t.Fatal("fixture never indexed")
			}
		})
	}
}

func TestStatusDoesNotReadDeclaredText(t *testing.T) {
	f, source, _, _ := federationFixture(t)
	if _, err := f.Ingest(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	// A failing view would make any attempt to count/chunk declared text fail.
	mutateSourceDatabase(t, source, `ALTER TABLE articles RENAME TO originals; CREATE VIEW articles AS SELECT id,title,body,telemetry FROM originals WHERE abs(-9223372036854775808)>0`)
	marker, err := sourceFileMarker(source)
	if err != nil {
		t.Fatal(err)
	}
	db := openTestSQLite(t, SidecarPath(source))
	if _, err := db.Exec(`UPDATE meta SET value=? WHERE key IN (?,?)`, marker, sourceMarkerMetaKey, "completed_generation"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	report, err := ReportVectorization(context.Background(), StatusRequest{PluginRoot: f.PluginRoot})
	if err != nil || report.Databases[0].CandidateChunks == nil {
		t.Fatalf("status read source: %+v %v", report, err)
	}
}
