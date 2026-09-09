package rocaops

import (
	"path/filepath"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/distribution/migrationledger"
)

func TestAuditRemovalPreservesCustodyAndRetriesLedgerFailure(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "populated", true: "failure-retry"}[fail], func(t *testing.T) {
			fixture := smallCustodyFixture(t)
			options := MemoryCustodyOptions{CorePath: fixture.core, CorpusPath: fixture.corpus,
				OpsPath: fixture.ops, SnapshotDir: fixture.snapshots, LockPath: filepath.Join(t.TempDir(), "lock")}
			before, err := MigrateMemoryCustody(t.Context(), options)
			if err != nil {
				t.Fatal(err)
			}
			db := openCustodyDB(t, fixture.ops)
			defer db.Close()
			exec := func(query string) {
				t.Helper()
				if _, err := db.Exec(query); err != nil {
					t.Fatal(err)
				}
			}
			// Minimal historical audit shape; the paired CLI cost lab uses the exact
			// published schema. Operator state is deliberately outside the ops schema.
			exec(`UPDATE plugin_schema SET schema_version = 4;
    CREATE TABLE call_history(id TEXT PRIMARY KEY, record_json TEXT);
    CREATE INDEX idx_call_history_timestamp ON call_history(record_json);
    INSERT INTO call_history VALUES('expired', '{}');
    CREATE TABLE call_history_segments(source_file TEXT PRIMARY KEY);
    INSERT INTO call_history_segments VALUES('expired.jsonl');
    CREATE TABLE call_history_state(singleton INTEGER PRIMARY KEY);
    INSERT INTO call_history_state VALUES(1);
    CREATE TABLE operator_state(value TEXT);
    INSERT INTO operator_state VALUES('preserved');`)
			if fail {
				exec(`CREATE TRIGGER fail_audit_upgrade BEFORE UPDATE ON plugin_schema
     BEGIN SELECT RAISE(ABORT, 'synthetic ledger failure'); END`)
				if err := ApplySchema(fixture.ops); err == nil {
					t.Fatal("expected ledger failure")
				}
				assertCustodyCount(t, db, `SELECT count(*) FROM sqlite_master WHERE name GLOB '*call_history*'`, 0)
				assertCustodyCount(t, db, `SELECT schema_version FROM plugin_schema`, 4)
				exec(`DROP TRIGGER fail_audit_upgrade`)
			}
			for range 2 {
				if err := ApplySchema(fixture.ops); err != nil {
					t.Fatal(err)
				}
				assertCustodyCount(t, db, `SELECT count(*) FROM sqlite_master WHERE name GLOB '*call_history*'`, 0)
				assertCustodyCount(t, db, `SELECT schema_version FROM plugin_schema`, SchemaVersion)
				assertCustodyCount(t, db, `SELECT index_version FROM plugin_schema`, IndexVersion)
				assertCustodyCount(t, db, `SELECT count(*) FROM plugin_migrations WHERE migration_state <> 'prepared' OR verification_digest IS NOT NULL`, 0)
				assertCustodyCount(t, db, `SELECT count(*) FROM operator_state WHERE value = 'preserved'`, 1)
				assertCustodyCount(t, db, `SELECT count(*) FROM custody_memberships`, int(before.Memberships))
				assertCustodyCount(t, db, `SELECT count(*) FROM memory_records`, int(before.PhysicalRecords))
			}
			after, err := MigrateMemoryCustody(t.Context(), options)
			if err != nil {
				t.Fatal(err)
			}
			if after.State != migrationledger.StateVerified || after.VerificationDigest != before.VerificationDigest ||
				after.Memberships != before.Memberships || after.FTSRecords != before.FTSRecords {
				t.Fatalf("custody changed: before=%+v after=%+v", before, after)
			}
			var integrity string
			if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&integrity); err != nil || integrity != "ok" {
				t.Fatalf("integrity=%s err=%v", integrity, err)
			}
		})
	}
}
