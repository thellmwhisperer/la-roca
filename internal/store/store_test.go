package store_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/store"
)

func TestOpenTightensTheDatabaseFileToOperatorOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "roca.db")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("database info = %v, err=%v; want mode 0600", info, err)
	}
}

func TestOpenTightensExistingWALSidecarsToOperatorOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "roca.db")
	first, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { first.Close() })
	if _, err := first.SQL().Exec("CREATE TABLE permission_fixture (id INTEGER)"); err != nil {
		t.Fatal(err)
	}
	for _, sidecar := range []string{path + "-wal", path + "-shm"} {
		if err := os.Chmod(sidecar, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	second, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { second.Close() })
	for _, sidecar := range []string{path + "-wal", path + "-shm"} {
		info, err := os.Stat(sidecar)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Errorf("sidecar info = %v, err=%v; want mode 0600", info, err)
		}
	}
}

func TestOpenAppliesWALAndBusyTimeout(t *testing.T) {
	db := openFresh(t)

	var journal string
	if err := db.SQL().QueryRow("PRAGMA journal_mode").Scan(&journal); err != nil {
		t.Fatalf("PRAGMA journal_mode: %v", err)
	}
	if journal != "wal" {
		t.Errorf("journal_mode = %q, want wal", journal)
	}

	var busy int
	if err := db.SQL().QueryRow("PRAGMA busy_timeout").Scan(&busy); err != nil {
		t.Fatalf("PRAGMA busy_timeout: %v", err)
	}
	if busy != 15000 {
		t.Errorf("busy_timeout = %d, want 15000", busy)
	}
}

func TestOpenStrictReadOnlyCannotAlterTheDatabaseOrItsPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "external.db")
	writable, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writable.SQL().Exec("CREATE TABLE fixture (value TEXT); INSERT INTO fixture VALUES ('Ada owns Quartz')"); err != nil {
		t.Fatal(err)
	}
	if err := writable.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}

	db, err := store.OpenStrictReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	reader, err := db.ReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	var value string
	if err := reader.QueryRow("SELECT value FROM fixture").Scan(&value); err != nil || value != "Ada owns Quartz" {
		t.Fatalf("strict read = %q, err=%v", value, err)
	}
	if _, err := db.SQL().Exec("INSERT INTO fixture VALUES ('mutation')"); err == nil {
		t.Fatal("strict read-only handle accepted a write")
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o644 {
		t.Fatalf("external database mode = %v, err=%v; want unchanged 0644", info, err)
	}
}

func TestOpenStrictReadOnlyExplainsTheWALSharedIndex(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the directory permissions this diagnosis needs")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "external.db")
	writable, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writable.SQL().Exec("CREATE TABLE fixture (value TEXT)"); err != nil {
		t.Fatal(err)
	}
	if err := writable.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	db, err := store.OpenStrictReadOnly(path)
	if err == nil {
		db.Close()
		t.Skip("this filesystem serves a WAL database read-only")
	}
	if !strings.Contains(err.Error(), "shared index") {
		t.Fatalf("read-only open error = %v; want the WAL shared index named", err)
	}
}

func TestApplySchemaCreatesTheSevenV1Tables(t *testing.T) {
	db := openFresh(t)
	if err := store.ApplySchema(context.Background(), db); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}

	want := []string{
		"exchanges", "ingest_file_state", "layers", "memories",
		"sessions", "thinking_blocks", "tool_uses",
	}
	tables := tableNames(t, db.SQL())
	if len(tables) != len(want) {
		t.Fatalf("tables = %v, want exactly %v", tables, want)
	}
	for i, name := range want {
		if tables[i] != name {
			t.Errorf("table[%d] = %q, want %q", i, tables[i], name)
		}
	}
}

// The v1 schema contains only tables used by the current command and tool set.
func TestApplySchemaCreatesNoTableOutsideV1(t *testing.T) {
	db := openFresh(t)
	if err := store.ApplySchema(context.Background(), db); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}

	for _, missing := range []string{"messages", "proposals", "runs", "run_logs", "layer_stats"} {
		var n int
		err := db.SQL().QueryRow(
			"SELECT COUNT(*) FROM sqlite_master WHERE name = ?", missing).Scan(&n)
		if err != nil {
			t.Fatalf("sqlite_master: %v", err)
		}
		if n != 0 {
			t.Errorf("v1 created %q, which is out of scope", missing)
		}
	}
}

func TestApplySchemaIsIdempotent(t *testing.T) {
	db := openFresh(t)
	ctx := context.Background()
	if err := store.ApplySchema(ctx, db); err != nil {
		t.Fatalf("first ApplySchema: %v", err)
	}
	if err := store.ApplySchema(ctx, db); err != nil {
		t.Fatalf("second ApplySchema: %v", err)
	}
	if got := len(tableNames(t, db.SQL())); got != 7 {
		t.Errorf("tables = %d after two passes, want 7", got)
	}
}

func TestWriteOpensTheTransactionInImmediateMode(t *testing.T) {
	db := openFresh(t)
	ctx := context.Background()
	if err := store.ApplySchema(ctx, db); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}

	// A transaction that reads before writing takes a read snapshot and its
	// promotion fails with SQLITE_BUSY_SNAPSHOT, which the busy handler never
	// retries. IMMEDIATE takes the write lock when it opens, so another
	// concurrent writer waits instead of blowing up.
	err := db.Write(ctx, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRow("SELECT COUNT(*) FROM memories").Scan(&n); err != nil {
			return err
		}
		_, err := tx.Exec(
			"INSERT INTO memories (layer, content, origin) VALUES (?, ?, ?)",
			"project", "anchor", "agent")
		return err
	})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}

	if got := countMemories(t, db.SQL()); got != 1 {
		t.Errorf("memories = %d, want 1", got)
	}
}

func TestWriteRollsBackTheTransactionIfTheBodyFails(t *testing.T) {
	db := openFresh(t)
	ctx := context.Background()
	if err := store.ApplySchema(ctx, db); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}

	err := db.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.Exec(
			"INSERT INTO memories (layer, content, origin) VALUES (?, ?, ?)",
			"project", "a medias", "agent"); err != nil {
			return err
		}
		return errBodyFailure
	})
	if err == nil {
		t.Fatal("Write returned nil, want the body's error")
	}
	if got := countMemories(t, db.SQL()); got != 0 {
		t.Errorf("memories = %d after a failure, want 0", got)
	}
}

var errBodyFailure = errFailure("the body failed")

type errFailure string

func (e errFailure) Error() string { return string(e) }

// --- helpers ---

func openFresh(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "roca.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func tableNames(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.Query(
		`SELECT name FROM sqlite_master
		  WHERE type = 'table' AND name NOT LIKE 'sqlite_%'
		  ORDER BY name`)
	if err != nil {
		t.Fatalf("sqlite_master: %v", err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatalf("scan: %v", err)
		}
		names = append(names, n)
	}
	return names
}

func countMemories(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM memories").Scan(&n); err != nil {
		t.Fatalf("COUNT memories: %v", err)
	}
	return n
}
