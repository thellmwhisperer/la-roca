//go:build acceptance

package acceptance

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// Issue #461: an external operator's interrupted migrate left a real `layers`
// table inside the roca-corpus plugin database. The pre-fix binary refused the
// plugin — "semantic layer omits database table layers" — and the corpus owner
// of ingest went unavailable, taking doctor down with rc=1. The recovery this
// suite proves: on the same database state, the fixed binary's doctor exits 0,
// names the orphan table, ingest serves, a supported service query still
// answers, and the orphan's rows are exactly where they were.
//
// Representative fixture only: a disposable HOME, an empty harvested corpus,
// one seeded orphan row. Never an operator home, never personal data.
func TestInterruptedMigrateOrphanKeepsTheServiceServing(t *testing.T) {
	binary, err := rocaBinary()
	if err != nil {
		t.Fatal(err)
	}
	home := disposableSmokeHome(t, "roca-461-orphan-")
	m := &world{binary: binary, home: home}
	if err := m.mustRun(m.initCommand(true)); err != nil {
		t.Fatalf("init: %v\n%s", err, m.last.stderr)
	}

	corpusPath := filepath.Join(home, ".roca", "plugins", "roca-corpus", "roca-corpus.db")
	if err := seedLayersOrphan(corpusPath); err != nil {
		t.Fatalf("seed the #461 fixture state: %v", err)
	}

	if err := m.mustRun("roca doctor --json"); err != nil {
		t.Fatalf("doctor: %v\n%s", err, m.last.stderr)
	}
	if strings.Contains(m.last.stdout+m.last.stderr, "omits database table") {
		t.Fatalf("doctor still reports the #461 rejection:\n%s", m.last.stdout)
	}
	if strings.Contains(m.last.stdout+m.last.stderr, "owns ingest is unavailable") {
		t.Fatalf("doctor still loses the corpus plugin:\n%s", m.last.stdout)
	}
	var report struct {
		Warnings []string `json:"warnings"`
	}
	if err := json.Unmarshal([]byte(m.last.stdout), &report); err != nil {
		t.Fatalf("doctor JSON: %v\n%s", err, m.last.stdout)
	}
	named := false
	for _, warning := range report.Warnings {
		if strings.Contains(warning, "roca-corpus") && strings.Contains(warning, "layers") {
			named = true
		}
	}
	if !named {
		t.Fatalf("doctor warnings %v do not name the corpus orphan", report.Warnings)
	}

	// The declared plugin tables stay served and their counts unchanged, and
	// the orphan table's rows are still physically there, untouched.
	if err := m.mustRun(
		"roca exec \"SELECT count(*) AS n FROM plugin_roca_corpus.memories\" --json"); err != nil {
		t.Fatalf("served corpus query: %v\n%s", err, m.last.stderr)
	}
	if !strings.Contains(m.last.stdout, `"n": 0`) && !strings.Contains(m.last.stdout, `"n":0`) {
		t.Fatalf("the served corpus query answered something else:\n%s", m.last.stdout)
	}
	orphans, err := sql.Open("sqlite", corpusPath)
	if err != nil {
		t.Fatal(err)
	}
	var orphanRows, memoryRows int
	if err := orphans.QueryRow(`SELECT (SELECT count(*) FROM layers), (SELECT count(*) FROM memories)`).
		Scan(&orphanRows, &memoryRows); err != nil {
		orphans.Close()
		t.Fatal(err)
	}
	if err := orphans.Close(); err != nil {
		t.Fatal(err)
	}
	if orphanRows != 1 {
		t.Fatalf("the orphan table did not keep its one row: %d", orphanRows)
	}
	if memoryRows != 0 {
		t.Fatalf("corpus memories changed: %d", memoryRows)
	}

	// Ingest is the seat the corpus plugin owns; it is exactly what the #461
	// state took down, so a served ingest proves the service recovered.
	if err := m.mustRun("roca ingest --json"); err != nil {
		t.Fatalf("ingest with the orphan table in place: %v\n%s", err, m.last.stderr)
	}
	if err := m.mustRun("roca query 'what do we know about the ingest matrix' --json"); err != nil {
		t.Fatalf("service query: %v\n%s", err, m.last.stderr)
	}
}

// seedLayersOrphan opens the disposable fixture database the way the product's
// own Go fixtures do and adds the exact stray table the interrupted migrate
// left behind, with the layer-registry shape the operator's database carried.
func seedLayersOrphan(path string) error {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return err
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE layers (
		name            TEXT PRIMARY KEY,
		description     TEXT NOT NULL,
		schema_file     TEXT NOT NULL,
		access_mode     TEXT DEFAULT 'read-write',
		ingest_allowed  INTEGER DEFAULT 1,
		is_coordination INTEGER DEFAULT 0,
		search_excluded INTEGER DEFAULT 0,
		alias_of        TEXT,
		added_by        TEXT DEFAULT 'kernel',
		deprecated      INTEGER DEFAULT 0,
		lifecycle       TEXT DEFAULT 'curated',
		capabilities    TEXT DEFAULT '{}',
		since_version   TEXT
	)`); err != nil {
		return err
	}
	_, err = db.Exec(`INSERT INTO layers (name, description, schema_file)
		VALUES ('knowledge', '#461 fixture layer', '')`)
	return err
}
