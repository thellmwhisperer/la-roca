package rocacorpus

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/thellmwhisperer/la-roca/internal/distribution/bundledplugin"
	"github.com/thellmwhisperer/la-roca/internal/distribution/migrationledger"
	"github.com/thellmwhisperer/la-roca/internal/store/exactdedup"
)

// CompactReport is the measured rewrite of a corpus database onto the one-row law.
type CompactReport struct {
	Sessions           int64 `json:"sessions"`
	Exchanges          int64 `json:"exchanges"`
	ThinkingBlocks     int64 `json:"thinking_blocks"`
	ToolUses           int64 `json:"tool_uses"`
	CustodyMemberships int64 `json:"custody_memberships"`
	CorpusSourceRows   int64 `json:"corpus_source_rows"`
	BytesBefore        int64 `json:"bytes_before"`
	BytesAfter         int64 `json:"bytes_after"`
	ReclaimedBytes     int64 `json:"reclaimed_bytes"`
	AlreadyApplied     bool  `json:"already_applied"`
	VersionFTSDropped  bool  `json:"version_fts_dropped"`
	HashIndexes        bool  `json:"hash_indexes"`
	ArchiveRowsDropped bool  `json:"archive_rows_dropped"`
	VacuumFreelist     int64 `json:"vacuum_freelist"`
}

type migrationSeal struct {
	Name, Digest string
	State        migrationledger.State
}

// Compact rewrites an existing corpus database to the one-row storage law and
// VACUUMs. Current harvest rows are counted before and after; they must match.
func Compact(ctx context.Context, path string) (CompactReport, error) {
	beforeBytes, err := databaseSize(path)
	if err != nil {
		return CompactReport{}, fmt.Errorf("measure corpus database: %w", err)
	}
	db, err := bundledplugin.OpenDatabase(path, false)
	if err != nil {
		return CompactReport{}, err
	}
	if err := ensureCorpusOwned(ctx, db); err != nil {
		db.Close()
		return CompactReport{}, err
	}
	before, err := countCurrentRows(ctx, db)
	if err == nil {
		err = preflightHashGuards(ctx, db)
	}
	if closeErr := db.Close(); err != nil {
		return CompactReport{}, err
	} else if closeErr != nil {
		return CompactReport{}, closeErr
	}
	if err := ctx.Err(); err != nil {
		return CompactReport{}, err
	}

	rewrote, err := applyStorageLaw(ctx, path, true)
	if err != nil {
		return CompactReport{}, err
	}
	if err := restoreCompactSchema(ctx, path); err != nil {
		return CompactReport{}, err
	}
	if err := vacuumDatabase(ctx, path); err != nil {
		return CompactReport{}, err
	}

	db, err = bundledplugin.OpenDatabase(path, true)
	if err != nil {
		return CompactReport{}, err
	}
	after, err := countCurrentRows(ctx, db)
	if err != nil {
		db.Close()
		return CompactReport{}, err
	}
	custody, err := tableCountDB(ctx, db, "custody_memberships")
	if err != nil {
		db.Close()
		return CompactReport{}, err
	}
	sourceRows, err := tableCountDB(ctx, db, "corpus_source_rows")
	if err != nil {
		db.Close()
		return CompactReport{}, err
	}
	hashIndexes, err := exactdedup.GuardsInstalled(ctx, db)
	if err != nil {
		db.Close()
		return CompactReport{}, err
	}
	if !hashIndexes {
		db.Close()
		return CompactReport{}, fmt.Errorf("compact could not install every hash-backed exact-payload guard")
	}
	versionFTSDropped, err := versionFTSAbsent(ctx, db)
	if err != nil {
		db.Close()
		return CompactReport{}, err
	}
	var vacuumFreelist int64
	if err := db.QueryRowContext(ctx, `PRAGMA freelist_count`).Scan(&vacuumFreelist); err != nil {
		db.Close()
		return CompactReport{}, err
	}
	closeErr := db.Close()
	if closeErr != nil {
		return CompactReport{}, closeErr
	}
	if !versionFTSDropped {
		return CompactReport{}, fmt.Errorf("compact left a version full-text index installed")
	}
	if vacuumFreelist != 0 {
		return CompactReport{}, fmt.Errorf("compact left VACUUM freelist at %d", vacuumFreelist)
	}
	if after != before {
		return CompactReport{}, fmt.Errorf(
			"current rows changed during compact: sessions %d→%d exchanges %d→%d thinking %d→%d tools %d→%d",
			before.sessions, after.sessions, before.exchanges, after.exchanges,
			before.thinking, after.thinking, before.tools, after.tools)
	}
	afterBytes, err := databaseSize(path)
	if err != nil {
		return CompactReport{}, err
	}
	reclaimed := beforeBytes - afterBytes
	if reclaimed < 0 {
		reclaimed = 0
	}
	return CompactReport{
		Sessions:           after.sessions,
		Exchanges:          after.exchanges,
		ThinkingBlocks:     after.thinking,
		ToolUses:           after.tools,
		CustodyMemberships: custody,
		CorpusSourceRows:   sourceRows,
		BytesBefore:        beforeBytes,
		BytesAfter:         afterBytes,
		ReclaimedBytes:     reclaimed,
		AlreadyApplied:     !rewrote,
		VersionFTSDropped:  versionFTSDropped,
		HashIndexes:        hashIndexes,
		ArchiveRowsDropped: custody == 0 && sourceRows == 0,
		VacuumFreelist:     vacuumFreelist,
	}, nil
}

