// Package migrationledger owns the transaction boundary that makes plugin
// custody migrations resumable without introducing a kernel database.
package migrationledger

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

type State string

const (
	StateAbsent          State = "absent"
	StatePrepared        State = "prepared"
	StateBatchInProgress State = "batch-in-progress"
	StateVerified        State = "verified"
	// StateVerifiedEmpty reports a migration that verified with nothing to
	// carry. Unlike StateVerified it is not terminal: a home whose sources are
	// empty today may hold rows tomorrow, so BeginBatch reopens it.
	StateVerifiedEmpty State = "verified-empty"
)

var (
	ErrBatchCommitted = errors.New("migration batch is already committed")
	ErrVerified       = errors.New("verified migration cannot accept another batch")
	identifier        = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	tableIdentifier   = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	hexDigest         = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

type Definition struct {
	Plugin        string
	SchemaVersion int
	IndexVersion  int
}

// Snapshot is the plugin database's own identity. It deliberately carries no
// migration state: since named migrations, `plugin_migrations` is the only
// authoritative lifecycle, and InspectMigration is how a caller reads it.
type Snapshot struct {
	Plugin        string
	SchemaVersion int
	IndexVersion  int
}

// Migration names one custody migration inside a plugin database and the single
// destination it owns. A plugin database hosts as many as it needs: the ledger
// keys every lifecycle transition, batch and verification by this name, so two
// migrations in one database advance, resume and verify without reading or
// overwriting each other's state.
type Migration struct {
	Name             string
	DestinationTable string
}

type MigrationSnapshot struct {
	Name               string
	DestinationTable   string
	State              State
	VerificationDigest string
}

type BatchSpec struct {
	Migration      string
	ID             string
	SourceDatabase string
	SourceTable    string
}

type Membership struct {
	SourceKey        string
	DestinationTable string
	DestinationKey   string
	CanonicalDigest  string
}

type BatchCommit struct {
	RowCount        int
	CanonicalDigest string
	HighWaterMark   string
}

type CommittedBatch struct {
	BatchSpec
	BatchCommit
}

type Batch struct {
	conn        *sql.Conn
	tx          *sql.Tx
	spec        BatchSpec
	destination string
	foreignKeys int
	done        bool
}

// pluginTables hold the plugin's own identity. `plugin_schema.migration_state`
// and its verification columns are legacy: DATA-1 kept one lifecycle per plugin
// database there, and the columns survive only for the databases that already
// have them. `plugin_migrations` is where a migration's state actually lives.
const pluginTables = `
CREATE TABLE IF NOT EXISTS plugin_schema (
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

CREATE TABLE IF NOT EXISTS plugin_migrations (
  migration           TEXT PRIMARY KEY,
  destination_table   TEXT NOT NULL,
  migration_state     TEXT NOT NULL CHECK (migration_state IN
                        ('prepared', 'batch-in-progress', 'verified', 'verified-empty')),
  verification_digest TEXT,
  prepared_at         TEXT NOT NULL DEFAULT (datetime('now')),
  verified_at         TEXT,
  updated_at          TEXT NOT NULL DEFAULT (datetime('now'))
);
`

// custodyTables key a batch by the migration that carried it, not by its id
// alone, so two migrations in one database may number their batches however
// they like without one being mistaken for the other.
const custodyTables = `
CREATE TABLE IF NOT EXISTS migration_batches (
  migration           TEXT NOT NULL DEFAULT '',
  batch_id            TEXT NOT NULL,
  destination_table   TEXT NOT NULL DEFAULT '',
  source_database     TEXT NOT NULL,
  source_table        TEXT NOT NULL,
  row_count           INTEGER NOT NULL CHECK (row_count >= 0),
  canonical_digest    TEXT NOT NULL,
  high_water_mark     TEXT NOT NULL,
  committed_at        TEXT NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (migration, batch_id)
);

CREATE TABLE IF NOT EXISTS custody_memberships (
  migration           TEXT NOT NULL DEFAULT '',
  source_database     TEXT NOT NULL,
  source_table        TEXT NOT NULL,
  source_key          TEXT NOT NULL,
  destination_table   TEXT NOT NULL,
  destination_key     TEXT NOT NULL,
  canonical_digest    TEXT NOT NULL,
  batch_id            TEXT NOT NULL,
  PRIMARY KEY (migration, source_database, source_table, source_key),
  FOREIGN KEY (migration, batch_id) REFERENCES migration_batches(migration, batch_id)
    DEFERRABLE INITIALLY DEFERRED
);
`

const tables = pluginTables + custodyTables

const indexes = `
CREATE INDEX IF NOT EXISTS migration_batches_migration
  ON migration_batches(migration);
CREATE INDEX IF NOT EXISTS custody_memberships_destination
  ON custody_memberships(destination_table, destination_key);
CREATE INDEX IF NOT EXISTS custody_memberships_digest
  ON custody_memberships(canonical_digest);
CREATE INDEX IF NOT EXISTS custody_memberships_batch
  ON custody_memberships(migration, batch_id);
CREATE INDEX IF NOT EXISTS custody_memberships_migration
  ON custody_memberships(migration);
`

// batchKeyColumns and membershipKeyColumns are how many columns each custody
// table's primary key carries once it is keyed by migration. A table that
// reports anything else predates named migrations and still owes adoption.
const (
	batchKeyColumns      = 2
	membershipKeyColumns = 4
)

func Prepare(ctx context.Context, db *sql.DB, definition Definition) error {
	if err := definition.valid(); err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin plugin migration preparation: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, tables); err != nil {
		return fmt.Errorf("prepare plugin migration ledger: %w", err)
	}
	if err := adopt(ctx, tx); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, indexes); err != nil {
		return fmt.Errorf("index plugin migration ledger: %w", err)
	}

	current, found, err := inspect(ctx, tx)
	if err != nil {
		return err
	}
	if !found {
		_, err = tx.ExecContext(ctx, `INSERT INTO plugin_schema
			(singleton, plugin_name, schema_version, index_version, migration_state)
			VALUES (1, ?, ?, ?, ?)`, definition.Plugin, definition.SchemaVersion,
			definition.IndexVersion, StatePrepared)
		if err != nil {
			return fmt.Errorf("record plugin schema identity: %w", err)
		}
	} else {
		if current.Plugin != definition.Plugin {
			return fmt.Errorf("plugin database belongs to %q, not %q", current.Plugin, definition.Plugin)
		}
		if current.SchemaVersion > definition.SchemaVersion || current.IndexVersion > definition.IndexVersion {
			return fmt.Errorf("plugin database schema/index %d/%d is newer than supported %d/%d",
				current.SchemaVersion, current.IndexVersion, definition.SchemaVersion, definition.IndexVersion)
		}
		if current.SchemaVersion != definition.SchemaVersion || current.IndexVersion != definition.IndexVersion {
			_, err = tx.ExecContext(ctx, `UPDATE plugin_schema SET
				schema_version = ?, index_version = ?,
				prepared_at = datetime('now'), updated_at = datetime('now')
				WHERE singleton = 1`, definition.SchemaVersion, definition.IndexVersion)
			if err != nil {
				return fmt.Errorf("advance plugin schema identity: %w", err)
			}
			// A destination's shape may have moved under the migrations that
			// fill it, so every recorded verification is stale until re-proven.
			if _, err = tx.ExecContext(ctx, `UPDATE plugin_migrations SET migration_state = ?,
				verification_digest = NULL, verified_at = NULL, updated_at = datetime('now')
				WHERE migration_state <> ?`, StatePrepared, StatePrepared); err != nil {
				return fmt.Errorf("reopen named migrations after a schema change: %w", err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit plugin migration preparation: %w", err)
	}
	return nil
}

// PrepareMigration declares one named migration and the destination it owns.
// It is idempotent, and it never disturbs a migration that already exists, so a
// plugin can declare all of its migrations on every install without resetting
// the ones that already carried rows.
func PrepareMigration(ctx context.Context, db *sql.DB, migration Migration) error {
	if err := migration.valid(); err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin named migration preparation: %w", err)
	}
	defer tx.Rollback()
	present, err := ledgerPresent(ctx, tx)
	if err != nil {
		return err
	}
	if !present {
		return fmt.Errorf("plugin migration ledger is absent")
	}
	current, found, err := inspectMigration(ctx, tx, migration.Name)
	if err != nil {
		return err
	}
	switch {
	case !found:
		if _, err := tx.ExecContext(ctx, `INSERT INTO plugin_migrations
			(migration, destination_table, migration_state) VALUES (?, ?, ?)`,
			migration.Name, migration.DestinationTable, StatePrepared); err != nil {
			return fmt.Errorf("declare migration %q: %w", migration.Name, err)
		}
	case current.DestinationTable != migration.DestinationTable:
		return fmt.Errorf("migration %q owns destination %q, not %q",
			migration.Name, current.DestinationTable, migration.DestinationTable)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit named migration preparation: %w", err)
	}
	return nil
}

// Inspect reads the plugin database's identity. A database that never had a
// ledger yields the zero Snapshot, whose empty Plugin is what marks it absent.
func Inspect(ctx context.Context, db *sql.DB) (Snapshot, error) {
	snapshot, found, err := inspect(ctx, db)
	if err != nil {
		return Snapshot{}, err
	}
	if !found {
		return Snapshot{}, nil
	}
	return snapshot, nil
}

// InspectMigration reports one named migration's own lifecycle, independent of
// every other migration the same plugin database hosts.
func InspectMigration(ctx context.Context, db *sql.DB, name string) (MigrationSnapshot, error) {
	present, err := ledgerPresent(ctx, db)
	if err != nil {
		return MigrationSnapshot{}, err
	}
	if !present {
		return MigrationSnapshot{Name: name, State: StateAbsent}, nil
	}
	snapshot, found, err := inspectMigration(ctx, db, name)
	if err != nil {
		return MigrationSnapshot{}, err
	}
	if !found {
		return MigrationSnapshot{Name: name, State: StateAbsent}, nil
	}
	return snapshot, nil
}

func BeginBatch(ctx context.Context, db *sql.DB, spec BatchSpec) (*Batch, error) {
	if err := spec.valid(); err != nil {
		return nil, err
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("reserve plugin migration connection: %w", err)
	}
	batch := &Batch{conn: conn, spec: spec}
	if err := conn.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&batch.foreignKeys); err != nil {
		conn.Close()
		return nil, fmt.Errorf("read plugin migration foreign key mode: %w", err)
	}
	fail := func(err error) (*Batch, error) {
		if batch.tx != nil {
			batch.tx.Rollback()
		}
		batch.release()
		return nil, err
	}
	if _, err := conn.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
		return fail(fmt.Errorf("enable plugin migration foreign keys: %w", err))
	}
	batch.tx, err = conn.BeginTx(ctx, nil)
	if err != nil {
		return fail(fmt.Errorf("begin plugin migration batch: %w", err))
	}
	present, err := ledgerPresent(ctx, batch.tx)
	if err != nil {
		return fail(err)
	}
	if !present {
		return fail(fmt.Errorf("plugin migration ledger is absent"))
	}
	current, found, err := inspectMigration(ctx, batch.tx, spec.Migration)
	if err != nil {
		return fail(err)
	}
	if !found {
		return fail(fmt.Errorf("migration %q is not prepared", spec.Migration))
	}
	if current.State == StateVerified {
		return fail(ErrVerified)
	}
	if current.State != StatePrepared && current.State != StateBatchInProgress &&
		current.State != StateVerifiedEmpty {
		return fail(fmt.Errorf("migration %q state is %q, want %q, %q, or %q",
			spec.Migration, current.State, StatePrepared, StateBatchInProgress, StateVerifiedEmpty))
	}
	batch.destination = current.DestinationTable
	var committed int
	if err := batch.tx.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM migration_batches WHERE migration = ? AND batch_id = ?",
		spec.Migration, spec.ID).Scan(&committed); err != nil {
		return fail(fmt.Errorf("inspect migration batch %q: %w", spec.ID, err))
	}
	if committed != 0 {
		return fail(fmt.Errorf("%w: %s", ErrBatchCommitted, spec.ID))
	}
	if _, err := batch.tx.ExecContext(ctx, `UPDATE plugin_migrations SET migration_state = ?,
		verification_digest = NULL, verified_at = NULL, updated_at = datetime('now')
		WHERE migration = ?`, StateBatchInProgress, spec.Migration); err != nil {
		return fail(fmt.Errorf("mark migration batch in progress: %w", err))
	}
	return batch, nil
}

