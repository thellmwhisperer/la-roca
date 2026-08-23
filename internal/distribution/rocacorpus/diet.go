package rocacorpus

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"

	"github.com/thellmwhisperer/la-roca/internal/distribution/bundledplugin"
	"github.com/thellmwhisperer/la-roca/internal/distribution/migrationledger"
)

// CompactReport is the measured rewrite of a corpus database onto the one-row law.
type CompactReport struct {
	Sessions          int64 `json:"sessions"`
	Exchanges         int64 `json:"exchanges"`
	ThinkingBlocks    int64 `json:"thinking_blocks"`
	ToolUses          int64 `json:"tool_uses"`
	BytesBefore       int64 `json:"bytes_before"`
	BytesAfter        int64 `json:"bytes_after"`
	ReclaimedBytes    int64 `json:"reclaimed_bytes"`
	AlreadyApplied    bool  `json:"already_applied"`
	VersionFTSDropped bool  `json:"version_fts_dropped"`
	HashIndexes       bool  `json:"hash_indexes"`
}

type migrationSeal struct {
	Name, Digest string
	State        migrationledger.State
}

// Compact rewrites an existing corpus database to the one-row storage law and
// VACUUMs. Current harvest rows are counted before and after; they must match.
func Compact(ctx context.Context, path string) (CompactReport, error) {
	beforeInfo, err := os.Stat(path)
	if err != nil {
		return CompactReport{}, fmt.Errorf("stat corpus database: %w", err)
	}
	db, err := bundledplugin.OpenDatabase(path, true)
	if err != nil {
		return CompactReport{}, err
	}
	before, err := countCurrentRows(ctx, db)
	if closeErr := db.Close(); err != nil {
		return CompactReport{}, err
	} else if closeErr != nil {
		return CompactReport{}, closeErr
	}

	rewrote, err := applyStorageLaw(path)
	if err != nil {
		return CompactReport{}, err
	}
	if err := ApplySchema(path); err != nil {
		return CompactReport{}, err
	}
	if rewrote {
		if err := vacuumDatabase(path); err != nil {
			return CompactReport{}, err
		}
	}

	db, err = bundledplugin.OpenDatabase(path, true)
	if err != nil {
		return CompactReport{}, err
	}
	after, err := countCurrentRows(ctx, db)
	closeErr := db.Close()
	if err != nil {
		return CompactReport{}, err
	}
	if closeErr != nil {
		return CompactReport{}, closeErr
	}
	if after != before {
		return CompactReport{}, fmt.Errorf(
			"current rows changed during compact: sessions %d→%d exchanges %d→%d thinking %d→%d tools %d→%d",
			before.sessions, after.sessions, before.exchanges, after.exchanges,
			before.thinking, after.thinking, before.tools, after.tools)
	}
	afterInfo, err := os.Stat(path)
	if err != nil {
		return CompactReport{}, err
	}
	reclaimed := beforeInfo.Size() - afterInfo.Size()
	if reclaimed < 0 {
		reclaimed = 0
	}
	return CompactReport{
		Sessions:          after.sessions,
		Exchanges:         after.exchanges,
		ThinkingBlocks:    after.thinking,
		ToolUses:          after.tools,
		BytesBefore:       beforeInfo.Size(),
		BytesAfter:        afterInfo.Size(),
		ReclaimedBytes:    reclaimed,
		AlreadyApplied:    !rewrote,
		VersionFTSDropped: true,
		HashIndexes:       true,
	}, nil
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

func applyStorageLaw(path string) (bool, error) {
	db, err := bundledplugin.OpenDatabase(path, false)
	if err != nil {
		return false, err
	}
	defer db.Close()
	needed, err := storageLawNeeded(context.Background(), db)
	if err != nil {
		return false, err
	}
	if !needed {
		return false, nil
	}
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		return false, fmt.Errorf("begin storage-law rewrite: %w", err)
	}
	defer tx.Rollback()
	for _, statement := range storageLawStatements {
		if _, err := tx.Exec(statement); err != nil {
			return false, fmt.Errorf("apply storage-law rewrite: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit storage-law rewrite: %w", err)
	}
	return true, nil
}

func storageLawNeeded(ctx context.Context, db *sql.DB) (bool, error) {
	if present, err := tableExistsDB(ctx, db, "exchange_versions_fts"); err != nil {
		return false, err
	} else if present {
		return true, nil
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

func vacuumDatabase(path string) error {
	db, err := bundledplugin.OpenDatabase(path, false)
	if err != nil {
		return err
	}
	defer db.Close()
	if _, err := db.Exec("VACUUM"); err != nil {
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

var storageLawStatements = []string{
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
	`CREATE TABLE IF NOT EXISTS exchange_versions_slim (
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
   model, provider, tokens_in, tokens_out, tokens_reasoning, cost_usd)
 SELECT id, version_digest, session_id, exchange_number, is_after_compaction,
   human_timestamp, agent_timestamp, response_latency_ms,
   model, provider, tokens_in, tokens_out, tokens_reasoning, cost_usd
 FROM exchange_versions`,
	`DROP TABLE IF EXISTS exchange_versions`,
	`ALTER TABLE exchange_versions_slim RENAME TO exchange_versions`,
	`CREATE INDEX IF NOT EXISTS exchange_versions_logical_key
  ON exchange_versions(session_id, exchange_number)`,
	`CREATE TABLE IF NOT EXISTS thinking_block_versions_slim (
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
   caution_ratio, word_count, is_after_compaction)
 SELECT id, version_digest, session_id, exchange_number, position_in_session, depth,
   caution_ratio, word_count, is_after_compaction
 FROM thinking_block_versions`,
	`DROP TABLE IF EXISTS thinking_block_versions`,
	`ALTER TABLE thinking_block_versions_slim RENAME TO thinking_block_versions`,
	`CREATE INDEX IF NOT EXISTS thinking_block_versions_parent
  ON thinking_block_versions(session_id, exchange_number)`,
}
