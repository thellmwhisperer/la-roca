//go:build acceptance

package acceptance

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Called only through TestCostAuditDestination and its explicit published control.
func checkAuditStorageUpgrade(t *testing.T, branch, published string) {
	m := aWorldIn(t, "audit-storage")
	m.binary = published
	run := func(args ...string) string {
		t.Helper()
		output, code := m.runUnder(t, nil, args...)
		t.Logf("$ roca %s\n%s[exit %d]", strings.ReplaceAll(strings.Join(args, " "), m.home, "~"), strings.ReplaceAll(output, m.home, "~"), code)
		if code != 0 {
			t.Fatalf("CLI failed: %s", output)
		}
		return output
	}
	oldVersion := run("--version")
	run("init", "--db-path", filepath.Join(m.home, ".roca", "roca.db"), "--json")
	path := filepath.Join(m.home, ".roca", "plugins", "roca-ops", "roca-ops.db")
	open := func(path string) *sql.DB {
		t.Helper()
		db, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		return db
	}
	db := open(path)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	scalar := func(query string) int64 {
		t.Helper()
		var n int64
		if err := db.QueryRow(query).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if scalar(`SELECT schema_version FROM plugin_schema`) != 4 {
		t.Fatal("control must have pre-removal schema 4")
	}
	exec(`INSERT INTO memories(layer,content,origin) VALUES('handoff','synthetic amber sentinel','agent');
  INSERT INTO layers(name,description,schema_file) VALUES('synthetic-operator','preserved','synthetic.schema');`)
	db.Close()
	run("migrate", "--json")
	db = open(path)
	memberships := scalar(`SELECT count(*) FROM custody_memberships`)
	if memberships == 0 || scalar(`SELECT count(*) FROM plugin_migrations WHERE migration_state = 'verified'`) == 0 {
		t.Fatal("published fixture did not verify populated custody")
	}
	// About 14 MiB of realistic redacted JSON, with duplicated extracted fields
	// and the published indexes. Only a smaller recent JSONL subset is retained.
	payload, err := json.Marshal(map[string]any{"source": "cli", "command": "exec", "args": []string{"SELECT 'synthetic audit'"}, "ok": true, "sql": strings.Repeat("synthetic audit ", 90)})
	if err != nil {
		t.Fatal(err)
	}
	exec(`WITH RECURSIVE n(i) AS (VALUES(1) UNION ALL SELECT i+1 FROM n WHERE i<10000)
  INSERT INTO call_history(id,timestamp,stream,source,operation,args,ok,duration_ms,row_count,
   retried,retried_sql,model_sql_present,source_file,source_line,record_digest,record_json)
  SELECT 'synthetic-'||i,'2026-08-01T00:00:00Z','executions','cli','exec','[]',1,1,1,
   0,0,0,'expired.jsonl',i,'synthetic-digest',? FROM n`, string(payload))
	exec(`INSERT OR REPLACE INTO call_history_segments VALUES('expired.jsonl','executions','synthetic',14000000,10000,10000,0,'2026-08-01T00:00:00Z');
  INSERT OR REPLACE INTO call_history_state VALUES(1,1,0,0,'2026-08-01T00:00:00Z')`)
	payloadBytes := scalar(`SELECT sum(length(record_json)) FROM call_history`)
	if payloadBytes < 10<<20 || payloadBytes > 20<<20 {
		t.Fatalf("payload bytes=%d", payloadBytes)
	}
	measure := func(label string) (pages, free, size int64) {
		t.Helper()
		exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
		pages, free, size = scalar(`PRAGMA page_count`), scalar(`PRAGMA freelist_count`), scalar(`PRAGMA page_size`)
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		var wal int64
		if info, err := os.Stat(path + "-wal"); err == nil {
			wal = info.Size()
		} else if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if info.Size() != pages*size || wal != 0 {
			t.Fatalf("unaccounted database/WAL bytes: %d/%d", info.Size(), wal)
		}
		t.Logf("%s pages=%d free=%d page_size=%d database_bytes=%d WAL_bytes=%d", label, pages, free, size, info.Size(), wal)
		return
	}
	beforePages, beforeFree, pageSize := measure("published populated")
	db.Close()
	// Exercise cutover readiness through the public CLI. The changed build
	// version must run Ensure's upgrade instead of its same-version fast path.
	if err := os.WriteFile(filepath.Join(m.home, ".roca", "config.toml"), []byte("[layout]\nserving = \"cutover\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	m.binary = branch
	if run("--version") == oldVersion {
		t.Fatal("upgrade needs distinct product versions")
	}
	run("migrate", "--json")
	auditRows(t, path, true)
	db = open(path)
	if scalar(`SELECT schema_version FROM plugin_schema`) != 5 || scalar(`SELECT index_version FROM plugin_schema`) != 2 ||
		scalar(`SELECT count(*) FROM plugin_migrations WHERE migration_state NOT IN ('verified','verified-empty')`) != 0 {
		t.Fatal("normal migrate did not restore verified readiness")
	}
	check := func() {
		t.Helper()
		if scalar(`SELECT count(*) FROM memories WHERE content='synthetic amber sentinel'`) != 1 ||
			scalar(`SELECT count(*) FROM memory_records_fts WHERE memory_records_fts MATCH 'amber'`) != 1 ||
			scalar(`SELECT count(*) FROM custody_memberships`) != memberships ||
			scalar(`SELECT count(*) FROM layers WHERE name='synthetic-operator' AND description='preserved' AND schema_file='synthetic.schema'`) != 1 {
			t.Fatal("upgrade changed unrelated sentinel data")
		}
		var integrity string
		if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&integrity); err != nil || integrity != "ok" {
			t.Fatalf("integrity=%s err=%v", integrity, err)
		}
	}
	check()
	afterPages, afterFree, _ := measure("branch dropped")
	freed := (beforePages - beforeFree - (afterPages - afterFree)) * pageSize
	if freed < payloadBytes {
		t.Fatalf("removed allocation %d smaller than payload %d", freed, payloadBytes)
	}
	// VACUUM only a second lab copy. The product upgrade must not auto-vacuum.
	if afterPages != beforePages {
		t.Fatal("upgrade unexpectedly compacted storage")
	}
	copyPath := filepath.Join(m.home, "vacuum-lab.db")
	db.Close()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(copyPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	path = copyPath
	db = open(path)
	defer db.Close()
	exec(`VACUUM`)
	vacuumPages, _, _ := measure("branch lab copy vacuumed")
	reclaimed := (afterPages - vacuumPages) * pageSize
	if reclaimed < freed-pageSize {
		t.Fatalf("VACUUM reclaimed %d; removed allocation=%d", reclaimed, freed)
	}
	check()
	t.Logf("synthetic rows=10000 payload_bytes=%d removed_allocation_bytes=%d reclaimed_bytes=%d integrity=ok", payloadBytes, freed, reclaimed)
	run("migrate", "--json")
	run("exec", "SELECT content FROM plugin_roca_ops.memories WHERE content = 'synthetic amber sentinel'", "--json")
	checkAuditDoctor(t, m, filepath.Join(m.home, ".roca", "plugins", "roca-ops", "roca-ops.db"))
	t.Log("audit storage upgrade: distinct versions, verified custody, JSONL doctor, isolated VACUUM passed")
}