// ensureCorpusOwned verifies the target file belongs to the roca-corpus plugin
// before any mutation. The core schema shares the four current tables, so a row
// count alone cannot distinguish it from a corpus database.
func ensureCorpusOwned(ctx context.Context, db *sql.DB) error {
	snapshot, err := migrationledger.Inspect(ctx, db)
	if err != nil {
		return fmt.Errorf("inspect corpus database ownership: %w", err)
	}
	if snapshot.Plugin != Name {
		if snapshot.Plugin == "" {
			return fmt.Errorf("the database declares no %s corpus identity; compact refuses to mutate it", Name)
		}
		return fmt.Errorf("the database belongs to %q, not the %s corpus", snapshot.Plugin, Name)
	}
	return nil
}

// databaseSize reports the total physical footprint of a database, including
// the WAL sidecar where uncheckpointed pages live.
func databaseSize(path string) (int64, error) {
	total, err := fileSize(path)
	if err != nil {
		return 0, err
	}
	if wal, err := fileSize(path + "-wal"); err == nil {
		total += wal
	} else if !errors.Is(err, os.ErrNotExist) {
		return 0, fmt.Errorf("measure corpus database WAL: %w", err)
	}
	return total, nil
}

func fileSize(path string) (int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}

func restoreCompactSchema(ctx context.Context, path string) error {
	if err := applySchema(context.WithoutCancel(ctx), path); err != nil {
		return err
	}
	return ctx.Err()
}

func preflightHashGuards(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin hash-guard preflight: %w", err)
	}
	if err := exactdedup.EnsureGuards(ctx, tx); err != nil {
		if rollbackErr := tx.Rollback(); rollbackErr != nil {
			return fmt.Errorf("preflight hash guards: %v; rollback preflight: %w", err, rollbackErr)
		}
		return fmt.Errorf("preflight hash guards: %w", err)
	}
	if err := tx.Rollback(); err != nil {
		return fmt.Errorf("rollback hash-guard preflight: %w", err)
	}
	return nil
}

func installHashGuards(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin hash-guard installation: %w", err)
	}
	defer tx.Rollback()
	if err := exactdedup.EnsureGuards(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit hash-guard installation: %w", err)
	}
	return nil
}

func versionFTSAbsent(ctx context.Context, db *sql.DB) (bool, error) {
	for _, table := range []string{
		"session_versions_fts", "exchange_versions_fts", "thinking_block_versions_fts",
	} {
		present, err := tableExistsDB(ctx, db, table)
		if err != nil {
			return false, err
		}
		if present {
			return false, nil
		}
	}
	return true, nil
}