// LookupBatch returns the immutable receipt for one committed batch, scoped to
// the migration that carried it. Importers use it to prove that replaying the
// same frozen source is a no-op rather than trusting the batch id alone.
func LookupBatch(ctx context.Context, db *sql.DB, migration, id string) (CommittedBatch, bool, error) {
	if strings.TrimSpace(id) == "" {
		return CommittedBatch{}, false, fmt.Errorf("migration batch lookup needs an id")
	}
	var record CommittedBatch
	record.Migration = migration
	record.ID = id
	err := db.QueryRowContext(ctx, `SELECT source_database, source_table, row_count,
		canonical_digest, high_water_mark FROM migration_batches
		WHERE migration = ? AND batch_id = ?`, migration, id).Scan(
		&record.SourceDatabase, &record.SourceTable, &record.RowCount,
		&record.CanonicalDigest, &record.HighWaterMark)
	if errors.Is(err, sql.ErrNoRows) {
		return CommittedBatch{}, false, nil
	}
	if err != nil {
		return CommittedBatch{}, false, fmt.Errorf("look up migration batch %q: %w", id, err)
	}
	return record, true, nil
}

func (batch *Batch) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if batch == nil || batch.done {
		return nil, fmt.Errorf("migration batch is closed")
	}
	return batch.tx.ExecContext(ctx, query, args...)
}

