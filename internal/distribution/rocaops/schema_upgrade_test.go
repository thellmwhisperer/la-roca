package rocaops

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/distribution/migrationledger"
)

func TestSchemaUpgradeTwicePreservesAnOldOpsHome(t *testing.T) {
	path := filepath.Join(t.TempDir(), DatabaseFilename)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE memories (
		id              INTEGER PRIMARY KEY AUTOINCREMENT,
		layer           TEXT NOT NULL,
		content         TEXT NOT NULL,
		metadata        TEXT DEFAULT '{}',
		origin          TEXT NOT NULL,
		source_agent    TEXT,
		source_model    TEXT,
		source_surface  TEXT,
		source_session  TEXT,
		source_sequence INTEGER,
		project         TEXT,
		status          TEXT DEFAULT 'active',
		supersedes      INTEGER,
		created_at      TEXT DEFAULT (datetime('now')),
		expires_at      TEXT
	)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO memories (layer, content, origin) VALUES ('handoff', 'frozen ops row', 'agent')`); err != nil {
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
	assertOldOpsRows(t, path, 1)
}

func assertOldOpsRows(t *testing.T, path string, want int) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var got int
	if err := db.QueryRow(`SELECT COUNT(*) FROM memories WHERE content = 'frozen ops row'`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("old ops rows = %d, want %d", got, want)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM memories_fts WHERE memories_fts MATCH 'frozen'`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("indexed old ops rows = %d, want %d", got, want)
	}
	state, err := migrationledger.Inspect(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	if state.Plugin != Name || state.State != migrationledger.StatePrepared {
		t.Fatalf("ops migration state = %+v", state)
	}
}