type currentRowCounts struct {
	sessions, exchanges, thinking, tools int64
}

func countCurrentRows(ctx context.Context, db *sql.DB) (currentRowCounts, error) {
	var counts currentRowCounts
	for _, item := range []struct {
		table string
		dest  *int64
	}{
		{"sessions", &counts.sessions},
		{"exchanges", &counts.exchanges},
		{"thinking_blocks", &counts.thinking},
		{"tool_uses", &counts.tools},
	} {
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+item.table).Scan(item.dest); err != nil {
			return currentRowCounts{}, fmt.Errorf("count current %s: %w", item.table, err)
		}
	}
	return counts, nil
}

func applyStorageLaw(ctx context.Context, path string, dropArchive bool) (bool, error) {
	db, err := bundledplugin.OpenDatabase(path, false)
	if err != nil {
		return false, err
	}
	defer db.Close()
	needed, err := storageLawNeeded(ctx, db)
	if err != nil {
		return false, err
	}
	bookkeeping, err := archiveBookkeepingPresent(ctx, db)
	if err != nil {
		return false, err
	}
	if !needed && !(dropArchive && bookkeeping) {
		return false, nil
	}
	harvest, err := countCurrentRows(ctx, db)
	if err != nil {
		return false, err
	}
	var statements []string
	slimVersions := false
	if dropArchive && bookkeeping &&
		harvest.sessions+harvest.exchanges+harvest.thinking+harvest.tools > 0 {
		statements = dropArchiveStatements
	} else if needed {
		statements = slimVersionStatements
		slimVersions = true
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin storage-law rewrite: %w", err)
	}
	defer tx.Rollback()
	for _, statement := range storageLawPrefix {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return false, fmt.Errorf("prepare storage-law rewrite: %w", err)
		}
	}
	if slimVersions {
		if err := prepareSlimVersionObservedAt(ctx, tx); err != nil {
			return false, err
		}
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return false, fmt.Errorf("apply storage-law rewrite: %w", err)
		}
	}
	if err := exactdedup.EnsureGuards(ctx, tx); err != nil {
		return false, fmt.Errorf("install storage-law hash guards: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit storage-law rewrite: %w", err)
	}
	return true, nil
}

func prepareSlimVersionObservedAt(ctx context.Context, tx *sql.Tx) error {
	for _, table := range []string{
		"session_versions", "exchange_versions", "tool_use_versions", "thinking_block_versions",
	} {
		columns, err := bundledplugin.TableColumns(ctx, tx, table)
		if err != nil {
			return fmt.Errorf("inspect %s observed time: %w", table, err)
		}
		if !columns["observed_at"] {
			if _, err := tx.ExecContext(ctx, "ALTER TABLE "+table+" ADD COLUMN observed_at TEXT"); err != nil {
				return fmt.Errorf("add %s observed time: %w", table, err)
			}
		}
		if _, err := tx.ExecContext(ctx,
			"UPDATE "+table+" SET observed_at = datetime('now') WHERE observed_at IS NULL"); err != nil {
			return fmt.Errorf("backfill %s observed time: %w", table, err)
		}
	}
	return nil
}

