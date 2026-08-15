package migrationledger

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

const (
	killedBatchDatabase = "ROCA_LEDGER_KILLED_BATCH_DATABASE"
	killedBatchID       = "core-rows-0001"
	// rowsMigration and destinationMigration are two named migrations hosted by
	// one synthetic plugin database, each owning its own destination.
	rowsMigration        = "synthetic-rows"
	destinationMigration = "synthetic-destination"
)

func TestPrepareIsIdempotentAndASchemaUpgradeReturnsToPrepared(t *testing.T) {
	db := openTestDatabase(t)
	definition := Definition{Plugin: "synthetic", SchemaVersion: 1, IndexVersion: 2}
	if err := Prepare(context.Background(), db, definition); err != nil {
		t.Fatal(err)
	}
	if err := Prepare(context.Background(), db, definition); err != nil {
		t.Fatal(err)
	}

	got := inspectState(t, db)
	if got.Plugin != definition.Plugin || got.SchemaVersion != 1 || got.IndexVersion != 2 {
		t.Fatalf("prepared database = %+v", got)
	}

	commitFixtureBatch(t, db, "before-verification", "1")
	if err := VerifyMigration(context.Background(), db, rowsMigration, fixtureDigest('d')); err != nil {
		t.Fatal(err)
	}
	if err := Prepare(context.Background(), db, definition); err != nil {
		t.Fatal(err)
	}
	if carried := inspectMigrationState(t, db, rowsMigration); carried.State != StateVerified {
		t.Fatalf("same schema replay changed state to %q", carried.State)
	}

	definition.SchemaVersion++
	if err := Prepare(context.Background(), db, definition); err != nil {
		t.Fatal(err)
	}
	got = inspectState(t, db)
	reopened := inspectMigrationState(t, db, rowsMigration)
	if got.SchemaVersion != 2 || reopened.State != StatePrepared || reopened.VerificationDigest != "" {
		t.Fatalf("schema upgrade = %+v / %+v", got, reopened)
	}
	if err := VerifyMigration(context.Background(), db, rowsMigration, fixtureDigest('a')); err != nil {
		t.Fatalf("committed batches cannot be verified after a schema upgrade: %v", err)
	}
}

func TestABatchNamesAnAbsentLedgerAndLeavesForeignKeysAsItFoundThem(t *testing.T) {
	db := openTestDatabase(t)
	_, err := BeginBatch(context.Background(), db, BatchSpec{
		Migration: rowsMigration, ID: "core-rows-0001",
		SourceDatabase: "core", SourceTable: "rows",
	})
	if err == nil || !strings.Contains(err.Error(), "plugin migration ledger is absent") {
		t.Fatalf("batch against a database without a ledger = %v", err)
	}
	if err := Prepare(context.Background(), db, Definition{
		Plugin: "synthetic", SchemaVersion: 1, IndexVersion: 0,
	}); err != nil {
		t.Fatal(err)
	}
	commitFixtureBatch(t, db, "core-rows-0001", "1")

	var enforced int
	if err := db.QueryRow("PRAGMA foreign_keys").Scan(&enforced); err != nil {
		t.Fatal(err)
	}
	if enforced != 0 {
		t.Fatal("a committed batch left foreign key enforcement on the pooled connection")
	}
}

