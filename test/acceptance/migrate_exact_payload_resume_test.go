//go:build acceptance

package acceptance

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/data"
	"github.com/thellmwhisperer/la-roca/internal/distribution/rocaops"
	"github.com/thellmwhisperer/la-roca/internal/store/payloadhash"
)

// TestMigrateResumeAliasesAMemoryTheExactPayloadGuardRefuses replays the
// v1.94.2 home: DATA-2 prepared again after a verified run, ops carrying the
// exact-payload guard on memory_records, and a new ops memory that guard calls
// a copy of an imported record although its canonical digest differs.
func TestMigrateResumeAliasesAMemoryTheExactPayloadGuardRefuses(t *testing.T) {
	m := aWorldIn(t, "migrate-exact-payload")
	roca := filepath.Join(m.home, ".roca")
	core := filepath.Join(roca, "roca.db")
	ops := filepath.Join(roca, "plugins", rocaops.Name, rocaops.DatabaseFilename)
	if err := os.MkdirAll(roca, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := rocaops.Ensure(filepath.Join(roca, "plugins"), filepath.Join(m.home, ".local", "bin"), "dev"); err != nil {
		t.Fatal(err)
	}
	exec := func(path string, statements ...string) {
		t.Helper()
		db, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		for _, statement := range statements {
			if _, err := db.Exec(statement); err != nil {
				t.Fatalf("%s: %v", statement, err)
			}
		}
	}
	opsMemory := func(createdAt string) string {
		return `INSERT INTO memories (layer, content, metadata, origin, status, created_at)
			VALUES ('handoff', 'ops handoff alpha', '{}', 'agent', 'active', '` + createdAt + `')`
	}
	exec(core, data.Schema,
		`INSERT INTO memories (layer, content, origin) VALUES ('project', 'core marker', 'agent')`)
	exec(ops, opsMemory("2026-08-04 00:00:00"))
	run := func(args ...string) string {
		t.Helper()
		out, code := m.runUnder(t, nil, args...)
		if code != 0 {
			t.Fatalf("roca %v exit=%d:\n%s", args, code, out)
		}
		return out
	}
	if out := run("migrate"); !strings.Contains(out, "migration: verified") {
		t.Fatalf("first migrate:\n%s", out)
	}

	var parts []string
	for _, name := range strings.Fields(`provenance layer content metadata origin source_agent
		source_model source_surface source_session source_sequence project status supersedes expires_at`) {
		parts = append(parts, "typeof("+name+")",
			"CASE WHEN typeof("+name+") = 'text' THEN CAST("+name+" AS BLOB) ELSE "+name+" END")
	}
	// A plugin update reopens DATA-2 under a generation with no ops snapshot yet.
	exec(ops, "CREATE UNIQUE INDEX idx_memory_records_exact_payload ON memory_records("+
		payloadhash.SQLFunc+"("+strings.Join(parts, ",")+"))",
		opsMemory("2026-09-01 00:00:00"),
		`UPDATE plugin_migrations SET migration_state = 'prepared' WHERE migration = 'data2-memory-custody'`)
	snapshots, err := filepath.Glob(filepath.Join(roca, "backups", "data-split", ".plugin-roca-ops-*.snapshot.db"))
	if err != nil || len(snapshots) != 1 {
		t.Fatalf("ops memory snapshots = %v, %v", snapshots, err)
	}
	if err := os.Remove(snapshots[0]); err != nil {
		t.Fatal(err)
	}

	if out := run("migrate"); !strings.Contains(out, "migration: verified") {
		t.Fatalf("resumed migrate:\n%s", out)
	}
	if out := run("migrate", "--status", "--read-only"); !strings.Contains(out,
		"data2-memory-custody state=verified destination=memory_records memberships=3") {
		t.Fatalf("status after the resumed migrate:\n%s", out)
	}
	db, err := sql.Open("sqlite", ops)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var records, destinations int
	if err := db.QueryRow(`SELECT (SELECT COUNT(*) FROM memory_records),
		(SELECT COUNT(DISTINCT destination_key) FROM custody_memberships
		 WHERE migration = 'data2-memory-custody' AND source_database = 'plugin:roca-ops')`).
		Scan(&records, &destinations); err != nil {
		t.Fatal(err)
	}
	if records != 2 || destinations != 1 {
		t.Fatalf("memory_records = %d, ops destinations = %d; want the two ops memories on one record beside core's", records, destinations)
	}
}