func storageLawNeeded(ctx context.Context, db *sql.DB) (bool, error) {
	for _, table := range []string{
		"session_versions_fts", "exchange_versions_fts", "thinking_block_versions_fts",
	} {
		if present, err := tableExistsDB(ctx, db, table); err != nil {
			return false, err
		} else if present {
			return true, nil
		}
	}
	if present, err := columnExistsDB(ctx, db, "exchange_versions", "human_text"); err != nil {
		return false, err
	} else if present {
		return true, nil
	}
	if present, err := columnExistsDB(ctx, db, "thinking_block_versions", "full_text"); err != nil {
		return false, err
	} else if present {
		return true, nil
	}
	for _, column := range []struct{ table, name string }{
		{"session_versions", "title"},
		{"session_versions", "metadata"},
		{"tool_use_versions", "tool_params_summary"},
		{"tool_use_versions", "error_message"},
	} {
		if present, err := columnExistsDB(ctx, db, column.table, column.name); err != nil {
			return false, err
		} else if present {
			return true, nil
		}
	}
	var indexSQL sql.NullString
	err := db.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type = 'index' AND name = 'idx_exchanges_exact_payload'`).
		Scan(&indexSQL)
	if err != nil && err != sql.ErrNoRows {
		return false, err
	}
	if err == nil && indexSQL.Valid && !strings.Contains(strings.ToLower(indexSQL.String), "roca_payload_hash(") {
		return true, nil
	}
	for _, name := range []string{"custody_memberships_digest", "custody_memberships_migration"} {
		present, err := indexExistsDB(ctx, db, name)
		if err != nil {
			return false, err
		}
		if present {
			return true, nil
		}
	}
	return false, nil
}

func archiveBookkeepingPresent(ctx context.Context, db *sql.DB) (bool, error) {
	for _, table := range []string{"custody_memberships", "corpus_source_rows"} {
		count, err := tableCountDB(ctx, db, table)
		if err != nil {
			return false, err
		}
		if count > 0 {
			return true, nil
		}
	}
	return false, nil
}

func tableCountDB(ctx context.Context, db *sql.DB, name string) (int64, error) {
	present, err := tableExistsDB(ctx, db, name)
	if err != nil || !present {
		return 0, err
	}
	var count int64
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+name).Scan(&count); err != nil {
		return 0, fmt.Errorf("count %s: %w", name, err)
	}
	return count, nil
}

func indexExistsDB(ctx context.Context, db *sql.DB, name string) (bool, error) {
	var count int
	err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?`, name).Scan(&count)
	return count == 1, err
}

func tableExistsDB(ctx context.Context, db *sql.DB, name string) (bool, error) {
	var count int
	err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, name).Scan(&count)
	return count == 1, err
}

func columnExistsDB(ctx context.Context, db *sql.DB, table, column string) (bool, error) {
	present, err := tableExistsDB(ctx, db, table)
	if err != nil || !present {
		return false, err
	}
	columns, err := bundledplugin.TableColumns(ctx, db, table)
	if err != nil {
		return false, err
	}
	return columns[column], nil
}

func vacuumDatabase(ctx context.Context, path string) error {
	db, err := bundledplugin.OpenDatabase(path, false)
	if err != nil {
		return err
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, "VACUUM"); err != nil {
		return fmt.Errorf("vacuum corpus database: %w", err)
	}
	return nil
}