func (batch *Batch) AddMembership(ctx context.Context, membership Membership) error {
	if batch == nil || batch.done {
		return fmt.Errorf("migration batch is closed")
	}
	if err := membership.valid(); err != nil {
		return err
	}
	if membership.DestinationTable != batch.destination {
		return fmt.Errorf("migration %q owns destination %q and cannot write into %q",
			batch.spec.Migration, batch.destination, membership.DestinationTable)
	}
	_, err := batch.tx.ExecContext(ctx, `INSERT INTO custody_memberships
		(migration, source_database, source_table, source_key, destination_table,
		 destination_key, canonical_digest, batch_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		batch.spec.Migration, batch.spec.SourceDatabase, batch.spec.SourceTable,
		membership.SourceKey, membership.DestinationTable, membership.DestinationKey,
		membership.CanonicalDigest, batch.spec.ID)
	if err != nil {
		return fmt.Errorf("record custody membership: %w", err)
	}
	return nil
}

func (batch *Batch) Commit(ctx context.Context, commit BatchCommit) error {
	if batch == nil || batch.done {
		return fmt.Errorf("migration batch is closed")
	}
	if err := commit.valid(); err != nil {
		return batch.fail(err)
	}
	var memberships int
	if err := batch.tx.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM custody_memberships WHERE migration = ? AND batch_id = ?",
		batch.spec.Migration, batch.spec.ID).Scan(&memberships); err != nil {
		return batch.fail(fmt.Errorf("count migration batch memberships: %w", err))
	}
	if memberships != commit.RowCount {
		return batch.fail(fmt.Errorf("migration batch row count is %d, but %d memberships were recorded",
			commit.RowCount, memberships))
	}
	if _, err := batch.tx.ExecContext(ctx, `INSERT INTO migration_batches
		(batch_id, migration, destination_table, source_database, source_table,
		 row_count, canonical_digest, high_water_mark)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, batch.spec.ID, batch.spec.Migration,
		batch.destination, batch.spec.SourceDatabase, batch.spec.SourceTable,
		commit.RowCount, commit.CanonicalDigest, commit.HighWaterMark); err != nil {
		return batch.fail(fmt.Errorf("commit migration batch record: %w", err))
	}
	if err := batch.tx.Commit(); err != nil {
		return batch.fail(fmt.Errorf("commit migration batch: %w", err))
	}
	batch.done = true
	return batch.release()
}

