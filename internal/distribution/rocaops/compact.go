package rocaops

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/thellmwhisperer/la-roca/internal/distribution/bundledplugin"
)

const jsSafeInteger = 1<<53 - 1

func compactMemoryIDs(path string) error {
	db, err := bundledplugin.OpenDatabase(path, false)
	if err != nil {
		return err
	}
	defer db.Close()
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin memory id compact: %w", err)
	}
	defer tx.Rollback()
	if err := ensureLegacyIDColumn(ctx, tx); err != nil {
		return err
	}
	var oversized int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM memories WHERE id > ?`, jsSafeInteger).Scan(&oversized); err != nil {
		return fmt.Errorf("count oversized memory ids: %w", err)
	}
	if oversized == 0 {
		return tx.Commit()
	}
	if err := dropMemoryFTSTriggers(tx); err != nil {
		return err
	}
	if err := compactOversizedIDs(ctx, tx); err != nil {
		return err
	}
	if err := resetSequence(ctx, tx); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO memories_fts(memories_fts) VALUES ('rebuild')`); err != nil {
		return fmt.Errorf("rebuild memories fts after id compact: %w", err)
	}
	if err := createMemoryFTSTriggers(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func createMemoryFTSTriggers(tx *sql.Tx) error {
	statements := memoryFTSTriggerSQL(schema)
	if len(statements) != 3 {
		return fmt.Errorf("schema.sql is missing memories FTS triggers")
	}
	for _, statement := range statements {
		if _, err := tx.Exec(statement); err != nil {
			return fmt.Errorf("restore memories fts trigger: %w", err)
		}
	}
	return nil
}

func memoryFTSTriggerSQL(schemaSQL string) []string {
	var statements []string
	for _, name := range []string{"memories_ai", "memories_ad", "memories_au"} {
		needle := "CREATE TRIGGER IF NOT EXISTS " + name
		start := strings.Index(schemaSQL, needle)
		if start < 0 {
			return nil
		}
		rest := schemaSQL[start:]
		end := strings.Index(rest, "END;")
		if end < 0 {
			return nil
		}
		statements = append(statements, strings.TrimSpace(rest[:end+4]))
	}
	return statements
}

func ensureLegacyIDColumn(ctx context.Context, tx *sql.Tx) error {
	var n int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pragma_table_info('memories') WHERE name = 'legacy_id'`).Scan(&n); err != nil {
		return fmt.Errorf("inspect memories.legacy_id: %w", err)
	}
	if n == 0 {
		if _, err := tx.ExecContext(ctx, `ALTER TABLE memories ADD COLUMN legacy_id INTEGER`); err != nil {
			return fmt.Errorf("add memories.legacy_id: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `CREATE UNIQUE INDEX IF NOT EXISTS idx_memories_legacy_id
		ON memories(legacy_id) WHERE legacy_id IS NOT NULL`); err != nil {
		return fmt.Errorf("index memories.legacy_id: %w", err)
	}
	return nil
}

func compactOversizedIDs(ctx context.Context, tx *sql.Tx) error {
	var n int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM memories WHERE id > ?`, jsSafeInteger).Scan(&n); err != nil {
		return fmt.Errorf("count oversized memory ids: %w", err)
	}
	if n == 0 {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `CREATE TEMP TABLE memory_id_map (
		old INTEGER PRIMARY KEY,
		new INTEGER UNIQUE NOT NULL
	)`); err != nil {
		return fmt.Errorf("create memory id map: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO memory_id_map(old, new)
		SELECT id, ROW_NUMBER() OVER (ORDER BY created_at, id) FROM memories`); err != nil {
		return fmt.Errorf("assign compact memory ids: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE memories SET id = -id`); err != nil {
		return fmt.Errorf("park memory ids: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE memories SET
		id = (SELECT new FROM memory_id_map WHERE old = -memories.id),
		legacy_id = (SELECT old FROM memory_id_map WHERE old = -memories.id)`); err != nil {
		return fmt.Errorf("apply compact memory ids: %w", err)
	}
	if err := remapMemoryAliases(ctx, tx); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE memories SET supersedes = (
		COALESCE((SELECT new FROM memory_id_map WHERE old = memories.supersedes), memories.supersedes))
		WHERE supersedes IS NOT NULL`); err != nil {
		return fmt.Errorf("remap supersedes: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DROP TABLE memory_id_map`); err != nil {
		return fmt.Errorf("drop memory id map: %w", err)
	}
	return nil
}

func remapMemoryAliases(ctx context.Context, tx *sql.Tx) error {
	var present int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'memory_id_remaps'`).Scan(&present); err != nil {
		return fmt.Errorf("inspect memory id remaps: %w", err)
	}
	if present == 0 {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE memory_id_remaps SET canonical_id = COALESCE(
		(SELECT new FROM memory_id_map WHERE old = memory_id_remaps.canonical_id), canonical_id)`); err != nil {
		return fmt.Errorf("remap memory id aliases: %w", err)
	}
	return nil
}

func resetSequence(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM sqlite_sequence WHERE name = 'memories'`); err != nil {
		return fmt.Errorf("clear memories sqlite_sequence: %w", err)
	}
	var max sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT MAX(id) FROM memories`).Scan(&max); err != nil {
		return fmt.Errorf("read max memory id: %w", err)
	}
	if !max.Valid || max.Int64 <= 0 {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO sqlite_sequence(name, seq) VALUES ('memories', ?)`, max.Int64); err != nil {
		return fmt.Errorf("restore memories sqlite_sequence: %w", err)
	}
	return nil
}

func dropMemoryFTSTriggers(tx *sql.Tx) error {
	for _, name := range []string{"memories_ai", "memories_ad", "memories_au"} {
		if _, err := tx.Exec(`DROP TRIGGER IF EXISTS ` + name); err != nil {
			return fmt.Errorf("drop %s: %w", name, err)
		}
	}
	return nil
}