func snapshotMigrationSeals(path string) ([]migrationSeal, error) {
	db, err := bundledplugin.OpenDatabase(path, true)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	schemaPresent, err := tableExistsDB(context.Background(), db, "plugin_schema")
	if err != nil || !schemaPresent {
		return nil, err
	}
	var installed int
	if err := db.QueryRow(`SELECT schema_version FROM plugin_schema WHERE singleton = 1`).Scan(&installed); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	if installed >= SchemaVersion {
		return nil, nil
	}
	present, err := tableExistsDB(context.Background(), db, "plugin_migrations")
	if err != nil || !present {
		return nil, err
	}
	rows, err := db.Query(`SELECT migration, COALESCE(verification_digest, ''), migration_state
		FROM plugin_migrations`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var seals []migrationSeal
	for rows.Next() {
		var seal migrationSeal
		if err := rows.Scan(&seal.Name, &seal.Digest, &seal.State); err != nil {
			return nil, err
		}
		if seal.Digest != "" && (seal.State == migrationledger.StateVerified ||
			seal.State == migrationledger.StateVerifiedEmpty) {
			seals = append(seals, seal)
		}
	}
	return seals, rows.Err()
}

func restoreMigrationSeals(path string, seals []migrationSeal) error {
	if len(seals) == 0 {
		return nil
	}
	db, err := bundledplugin.OpenDatabase(path, false)
	if err != nil {
		return err
	}
	defer db.Close()
	for _, seal := range seals {
		var restoreErr error
		if seal.State == migrationledger.StateVerifiedEmpty {
			restoreErr = migrationledger.VerifyMigrationEmpty(context.Background(), db, seal.Name, seal.Digest)
		} else {
			restoreErr = migrationledger.VerifyMigration(context.Background(), db, seal.Name, seal.Digest)
		}
		if restoreErr != nil {
			return fmt.Errorf("restore custody seal %s: %w", seal.Name, restoreErr)
		}
	}
	return nil
}

var storageLawPrefix = []string{
	`DROP VIEW IF EXISTS session_version_memberships`,
	`DROP VIEW IF EXISTS exchange_version_memberships`,
	`DROP VIEW IF EXISTS tool_use_version_memberships`,
	`DROP VIEW IF EXISTS thinking_block_version_memberships`,
	`DROP VIEW IF EXISTS ingest_file_state_version_memberships`,
	`DROP TABLE IF EXISTS session_versions_fts`,
	`DROP TABLE IF EXISTS exchange_versions_fts`,
	`DROP TABLE IF EXISTS thinking_block_versions_fts`,
	`DROP INDEX IF EXISTS idx_exchanges_exact_payload`,
	`DROP INDEX IF EXISTS idx_thinking_blocks_exact_payload`,
	`DROP INDEX IF EXISTS idx_sessions_exact_payload`,
	`DROP INDEX IF EXISTS idx_memories_exact_payload`,
	`DROP INDEX IF EXISTS custody_memberships_digest`,
	`DROP INDEX IF EXISTS custody_memberships_migration`,
}

// dropArchiveStatements remove the internal archive copy once current harvest
// rows already hold the facts. Batch hashes stay on migration_batches.
var dropArchiveStatements = []string{
	`DROP TABLE IF EXISTS custody_memberships`,
	`CREATE TABLE IF NOT EXISTS custody_memberships (
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
)`,
	`CREATE INDEX IF NOT EXISTS custody_memberships_destination
  ON custody_memberships(destination_table, destination_key)`,
	`CREATE INDEX IF NOT EXISTS custody_memberships_batch
  ON custody_memberships(migration, batch_id)`,
	`DROP TABLE IF EXISTS corpus_source_rows`,
	`DROP TABLE IF EXISTS ingest_file_state_heads`,
	`DROP TABLE IF EXISTS ingest_file_state_versions`,
	`DROP TABLE IF EXISTS session_versions`,
	`DROP TABLE IF EXISTS exchange_versions`,
	`DROP TABLE IF EXISTS tool_use_versions`,
	`DROP TABLE IF EXISTS thinking_block_versions`,
}

var slimVersionStatements = []string{
	`DROP TABLE IF EXISTS session_versions_slim`,
	`CREATE TABLE session_versions_slim (
  id               INTEGER PRIMARY KEY AUTOINCREMENT,
  version_digest   TEXT NOT NULL UNIQUE CHECK (length(version_digest) = 64),
  session_id       TEXT NOT NULL,
  source_agent     TEXT,
  source_surface   TEXT,
  project          TEXT,
  started_at       TEXT,
  ended_at         TEXT,
  duration_minutes INTEGER,
  observed_at      TEXT NOT NULL DEFAULT (datetime('now'))
)`,
	`INSERT OR IGNORE INTO session_versions_slim
	  (id, version_digest, session_id, source_agent, source_surface, project,
	   started_at, ended_at, duration_minutes, observed_at)
	 SELECT id, version_digest, session_id, source_agent, source_surface, project,
	   started_at, ended_at, duration_minutes, observed_at
	 FROM session_versions`,
	`DROP TABLE IF EXISTS session_versions`,
	`ALTER TABLE session_versions_slim RENAME TO session_versions`,
	`CREATE INDEX IF NOT EXISTS session_versions_logical_id
  ON session_versions(session_id)`,
	`DROP TABLE IF EXISTS exchange_versions_slim`,
	`CREATE TABLE exchange_versions_slim (
  id                  INTEGER PRIMARY KEY AUTOINCREMENT,
  version_digest      TEXT NOT NULL UNIQUE CHECK (length(version_digest) = 64),
  session_id          TEXT NOT NULL,
  exchange_number     INTEGER,
  is_after_compaction INTEGER,
  human_timestamp     TEXT,
  agent_timestamp     TEXT,
  response_latency_ms INTEGER,
  model               TEXT,
  provider            TEXT,
  tokens_in           INTEGER,
  tokens_out          INTEGER,
  tokens_reasoning    INTEGER,
  cost_usd            REAL,
  observed_at         TEXT NOT NULL DEFAULT (datetime('now'))
)`,
	`INSERT OR IGNORE INTO exchange_versions_slim
	  (id, version_digest, session_id, exchange_number, is_after_compaction,
	   human_timestamp, agent_timestamp, response_latency_ms,
	   model, provider, tokens_in, tokens_out, tokens_reasoning, cost_usd, observed_at)
	 SELECT id, version_digest, session_id, exchange_number, is_after_compaction,
	   human_timestamp, agent_timestamp, response_latency_ms,
	   model, provider, tokens_in, tokens_out, tokens_reasoning, cost_usd, observed_at
	 FROM exchange_versions`,
	`DROP TABLE IF EXISTS exchange_versions`,
	`ALTER TABLE exchange_versions_slim RENAME TO exchange_versions`,
	`CREATE INDEX IF NOT EXISTS exchange_versions_logical_key
  ON exchange_versions(session_id, exchange_number)`,
	`DROP TABLE IF EXISTS tool_use_versions_slim`,
	`CREATE TABLE tool_use_versions_slim (
  id                  INTEGER PRIMARY KEY AUTOINCREMENT,
  version_digest      TEXT NOT NULL UNIQUE CHECK (length(version_digest) = 64),
  session_id          TEXT NOT NULL,
  exchange_number     INTEGER,
  tool_name           TEXT,
  had_error           INTEGER,
  initiative_type     TEXT,
  observed_at         TEXT NOT NULL DEFAULT (datetime('now'))
)`,
	`INSERT OR IGNORE INTO tool_use_versions_slim
	  (id, version_digest, session_id, exchange_number, tool_name, had_error, initiative_type,
	   observed_at)
	 SELECT id, version_digest, session_id, exchange_number, tool_name, had_error, initiative_type,
	   observed_at
	 FROM tool_use_versions`,
	`DROP TABLE IF EXISTS tool_use_versions`,
	`ALTER TABLE tool_use_versions_slim RENAME TO tool_use_versions`,
	`CREATE INDEX IF NOT EXISTS tool_use_versions_parent
  ON tool_use_versions(session_id, exchange_number)`,
	`DROP TABLE IF EXISTS thinking_block_versions_slim`,
	`CREATE TABLE thinking_block_versions_slim (
  id                  INTEGER PRIMARY KEY AUTOINCREMENT,
  version_digest      TEXT NOT NULL UNIQUE CHECK (length(version_digest) = 64),
  session_id          TEXT NOT NULL,
  exchange_number     INTEGER,
  position_in_session REAL,
  depth               TEXT,
  caution_ratio       REAL,
  word_count          INTEGER,
  is_after_compaction INTEGER,
  observed_at         TEXT NOT NULL DEFAULT (datetime('now'))
)`,
	`INSERT OR IGNORE INTO thinking_block_versions_slim
	  (id, version_digest, session_id, exchange_number, position_in_session, depth,
	   caution_ratio, word_count, is_after_compaction, observed_at)
	 SELECT id, version_digest, session_id, exchange_number, position_in_session, depth,
	   caution_ratio, word_count, is_after_compaction, observed_at
	 FROM thinking_block_versions`,
	`DROP TABLE IF EXISTS thinking_block_versions`,
	`ALTER TABLE thinking_block_versions_slim RENAME TO thinking_block_versions`,
	`CREATE INDEX IF NOT EXISTS thinking_block_versions_parent
  ON thinking_block_versions(session_id, exchange_number)`,
}