func (batch *Batch) Rollback() error {
	if batch == nil || batch.done {
		return nil
	}
	batch.done = true
	err := batch.tx.Rollback()
	releaseErr := batch.release()
	if err != nil && !errors.Is(err, sql.ErrTxDone) {
		return err
	}
	return releaseErr
}

// release restores the connection's foreign key enforcement before handing it
// back to the pool: the batch borrows a pooled connection, and a pragma left
// behind would enforce constraints on whatever unrelated write reuses it.
func (batch *Batch) release() error {
	_, err := batch.conn.ExecContext(context.Background(),
		fmt.Sprintf("PRAGMA foreign_keys = %d", batch.foreignKeys))
	return errors.Join(err, batch.conn.Close())
}

func (batch *Batch) fail(err error) error {
	rollbackErr := batch.Rollback()
	if rollbackErr != nil {
		return errors.Join(err, rollbackErr)
	}
	return err
}

// VerifyMigration seals one named migration that carried rows. The guard is
// scoped to that migration's own committed batches, so a sibling migration's
// work can never stand in for it.
func VerifyMigration(ctx context.Context, db *sql.DB, name, digest string) error {
	changed, err := recordVerification(ctx, db, name, digest, StateVerified,
		"EXISTS (SELECT 1 FROM migration_batches WHERE migration = ?)", "verify migration")
	if err != nil {
		return err
	}
	if changed != 1 {
		return fmt.Errorf("migration %q has no committed batch to verify", name)
	}
	return nil
}

