package rocacorpus

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/distribution/bundledplugin"
	"github.com/thellmwhisperer/la-roca/internal/store/exactdedup"
)

func TestStorageLawRewriteHonorsCancellation(t *testing.T) {
	db, path := openSchemaDB(t)
	addTitleColumnAndClose(t, db)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := applyStorageLaw(ctx, path, false); err == nil {
		t.Fatal("storage-law rewrite ignored cancellation")
	}
	db, err := bundledplugin.OpenDatabase(path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	present, err := columnExistsDB(context.Background(), db, "session_versions", "title")
	if err != nil {
		t.Fatal(err)
	}
	if !present {
		t.Fatal("canceled storage-law rewrite changed the database")
	}
}

func TestVacuumHonorsCancellation(t *testing.T) {
	path := t.TempDir() + "/roca-corpus.db"
	if err := ApplySchema(path); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := vacuumDatabase(ctx, path); err == nil {
		t.Fatal("VACUUM ignored cancellation")
	}
}

func TestApplySchemaRefusesDuplicatesWithoutDroppingPriorGuard(t *testing.T) {
	db, path := openSchemaDB(t)
	statements := []string{
		`DROP INDEX idx_sessions_exact_payload`,
		`CREATE INDEX idx_sessions_exact_payload ON sessions(source_agent, title, metadata)`,
		`INSERT INTO sessions(session_id, source_agent, title, started_at, metadata)
		 VALUES ('duplicate-a', 'fixture', 'same', '2026-08-16T10:00:00Z', '{}')`,
		`INSERT INTO sessions(session_id, source_agent, title, started_at, metadata)
		 VALUES ('duplicate-b', 'fixture', 'same', '2026-08-16T10:00:00Z', '{}')`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			db.Close()
			t.Fatalf("fixture %q: %v", statement, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	err := ApplySchema(path)
	if err == nil || !strings.Contains(err.Error(), "exact dedup") {
		t.Fatalf("ApplySchema duplicate error = %v", err)
	}
	db, err = bundledplugin.OpenDatabase(path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var indexSQL string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master
		WHERE type = 'index' AND name = 'idx_sessions_exact_payload'`).Scan(&indexSQL); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(indexSQL), "roca_payload_hash") {
		t.Fatalf("failed preflight replaced the prior guard: %s", indexSQL)
	}
}

func TestCanceledCompactRestoresSchemaAfterCommittedRewrite(t *testing.T) {
	db, path := openSchemaDB(t)
	addTitleColumnAndClose(t, db)
	if rewrote, err := applyStorageLaw(context.Background(), path, false); err != nil {
		t.Fatal(err)
	} else if !rewrote {
		t.Fatal("storage-law fixture did not rewrite")
	}
	db, err := bundledplugin.OpenDatabase(path, true)
	if err != nil {
		t.Fatal(err)
	}
	var missing int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master
		WHERE type = 'view' AND name = 'exchange_version_memberships'`).Scan(&missing); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if missing != 0 {
		t.Fatal("storage-law fixture unexpectedly retained the derived view")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := restoreCompactSchema(ctx, path); !errors.Is(err, context.Canceled) {
		t.Fatalf("restoreCompactSchema error = %v, want context canceled", err)
	}
	db, err = bundledplugin.OpenDatabase(path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var restored int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master
		WHERE type = 'view' AND name = 'exchange_version_memberships'`).Scan(&restored); err != nil {
		t.Fatal(err)
	}
	if restored != 1 {
		t.Fatal("canceled compact left the derived schema unrestored")
	}
	guards, err := exactdedup.GuardsInstalled(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if !guards {
		t.Fatal("canceled compact left hash guards unrestored")
	}
}

// openSchemaDB applies the corpus schema to a fresh database and opens it for
// writing, returning the handle and the file path for tests that reopen it.
func openSchemaDB(t *testing.T) (*sql.DB, string) {
	t.Helper()
	path := t.TempDir() + "/roca-corpus.db"
	if err := ApplySchema(path); err != nil {
		t.Fatal(err)
	}
	db, err := bundledplugin.OpenDatabase(path, false)
	if err != nil {
		t.Fatal(err)
	}
	return db, path
}

// addTitleColumnAndClose adds the pre-storage-law title column and closes the
// database, the shared fixture of the cancellation tests.
func addTitleColumnAndClose(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`ALTER TABLE session_versions ADD COLUMN title TEXT`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}