func TestInterruptedBatchIsAbsentAndTheSameBatchResumes(t *testing.T) {
	db := openTestDatabase(t)
	prepareWithDestination(t, db)

	pending, err := BeginBatch(context.Background(), db, BatchSpec{
		Migration: destinationMigration, ID: "core-memories-0001",
		SourceDatabase: "core", SourceTable: "memories",
	})
	if err != nil {
		t.Fatal(err)
	}
	var inProgress State
	if err := pending.tx.QueryRowContext(context.Background(),
		`SELECT migration_state FROM plugin_migrations WHERE migration = ?`,
		destinationMigration).Scan(&inProgress); err != nil {
		t.Fatal(err)
	}
	if inProgress != StateBatchInProgress {
		t.Fatalf("transactional state = %q", inProgress)
	}
	if _, err := pending.ExecContext(context.Background(),
		`INSERT INTO destination_rows VALUES ('20', 'interrupted')`); err != nil {
		t.Fatal(err)
	}
	if err := pending.AddMembership(context.Background(), Membership{
		SourceKey: "10", DestinationTable: "destination_rows", DestinationKey: "20",
		CanonicalDigest: fixtureDigest('a'),
	}); err != nil {
		t.Fatal(err)
	}
	if err := pending.Rollback(); err != nil {
		t.Fatal(err)
	}

	assertCount(t, db, "migration_batches", 0)
	assertCount(t, db, "custody_memberships", 0)
	assertCount(t, db, "destination_rows", 0)
	state := inspectMigrationState(t, db, destinationMigration)
	if state.State != StatePrepared {
		t.Fatalf("state after interruption = %q", state.State)
	}

	resumed, err := BeginBatch(context.Background(), db, BatchSpec{
		Migration: destinationMigration, ID: "core-memories-0001",
		SourceDatabase: "core", SourceTable: "memories",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resumed.ExecContext(context.Background(),
		`INSERT INTO destination_rows VALUES ('20', 'committed')`); err != nil {
		t.Fatal(err)
	}
	if err := resumed.AddMembership(context.Background(), Membership{
		SourceKey: "10", DestinationTable: "destination_rows", DestinationKey: "20",
		CanonicalDigest: fixtureDigest('a'),
	}); err != nil {
		t.Fatal(err)
	}
	if err := resumed.Commit(context.Background(), BatchCommit{
		RowCount: 1, CanonicalDigest: fixtureDigest('b'), HighWaterMark: "10",
	}); err != nil {
		t.Fatal(err)
	}

	assertCount(t, db, "migration_batches", 1)
	assertCount(t, db, "custody_memberships", 1)
	assertCount(t, db, "destination_rows", 1)
	state = inspectMigrationState(t, db, destinationMigration)
	if state.State != StateBatchInProgress {
		t.Fatalf("state after committed batch = %q", state.State)
	}
	if _, err := BeginBatch(context.Background(), db, BatchSpec{
		Migration: destinationMigration, ID: "core-memories-0001",
		SourceDatabase: "core", SourceTable: "memories",
	}); !errors.Is(err, ErrBatchCommitted) {
		t.Fatalf("committed batch replay error = %v", err)
	}
	record, found, err := LookupBatch(context.Background(), db, destinationMigration, "core-memories-0001")
	if err != nil {
		t.Fatal(err)
	}
	if !found || record.SourceDatabase != "core" || record.SourceTable != "memories" ||
		record.RowCount != 1 || record.CanonicalDigest != fixtureDigest('b') || record.HighWaterMark != "10" {
		t.Fatalf("committed batch = %+v, found = %t", record, found)
	}
	if _, found, err := LookupBatch(context.Background(), db, destinationMigration, "absent"); err != nil || found {
		t.Fatalf("absent batch found = %t, error = %v", found, err)
	}
}

func TestOnlyVerifiedDatabasesAreCutoverEligible(t *testing.T) {
	db := openTestDatabase(t)
	if err := Prepare(context.Background(), db, Definition{
		Plugin: "synthetic", SchemaVersion: 1, IndexVersion: 0,
	}); err != nil {
		t.Fatal(err)
	}
	eligible, err := MigrationCutoverEligible(context.Background(), db, rowsMigration)
	if err != nil {
		t.Fatal(err)
	}
	if eligible {
		t.Fatal("a merely prepared database is cutover eligible")
	}
	if err := VerifyMigration(context.Background(), db, rowsMigration, fixtureDigest('c')); err == nil {
		t.Fatal("a database with no committed batch was verified")
	}
	commitFixtureBatch(t, db, "core-rows-0001", "1")
	if err := VerifyMigration(context.Background(), db, rowsMigration, fixtureDigest('c')); err != nil {
		t.Fatal(err)
	}
	eligible, err = MigrationCutoverEligible(context.Background(), db, rowsMigration)
	if err != nil {
		t.Fatal(err)
	}
	if !eligible {
		t.Fatal("a verified database is not cutover eligible")
	}
	if _, err := BeginBatch(context.Background(), db, BatchSpec{
		Migration: rowsMigration, ID: "late", SourceDatabase: "core", SourceTable: "rows",
	}); !errors.Is(err, ErrVerified) {
		t.Fatalf("batch after verification error = %v", err)
	}
}

// TestTwoNamedMigrationsAdvanceIndependently pins the boundary the ledger now
// draws: one plugin database hosts several named migrations, and each one's
// lifecycle, batches and verification belong to it alone.
func TestTwoNamedMigrationsAdvanceIndependently(t *testing.T) {
	db := openTestDatabase(t)
	prepareWithDestination(t, db)
	prepareMigration(t, db, rowsMigration, "rows")

	commitFixtureBatch(t, db, "core-rows-0001", "1")
	if err := VerifyMigration(context.Background(), db, rowsMigration, fixtureDigest('c')); err != nil {
		t.Fatal(err)
	}
	if err := VerifyMigration(context.Background(), db, destinationMigration, fixtureDigest('d')); err == nil {
		t.Fatal("a migration was verified by a sibling's committed batch")
	}
	if err := VerifyMigrationEmpty(context.Background(), db, destinationMigration, fixtureDigest('a')); err != nil {
		t.Fatalf("a sibling's committed batch blocked an empty verification: %v", err)
	}
	if err := VerifyMigrationEmpty(context.Background(), db, rowsMigration, fixtureDigest('b')); err == nil {
		t.Fatal("a migration that carried a row verified as empty")
	}

	carried := inspectMigrationState(t, db, rowsMigration)
	empty := inspectMigrationState(t, db, destinationMigration)
	if carried.State != StateVerified || empty.State != StateVerifiedEmpty {
		t.Fatalf("simultaneous states = %q and %q", carried.State, empty.State)
	}
	for _, name := range []string{rowsMigration, destinationMigration} {
		eligible, err := MigrationCutoverEligible(context.Background(), db, name)
		if err != nil {
			t.Fatal(err)
		}
		if !eligible {
			t.Fatalf("verified migration %q is not cutover eligible", name)
		}
	}

	if _, err := BeginBatch(context.Background(), db, BatchSpec{
		Migration: rowsMigration, ID: "later-rows", SourceDatabase: "core", SourceTable: "rows",
	}); !errors.Is(err, ErrVerified) {
		t.Fatalf("a sealed migration accepted another batch: %v", err)
	}
	reopened, err := BeginBatch(context.Background(), db, BatchSpec{
		Migration: destinationMigration, ID: "core-rows-0001",
		SourceDatabase: "core", SourceTable: "memories",
	})
	if err != nil {
		t.Fatalf("a sibling's batch id closed a verified-empty migration: %v", err)
	}
	if _, err := reopened.ExecContext(context.Background(),
		`INSERT INTO destination_rows VALUES ('20', 'carried')`); err != nil {
		t.Fatal(err)
	}
	if err := reopened.AddMembership(context.Background(), Membership{
		SourceKey: "1", DestinationTable: "destination_rows", DestinationKey: "20",
		CanonicalDigest: fixtureDigest('a'),
	}); err != nil {
		t.Fatal(err)
	}
	if err := reopened.Commit(context.Background(), BatchCommit{
		RowCount: 1, CanonicalDigest: fixtureDigest('b'), HighWaterMark: "1",
	}); err != nil {
		t.Fatalf("a sibling's batch id blocked the commit of an identically named batch: %v", err)
	}

	assertCount(t, db, "migration_batches", 2)
	assertCount(t, db, "custody_memberships", 2)
	for _, owner := range []struct {
		migration string
		state     State
	}{{rowsMigration, StateVerified}, {destinationMigration, StateBatchInProgress}} {
		var batches, memberships int
		if err := db.QueryRow(`SELECT COUNT(*) FROM migration_batches
			WHERE migration = ? AND batch_id = 'core-rows-0001'`, owner.migration).Scan(&batches); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow(`SELECT COUNT(*) FROM custody_memberships
			WHERE migration = ?`, owner.migration).Scan(&memberships); err != nil {
			t.Fatal(err)
		}
		got := inspectMigrationState(t, db, owner.migration)
		if batches != 1 || memberships != 1 || got.State != owner.state {
			t.Fatalf("%s holds %d batches and %d memberships in state %q, want 1/1/%q",
				owner.migration, batches, memberships, got.State, owner.state)
		}
	}
}

// TestAMigrationCannotWriteIntoAnotherDestination keeps destination ownership
// enforced rather than merely declared.
func TestAMigrationCannotWriteIntoAnotherDestination(t *testing.T) {
	db := openTestDatabase(t)
	prepareWithDestination(t, db)
	batch, err := BeginBatch(context.Background(), db, BatchSpec{
		Migration: destinationMigration, ID: "trespass-0001",
		SourceDatabase: "core", SourceTable: "memories",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer batch.Rollback()
	err = batch.AddMembership(context.Background(), Membership{
		SourceKey: "1", DestinationTable: "rows", DestinationKey: "1",
		CanonicalDigest: fixtureDigest('a'),
	})
	if err == nil || !strings.Contains(err.Error(), "owns destination") {
		t.Fatalf("membership into another migration's destination = %v", err)
	}
}

// TestAKilledProcessResumesTheSameBatch interrupts a batch the way a machine
// does, by killing the process that holds it open, so the resume path is proven
// against a database no rollback call ever reached.
func TestAKilledProcessResumesTheSameBatch(t *testing.T) {
	if path := os.Getenv(killedBatchDatabase); path != "" {
		killedBatchChild(path)
		return
	}
	path := filepath.Join(t.TempDir(), "plugin.db")
	db := openDatabaseAt(t, path)
	prepareWithDestination(t, db)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	child := exec.Command(os.Args[0], "-test.run=^TestAKilledProcessResumesTheSameBatch$")
	child.Env = append(os.Environ(), killedBatchDatabase+"="+path)
	output, err := child.CombinedOutput()
	// Exit code 1 is the child dying inside the open batch; anything else means
	// it never got that far, and the resume below would prove nothing.
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 {
		t.Fatalf("child interruption = %v: %s", err, output)
	}

	db = openDatabaseAt(t, path)
	assertCount(t, db, "migration_batches", 0)
	assertCount(t, db, "custody_memberships", 0)
	assertCount(t, db, "destination_rows", 0)
	if state := inspectMigrationState(t, db, destinationMigration); state.State != StatePrepared {
		t.Fatalf("state after a killed batch = %q", state.State)
	}
	commitFixtureBatch(t, db, killedBatchID, "1")
	assertCount(t, db, "migration_batches", 1)
	assertCount(t, db, "custody_memberships", 1)
}

// killedBatchChild opens a batch, writes into it and dies without committing.
func killedBatchChild(path string) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		os.Exit(2)
	}
	batch, err := BeginBatch(context.Background(), db, BatchSpec{
		Migration: destinationMigration, ID: killedBatchID,
		SourceDatabase: "core", SourceTable: "rows",
	})
	if err != nil {
		os.Exit(2)
	}
	if _, err := batch.ExecContext(context.Background(),
		`INSERT INTO destination_rows VALUES ('20', 'interrupted')`); err != nil {
		os.Exit(2)
	}
	if err := batch.AddMembership(context.Background(), Membership{
		SourceKey: "1", DestinationTable: "destination_rows", DestinationKey: "20",
		CanonicalDigest: fixtureDigest('a'),
	}); err != nil {
		os.Exit(2)
	}
	os.Exit(1)
}

func openTestDatabase(t *testing.T) *sql.DB {
	t.Helper()
	return openDatabaseAt(t, filepath.Join(t.TempDir(), "plugin.db"))
}

func openDatabaseAt(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// prepareWithDestination prepares a synthetic ledger and creates the table the
// batch tests write their rows into.
func prepareWithDestination(t *testing.T, db *sql.DB) {
	t.Helper()
	if err := Prepare(context.Background(), db, Definition{
		Plugin: "synthetic", SchemaVersion: 1, IndexVersion: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE destination_rows (id TEXT PRIMARY KEY, payload TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	prepareMigration(t, db, destinationMigration, "destination_rows")
}

func prepareMigration(t *testing.T, db *sql.DB, name, destination string) {
	t.Helper()
	if err := PrepareMigration(context.Background(), db, Migration{
		Name: name, DestinationTable: destination,
	}); err != nil {
		t.Fatal(err)
	}
}

func assertCount(t *testing.T, db *sql.DB, table string, want int) {
	t.Helper()
	var got int
	if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("%s rows = %d, want %d", table, got, want)
	}
}

func inspectState(t *testing.T, db *sql.DB) Snapshot {
	t.Helper()
	state, err := Inspect(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func inspectMigrationState(t *testing.T, db *sql.DB, name string) MigrationSnapshot {
	t.Helper()
	state, err := InspectMigration(context.Background(), db, name)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func fixtureDigest(character byte) string {
	value := make([]byte, 64)
	for index := range value {
		value[index] = character
	}
	return string(value)
}

func commitFixtureBatch(t *testing.T, db *sql.DB, id, sourceKey string) {
	t.Helper()
	prepareMigration(t, db, rowsMigration, "rows")
	batch, err := BeginBatch(context.Background(), db, BatchSpec{
		Migration: rowsMigration, ID: id, SourceDatabase: "core", SourceTable: "rows",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := batch.AddMembership(context.Background(), Membership{
		SourceKey: sourceKey, DestinationTable: "rows", DestinationKey: sourceKey,
		CanonicalDigest: fixtureDigest('e'),
	}); err != nil {
		t.Fatal(err)
	}
	if err := batch.Commit(context.Background(), BatchCommit{
		RowCount: 1, CanonicalDigest: fixtureDigest('f'), HighWaterMark: sourceKey,
	}); err != nil {
		t.Fatal(err)
	}
}

// legacySchema is the DATA-1 shape: no named migrations, and a membership
// primary key that cannot tell two migrations apart.
const legacySchema = `
CREATE TABLE plugin_schema (
  singleton           INTEGER PRIMARY KEY CHECK (singleton = 1),
  plugin_name         TEXT NOT NULL UNIQUE,
  schema_version      INTEGER NOT NULL CHECK (schema_version > 0),
  index_version       INTEGER NOT NULL CHECK (index_version >= 0),
  migration_state     TEXT NOT NULL CHECK (migration_state IN ('prepared', 'batch-in-progress', 'verified')),
  verification_digest TEXT,
  prepared_at         TEXT NOT NULL DEFAULT (datetime('now')),
  verified_at         TEXT,
  updated_at          TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE TABLE migration_batches (
  batch_id            TEXT PRIMARY KEY,
  source_database     TEXT NOT NULL,
  source_table        TEXT NOT NULL,
  row_count           INTEGER NOT NULL CHECK (row_count >= 0),
  canonical_digest    TEXT NOT NULL,
  high_water_mark     TEXT NOT NULL,
  committed_at        TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE TABLE custody_memberships (
  source_database     TEXT NOT NULL,
  source_table        TEXT NOT NULL,
  source_key          TEXT NOT NULL,
  destination_table   TEXT NOT NULL,
  destination_key     TEXT NOT NULL,
  canonical_digest    TEXT NOT NULL,
  batch_id            TEXT NOT NULL REFERENCES migration_batches(batch_id)
                        DEFERRABLE INITIALLY DEFERRED,
  PRIMARY KEY (source_database, source_table, source_key)
);
CREATE INDEX custody_memberships_destination
  ON custody_memberships(destination_table, destination_key);
`

// TestALegacyLedgerAdoptsNamedMigrationsIdempotently proves a DATA-1 database
// grows the named-migration boundary in place, keeps the rows it already held,
// and never lets them stand in for a migration that has not run.
func TestALegacyLedgerAdoptsNamedMigrationsIdempotently(t *testing.T) {
	db := openTestDatabase(t)
	statements := []string{
		legacySchema,
		`INSERT INTO plugin_schema (singleton, plugin_name, schema_version, index_version, migration_state)
			VALUES (1, 'synthetic', 1, 0, 'prepared')`,
		`INSERT INTO migration_batches
			(batch_id, source_database, source_table, row_count, canonical_digest, high_water_mark)
			VALUES ('legacy-0001', 'core', 'rows', 1, '` + strings.Repeat("e", 64) + `', '1')`,
		`INSERT INTO custody_memberships
			(source_database, source_table, source_key, destination_table, destination_key,
			 canonical_digest, batch_id)
			VALUES ('core', 'rows', '1', 'rows', '1', '` + strings.Repeat("e", 64) + `', 'legacy-0001')`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}

	definition := Definition{Plugin: "synthetic", SchemaVersion: 1, IndexVersion: 0}
	for range 2 {
		if err := Prepare(context.Background(), db, definition); err != nil {
			t.Fatal(err)
		}
	}
	assertCount(t, db, "migration_batches", 1)
	assertCount(t, db, "custody_memberships", 1)
	var carried string
	if err := db.QueryRow(`SELECT migration FROM custody_memberships`).Scan(&carried); err != nil {
		t.Fatal(err)
	}
	if carried != "" {
		t.Fatalf("adopted membership belongs to %q, want the unclaimed name", carried)
	}

	prepareMigration(t, db, rowsMigration, "rows")
	if err := VerifyMigration(context.Background(), db, rowsMigration, fixtureDigest('c')); err == nil {
		t.Fatal("an adopted legacy batch verified a migration that never ran")
	}
	commitFixtureBatch(t, db, "core-rows-0001", "1")
	if err := VerifyMigration(context.Background(), db, rowsMigration, fixtureDigest('c')); err != nil {
		t.Fatal(err)
	}
	assertCount(t, db, "custody_memberships", 2)
}