// recordVerification is the one transition that records a verification outcome.
// Both outcomes write the same columns under the same non-repeatable guard and
// differ only in the population they demand, so the population clause is the
// caller's and the statement stays single-owner.
func recordVerification(ctx context.Context, db *sql.DB, name, digest string,
	state State, population, action string) (int64, error) {
	if !identifier.MatchString(name) {
		return 0, fmt.Errorf("migration name must be lowercase")
	}
	if !hexDigest.MatchString(digest) {
		return 0, fmt.Errorf("verification digest must be a lowercase SHA-256 digest")
	}
	result, err := db.ExecContext(ctx, `UPDATE plugin_migrations SET migration_state = ?,
		verification_digest = ?, verified_at = datetime('now'), updated_at = datetime('now')
		WHERE migration = ? AND migration_state <> ?
		  AND `+population, state, digest, name, StateVerified, name)
	if err != nil {
		return 0, fmt.Errorf("%s %q: %w", action, name, err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("read plugin verification result: %w", err)
	}
	return changed, nil
}

// VerifyMigrationEmpty records that one named migration verified with nothing to
// carry. VerifyMigration deliberately demands a committed batch, so a migration
// over an empty population could never leave `prepared`; this is the narrow
// counterpart, refusing any migration that did carry a row.
//
// It deliberately does not seal the migration. A home whose sources are empty
// today may hold rows tomorrow, so the recorded state is verified-empty and
// BeginBatch reopens it as soon as there is something to carry.
func VerifyMigrationEmpty(ctx context.Context, db *sql.DB, name, digest string) error {
	changed, err := recordVerification(ctx, db, name, digest, StateVerifiedEmpty,
		"NOT EXISTS (SELECT 1 FROM custody_memberships WHERE migration = ?)",
		"verify empty migration")
	if err != nil {
		return err
	}
	if changed != 1 {
		return fmt.Errorf("migration %q is not an empty population", name)
	}
	return nil
}

// MigrationCutoverEligible answers for both verified outcomes. A migration that
// verified with nothing to carry is as ready for the federated cutover as one
// that carried rows: the cutover is simply a no-op there. It stays re-openable
// until then, so rows written before the cutover are still carried.
func MigrationCutoverEligible(ctx context.Context, db *sql.DB, name string) (bool, error) {
	snapshot, err := InspectMigration(ctx, db, name)
	if err != nil {
		return false, err
	}
	if snapshot.State != StateVerified && snapshot.State != StateVerifiedEmpty {
		return false, nil
	}
	return snapshot.VerificationDigest != "", nil
}

type rowQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// inspect probes for the ledger before reading it, so a database that never had
// one is reported as absent instead of failing on a missing table.
func inspect(ctx context.Context, querier rowQuerier) (Snapshot, bool, error) {
	present, err := tablePresent(ctx, querier, "plugin_schema")
	if err != nil {
		return Snapshot{}, false, err
	}
	if !present {
		return Snapshot{}, false, nil
	}
	var snapshot Snapshot
	err = querier.QueryRowContext(ctx, `SELECT plugin_name, schema_version, index_version
		FROM plugin_schema WHERE singleton = 1`).Scan(
		&snapshot.Plugin, &snapshot.SchemaVersion, &snapshot.IndexVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return Snapshot{}, false, nil
	}
	if err != nil {
		return Snapshot{}, false, fmt.Errorf("read plugin migration state: %w", err)
	}
	return snapshot, true, nil
}

func inspectMigration(ctx context.Context, querier rowQuerier,
	name string) (MigrationSnapshot, bool, error) {
	snapshot := MigrationSnapshot{Name: name}
	var verification sql.NullString
	err := querier.QueryRowContext(ctx, `SELECT destination_table, migration_state,
		verification_digest FROM plugin_migrations WHERE migration = ?`, name).Scan(
		&snapshot.DestinationTable, &snapshot.State, &verification)
	if errors.Is(err, sql.ErrNoRows) {
		return MigrationSnapshot{}, false, nil
	}
	if err != nil {
		return MigrationSnapshot{}, false, fmt.Errorf("read migration %q state: %w", name, err)
	}
	snapshot.VerificationDigest = verification.String
	return snapshot, true, nil
}

func ledgerPresent(ctx context.Context, querier rowQuerier) (bool, error) {
	return tablePresent(ctx, querier, "plugin_migrations")
}

func tablePresent(ctx context.Context, querier rowQuerier, name string) (bool, error) {
	var exists int
	if err := querier.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master
		WHERE type = 'table' AND name = ?`, name).Scan(&exists); err != nil {
		return false, fmt.Errorf("inspect plugin migration ledger: %w", err)
	}
	return exists != 0, nil
}

// adopt brings a DATA-1 ledger up to named migrations without a migration
// runner. Both custody tables are keyed by migration, which SQLite cannot add to
// a primary key in place, so the rows are staged, the tables are recreated from
// the same declaration a fresh database gets, and the rows return under the
// empty name no migration can claim. It is idempotent: once both primary keys
// carry their migration, there is nothing left to do.
func adopt(ctx context.Context, tx *sql.Tx) error {
	batchKeys, err := primaryKeyColumns(ctx, tx, "migration_batches")
	if err != nil {
		return err
	}
	membershipKeys, err := primaryKeyColumns(ctx, tx, "custody_memberships")
	if err != nil {
		return err
	}
	if batchKeys == batchKeyColumns && membershipKeys == membershipKeyColumns {
		return nil
	}
	batchMigration, err := adoptedColumn(ctx, tx, "migration_batches", "migration")
	if err != nil {
		return err
	}
	batchDestination, err := adoptedColumn(ctx, tx, "migration_batches", "destination_table")
	if err != nil {
		return err
	}
	membershipMigration, err := adoptedColumn(ctx, tx, "custody_memberships", "migration")
	if err != nil {
		return err
	}
	rebuild := fmt.Sprintf(`
CREATE TABLE migration_batches_adopted AS SELECT %s AS migration, batch_id,
  %s AS destination_table, source_database, source_table, row_count,
  canonical_digest, high_water_mark, committed_at FROM migration_batches;
CREATE TABLE custody_memberships_adopted AS SELECT %s AS migration, source_database,
  source_table, source_key, destination_table, destination_key, canonical_digest,
  batch_id FROM custody_memberships;
DROP TABLE custody_memberships;
DROP TABLE migration_batches;
%s
INSERT INTO migration_batches (migration, batch_id, destination_table, source_database,
  source_table, row_count, canonical_digest, high_water_mark, committed_at)
  SELECT migration, batch_id, destination_table, source_database, source_table,
         row_count, canonical_digest, high_water_mark, committed_at
  FROM migration_batches_adopted;
INSERT INTO custody_memberships (migration, source_database, source_table, source_key,
  destination_table, destination_key, canonical_digest, batch_id)
  SELECT migration, source_database, source_table, source_key, destination_table,
         destination_key, canonical_digest, batch_id FROM custody_memberships_adopted;
DROP TABLE migration_batches_adopted;
DROP TABLE custody_memberships_adopted;
`, batchMigration, batchDestination, membershipMigration, custodyTables)
	if _, err := tx.ExecContext(ctx, rebuild); err != nil {
		return fmt.Errorf("adopt custody tables into named migrations: %w", err)
	}
	return nil
}

// adoptedColumn keeps a column a previous shape already carried and supplies the
// unclaimed empty name for one it never had.
func adoptedColumn(ctx context.Context, tx *sql.Tx, table, column string) (string, error) {
	var found int
	if err := tx.QueryRowContext(ctx, fmt.Sprintf(
		`SELECT COUNT(*) FROM pragma_table_info('%s') WHERE name = ?`, table), column).Scan(&found); err != nil {
		return "", fmt.Errorf("inspect %s columns: %w", table, err)
	}
	if found == 0 {
		return "''", nil
	}
	return column, nil
}

func primaryKeyColumns(ctx context.Context, tx *sql.Tx, table string) (int, error) {
	var keys int
	if err := tx.QueryRowContext(ctx, fmt.Sprintf(
		`SELECT COUNT(*) FROM pragma_table_info('%s') WHERE pk > 0`, table)).Scan(&keys); err != nil {
		return 0, fmt.Errorf("inspect %s key: %w", table, err)
	}
	return keys, nil
}

func (definition Definition) valid() error {
	if !identifier.MatchString(definition.Plugin) || definition.SchemaVersion < 1 || definition.IndexVersion < 0 {
		return fmt.Errorf("plugin migration definition needs a lowercase name, positive schema version, and non-negative index version")
	}
	return nil
}

func (migration Migration) valid() error {
	if !identifier.MatchString(migration.Name) ||
		!tableIdentifier.MatchString(migration.DestinationTable) {
		return fmt.Errorf("migration needs a lowercase name and a destination table")
	}
	return nil
}

func (spec BatchSpec) valid() error {
	if !identifier.MatchString(spec.Migration) {
		return fmt.Errorf("migration batch needs a lowercase migration name")
	}
	if strings.TrimSpace(spec.ID) == "" || strings.TrimSpace(spec.SourceDatabase) == "" ||
		strings.TrimSpace(spec.SourceTable) == "" {
		return fmt.Errorf("migration batch needs an id, source database, and source table")
	}
	return nil
}

func (membership Membership) valid() error {
	if strings.TrimSpace(membership.SourceKey) == "" || strings.TrimSpace(membership.DestinationTable) == "" ||
		strings.TrimSpace(membership.DestinationKey) == "" || !hexDigest.MatchString(membership.CanonicalDigest) {
		return fmt.Errorf("custody membership needs source/destination keys and a lowercase SHA-256 digest")
	}
	return nil
}

func (commit BatchCommit) valid() error {
	if commit.RowCount < 0 || !hexDigest.MatchString(commit.CanonicalDigest) || strings.TrimSpace(commit.HighWaterMark) == "" {
		return fmt.Errorf("migration batch commit needs a non-negative row count, lowercase SHA-256 digest, and high-water mark")
	}
	return nil
}
