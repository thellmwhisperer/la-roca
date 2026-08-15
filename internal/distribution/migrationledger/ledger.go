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
	// carry. It is derived, never stored: databases shipped before this state
	// existed constrain `migration_state` to the three stored values, so an
	// empty verification keeps `prepared` on disk and is recognized by its
	// recorded verification beside zero committed batches. That is what makes
	// it re-openable rather than terminal, unlike StateVerified.
	StateVerifiedEmpty State = "verified-empty"
)

var (
	ErrBatchCommitted = errors.New("migration batch is already committed")
	ErrVerified       = errors.New("verified plugin database cannot accept another batch")
	identifier        = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	hexDigest         = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

type Definition struct {
	Plugin        string
	SchemaVersion int
	IndexVersion  int
}

type Snapshot struct {
	Plugin             string
	SchemaVersion      int
	IndexVersion       int
	State              State
	VerificationDigest string
}

type BatchSpec struct {
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

type Batch struct {
	conn        *sql.Conn
	tx          *sql.Tx
	spec        BatchSpec
	foreignKeys int
	done        bool
}

const schema = `
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

CREATE TABLE IF NOT EXISTS migration_batches (
  batch_id            TEXT PRIMARY KEY,
  source_database     TEXT NOT NULL,
  source_table        TEXT NOT NULL,
  row_count           INTEGER NOT NULL CHECK (row_count >= 0),
  canonical_digest    TEXT NOT NULL,
  high_water_mark     TEXT NOT NULL,
  committed_at        TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS custody_memberships (
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

CREATE INDEX IF NOT EXISTS custody_memberships_destination
  ON custody_memberships(destination_table, destination_key);
CREATE INDEX IF NOT EXISTS custody_memberships_digest
  ON custody_memberships(canonical_digest);
CREATE INDEX IF NOT EXISTS custody_memberships_batch
  ON custody_memberships(batch_id);
`

func Prepare(ctx context.Context, db *sql.DB, definition Definition) error {
	if err := definition.valid(); err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin plugin migration preparation: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("prepare plugin migration ledger: %w", err)
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
				schema_version = ?, index_version = ?, migration_state = ?,
				verification_digest = NULL, verified_at = NULL,
				prepared_at = datetime('now'), updated_at = datetime('now')
				WHERE singleton = 1`, definition.SchemaVersion, definition.IndexVersion, StatePrepared)
			if err != nil {
				return fmt.Errorf("advance plugin schema identity: %w", err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit plugin migration preparation: %w", err)
	}
	return nil
}

func Inspect(ctx context.Context, db *sql.DB) (Snapshot, error) {
	snapshot, found, err := inspect(ctx, db)
	if err != nil {
		return Snapshot{}, err
	}
	if !found {
		return Snapshot{State: StateAbsent}, nil
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
	current, found, err := inspect(ctx, batch.tx)
	if err != nil {
		return fail(err)
	}
	if !found {
		return fail(fmt.Errorf("plugin migration ledger is absent"))
	}
	if current.State == StateVerified {
		return fail(ErrVerified)
	}
	if current.State != StatePrepared && current.State != StateBatchInProgress &&
		current.State != StateVerifiedEmpty {
		return fail(fmt.Errorf("plugin migration state is %q, want %q, %q, or %q",
			current.State, StatePrepared, StateBatchInProgress, StateVerifiedEmpty))
	}
	var committed int
	if err := batch.tx.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM migration_batches WHERE batch_id = ?", spec.ID).Scan(&committed); err != nil {
		return fail(fmt.Errorf("inspect migration batch %q: %w", spec.ID, err))
	}
	if committed != 0 {
		return fail(fmt.Errorf("%w: %s", ErrBatchCommitted, spec.ID))
	}
	if _, err := batch.tx.ExecContext(ctx, `UPDATE plugin_schema SET migration_state = ?,
		verification_digest = NULL, verified_at = NULL, updated_at = datetime('now')
		WHERE singleton = 1`, StateBatchInProgress); err != nil {
		return fail(fmt.Errorf("mark migration batch in progress: %w", err))
	}
	return batch, nil
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
	_, err := batch.tx.ExecContext(ctx, `INSERT INTO custody_memberships
		(source_database, source_table, source_key, destination_table, destination_key,
		 canonical_digest, batch_id) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		batch.spec.SourceDatabase, batch.spec.SourceTable, membership.SourceKey,
		membership.DestinationTable, membership.DestinationKey, membership.CanonicalDigest,
		batch.spec.ID)
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
		"SELECT COUNT(*) FROM custody_memberships WHERE batch_id = ?", batch.spec.ID).Scan(&memberships); err != nil {
		return batch.fail(fmt.Errorf("count migration batch memberships: %w", err))
	}
	if memberships != commit.RowCount {
		return batch.fail(fmt.Errorf("migration batch row count is %d, but %d memberships were recorded",
			commit.RowCount, memberships))
	}
	if _, err := batch.tx.ExecContext(ctx, `INSERT INTO migration_batches
		(batch_id, source_database, source_table, row_count, canonical_digest, high_water_mark)
		VALUES (?, ?, ?, ?, ?, ?)`, batch.spec.ID, batch.spec.SourceDatabase,
		batch.spec.SourceTable, commit.RowCount, commit.CanonicalDigest, commit.HighWaterMark); err != nil {
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

func Verify(ctx context.Context, db *sql.DB, digest string) error {
	if !hexDigest.MatchString(digest) {
		return fmt.Errorf("verification digest must be a lowercase SHA-256 digest")
	}
	result, err := db.ExecContext(ctx, `UPDATE plugin_schema SET migration_state = ?,
		verification_digest = ?, verified_at = datetime('now'), updated_at = datetime('now')
		WHERE singleton = 1 AND migration_state <> ?
		  AND EXISTS (SELECT 1 FROM migration_batches)`, StateVerified, digest, StateVerified)
	if err != nil {
		return fmt.Errorf("verify plugin migration: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read plugin verification result: %w", err)
	}
	if changed != 1 {
		return fmt.Errorf("plugin migration has no committed batch to verify")
	}
	return nil
}

// VerifyEmpty records that a migration verified with nothing to carry. Verify
// deliberately demands a committed batch, so a migration over an empty
// population could never leave `prepared`; this is the narrow counterpart,
// refusing any database that did commit a batch so the ordinary guard keeps its
// strength for every other migration.
//
// It deliberately does not seal the ledger. A home whose sources are empty today
// may hold rows tomorrow, so the stored state remains `prepared` and only the
// recorded verification marks the outcome: Inspect reports StateVerifiedEmpty,
// and BeginBatch reopens the migration as soon as there is something to carry.
func VerifyEmpty(ctx context.Context, db *sql.DB, digest string) error {
	if !hexDigest.MatchString(digest) {
		return fmt.Errorf("verification digest must be a lowercase SHA-256 digest")
	}
	result, err := db.ExecContext(ctx, `UPDATE plugin_schema SET migration_state = ?,
		verification_digest = ?, verified_at = datetime('now'), updated_at = datetime('now')
		WHERE singleton = 1 AND migration_state <> ?
		  AND NOT EXISTS (SELECT 1 FROM migration_batches)
		  AND NOT EXISTS (SELECT 1 FROM custody_memberships)`, StatePrepared, digest, StateVerified)
	if err != nil {
		return fmt.Errorf("verify empty plugin migration: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read plugin verification result: %w", err)
	}
	if changed != 1 {
		return fmt.Errorf("plugin migration is not an empty population")
	}
	return nil
}

// CommittedBatches counts the batches a plugin database has already carried, so
// a driver can tell an empty population from an interrupted migration.
func CommittedBatches(ctx context.Context, db *sql.DB) (int, error) {
	var committed int
	if err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM migration_batches").Scan(&committed); err != nil {
		return 0, fmt.Errorf("count committed migration batches: %w", err)
	}
	return committed, nil
}

func CutoverEligible(ctx context.Context, db *sql.DB) (bool, error) {
	snapshot, err := Inspect(ctx, db)
	if err != nil {
		return false, err
	}
	return snapshot.State == StateVerified && snapshot.VerificationDigest != "", nil
}

type rowQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// inspect probes for the ledger before reading it, so a database that never had
// one is reported as absent instead of failing on a missing table.
func inspect(ctx context.Context, querier rowQuerier) (Snapshot, bool, error) {
	var exists int
	if err := querier.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master
		WHERE type = 'table' AND name = 'plugin_schema'`).Scan(&exists); err != nil {
		return Snapshot{}, false, fmt.Errorf("inspect plugin migration ledger: %w", err)
	}
	if exists == 0 {
		return Snapshot{}, false, nil
	}
	var snapshot Snapshot
	var verification, verifiedAt sql.NullString
	err := querier.QueryRowContext(ctx, `SELECT plugin_name, schema_version, index_version,
		migration_state, verification_digest, verified_at FROM plugin_schema WHERE singleton = 1`).Scan(
		&snapshot.Plugin, &snapshot.SchemaVersion, &snapshot.IndexVersion,
		&snapshot.State, &verification, &verifiedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Snapshot{}, false, nil
	}
	if err != nil {
		return Snapshot{}, false, fmt.Errorf("read plugin migration state: %w", err)
	}
	snapshot.VerificationDigest = verification.String
	if snapshot.State == StatePrepared && verifiedAt.Valid {
		snapshot.State = StateVerifiedEmpty
	}
	return snapshot, true, nil
}

func (definition Definition) valid() error {
	if !identifier.MatchString(definition.Plugin) || definition.SchemaVersion < 1 || definition.IndexVersion < 0 {
		return fmt.Errorf("plugin migration definition needs a lowercase name, positive schema version, and non-negative index version")
	}
	return nil
}

func (spec BatchSpec) valid() error {
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
