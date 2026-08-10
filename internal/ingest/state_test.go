package ingest

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/thellmwhisperer/la-roca/internal/ingest/parsers"
)

func TestDatabaseFingerprintChangesWithWAL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`PRAGMA journal_mode=WAL; PRAGMA wal_autocheckpoint=0;
		CREATE TABLE events (value TEXT)`); err != nil {
		t.Fatal(err)
	}
	target := Target{Path: path, Kind: parsers.KindOpenCodeDB}
	before, err := targetFingerprint(target)
	if err != nil {
		t.Fatal(err)
	}
	mainBefore, err := Fingerprint(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO events VALUES ('committed in the wal')`); err != nil {
		t.Fatal(err)
	}
	mainAfter, err := Fingerprint(path)
	if err != nil {
		t.Fatal(err)
	}
	if mainAfter != mainBefore {
		t.Fatalf("test setup checkpointed the database: %q != %q", mainAfter, mainBefore)
	}
	after, err := targetFingerprint(target)
	if err != nil {
		t.Fatal(err)
	}
	if after == before {
		t.Fatalf("database fingerprint stayed %q after a WAL commit", after)
	}
}
