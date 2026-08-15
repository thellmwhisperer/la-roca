package rocacron

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/distribution/migrationledger"
)

func TestSchemaUpgradeTwicePreservesAnOldCronHome(t *testing.T) {
	path := filepath.Join(t.TempDir(), DatabaseFilename)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE journeys (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		train TEXT NOT NULL,
		ride TEXT NOT NULL,
		plugin TEXT NOT NULL,
		started_at TEXT NOT NULL,
		ended_at TEXT NOT NULL,
		duration_ms INTEGER NOT NULL,
		exit_code INTEGER,
		error TEXT NOT NULL DEFAULT '',
		gate_status TEXT NOT NULL,
		stdout TEXT NOT NULL DEFAULT '',
		stderr TEXT NOT NULL DEFAULT ''
	)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO journeys
		(train, ride, plugin, started_at, ended_at, duration_ms, gate_status)
		VALUES ('nightly', 'frozen-cron-row', 'synthetic', 'start', 'end', 1, 'ready')`); err != nil {
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
	assertOldCronRows(t, path, 1)
}

func assertOldCronRows(t *testing.T, path string, want int) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var got int
	if err := db.QueryRow(`SELECT COUNT(*) FROM journeys WHERE ride = 'frozen-cron-row'`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("old cron rows = %d, want %d", got, want)
	}
	state, err := migrationledger.Inspect(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	if state.Plugin != Name || state.State != migrationledger.StatePrepared {
		t.Fatalf("cron migration state = %+v", state)
	}
}
