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
	if got.Plugin != definition.Plugin || got.SchemaVersion != 1 || got.IndexVersion != 2 || got.State != StatePrepared {
		t.Fatalf("prepared database = %+v", got)
	}

	commitFixtureBatch(t, db, "before-verification", "1")
	if err := Verify(context.Background(), db, fixtureDigest('d')); err != nil {
		t.Fatal(err)
	}
	if err := Prepare(context.Background(), db, definition); err != nil {
		t.Fatal(err)
	}
	got = inspectState(t, db)
	if got.State != StateVerified {
		t.Fatalf("same schema replay changed state to %q", got.State)
	}

	definition.SchemaVersion++
	if err := Prepare(context.Background(), db, definition); err != nil {
		t.Fatal(err)
	}
	got = inspectState(t, db)
	if got.SchemaVersion != 2 || got.State != StatePrepared || got.VerificationDigest != "" {
		t.Fatalf("schema upgrade = %+v", got)
	}
	if err := Verify(context.Background(), db, fixtureDigest('a')); err != nil {
		t.Fatalf("committed batches cannot be verified after a schema upgrade: %v", err)
	}
}

func TestABatchNamesAnAbsentLedgerAndLeavesForeignKeysAsItFoundThem(t *testing.T) {
	db := openTestDatabase(t)
	_, err := BeginBatch(context.Background(), db, BatchSpec{
		ID: "core-rows-0001", SourceDatabase: "core", SourceTable: "rows",
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
		ID: "core-memories-0001", SourceDatabase: "core", SourceTable: "memories",
	})
	if err != nil {
		t.Fatal(err)
	}
	var inProgress State
	if err := pending.tx.QueryRowContext(context.Background(),
		`SELECT migration_state FROM plugin_schema WHERE singleton = 1`).Scan(&inProgress); err != nil {
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
	state := inspectState(t, db)
	if state.State != StatePrepared {
		t.Fatalf("state after interruption = %q", state.State)
	}

	resumed, err := BeginBatch(context.Background(), db, BatchSpec{
		ID: "core-memories-0001", SourceDatabase: "core", SourceTable: "memories",
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
	state = inspectState(t, db)
	if state.State != StateBatchInProgress {
		t.Fatalf("state after committed batch = %q", state.State)
	}
	if _, err := BeginBatch(context.Background(), db, BatchSpec{
		ID: "core-memories-0001", SourceDatabase: "core", SourceTable: "memories",
	}); !errors.Is(err, ErrBatchCommitted) {
		t.Fatalf("committed batch replay error = %v", err)
	}
}

func TestOnlyVerifiedDatabasesAreCutoverEligible(t *testing.T) {
	db := openTestDatabase(t)
	if err := Prepare(context.Background(), db, Definition{
		Plugin: "synthetic", SchemaVersion: 1, IndexVersion: 0,
	}); err != nil {
		t.Fatal(err)
	}
	eligible, err := CutoverEligible(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if eligible {
		t.Fatal("a merely prepared database is cutover eligible")
	}
	if err := Verify(context.Background(), db, fixtureDigest('c')); err == nil {
		t.Fatal("a database with no committed batch was verified")
	}
	commitFixtureBatch(t, db, "core-rows-0001", "1")
	if err := Verify(context.Background(), db, fixtureDigest('c')); err != nil {
		t.Fatal(err)
	}
	eligible, err = CutoverEligible(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if !eligible {
		t.Fatal("a verified database is not cutover eligible")
	}
	if _, err := BeginBatch(context.Background(), db, BatchSpec{
		ID: "late", SourceDatabase: "core", SourceTable: "rows",
	}); !errors.Is(err, ErrVerified) {
		t.Fatalf("batch after verification error = %v", err)
	}
}

func TestAnEmptyMigrationVerifiesPerDestinationAndStaysCutoverReady(t *testing.T) {
	db := openTestDatabase(t)
	if err := Prepare(context.Background(), db, Definition{
		Plugin: "synthetic", SchemaVersion: 1, IndexVersion: 0,
	}); err != nil {
		t.Fatal(err)
	}
	if err := VerifyEmpty(context.Background(), db, fixtureDigest('a'), "shadow_rows"); err != nil {
		t.Fatal(err)
	}
	if state := inspectState(t, db); state.State != StateVerifiedEmpty {
		t.Fatalf("state after an empty verification = %q", state.State)
	}
	eligible, err := CutoverEligible(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if !eligible {
		t.Fatal("a verified-empty database is not cutover eligible")
	}

	commitFixtureBatch(t, db, "core-rows-0001", "1")
	if err := VerifyEmpty(context.Background(), db, fixtureDigest('b'), "shadow_rows"); err != nil {
		t.Fatalf("another destination's committed batch blocked an empty verification: %v", err)
	}
	if err := VerifyEmpty(context.Background(), db, fixtureDigest('c'), "rows"); err == nil {
		t.Fatal("a destination that carried a row verified as empty")
	}
	batch, err := BeginBatch(context.Background(), db, BatchSpec{
		ID: "late-rows-0002", SourceDatabase: "core", SourceTable: "rows",
	})
	if err != nil {
		t.Fatalf("a verified-empty database refused the rows it later held: %v", err)
	}
	if err := batch.Rollback(); err != nil {
		t.Fatal(err)
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
	if state := inspectState(t, db); state.State != StatePrepared {
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
		ID: killedBatchID, SourceDatabase: "core", SourceTable: "rows",
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

func fixtureDigest(character byte) string {
	value := make([]byte, 64)
	for index := range value {
		value[index] = character
	}
	return string(value)
}

func commitFixtureBatch(t *testing.T, db *sql.DB, id, sourceKey string) {
	t.Helper()
	batch, err := BeginBatch(context.Background(), db, BatchSpec{
		ID: id, SourceDatabase: "core", SourceTable: "rows",
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
