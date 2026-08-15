package rocacorpus

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/distribution/migrationledger"
)

func TestSchemaUpgradeTwicePreservesAnOldCorpusHome(t *testing.T) {
	path := filepath.Join(t.TempDir(), DatabaseFilename)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE sessions (
		session_id TEXT PRIMARY KEY,
		source_agent TEXT DEFAULT 'claude-code',
		project TEXT,
		started_at TEXT,
		ended_at TEXT,
		duration_minutes INTEGER,
		title TEXT,
		metadata TEXT DEFAULT '{}'
	)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO sessions (session_id, title) VALUES ('frozen-session', 'frozen corpus row')`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	if err := applySchema(path); err != nil {
		t.Fatal(err)
	}
	if err := applySchema(path); err != nil {
		t.Fatal(err)
	}
	assertOldCorpusRows(t, path, 1)
}

func assertOldCorpusRows(t *testing.T, path string, want int) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var got int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE title = 'frozen corpus row'`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("old corpus rows = %d, want %d", got, want)
	}
	state, err := migrationledger.Inspect(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	if state.Plugin != Name || state.State != migrationledger.StatePrepared {
		t.Fatalf("corpus migration state = %+v", state)
	}
}
