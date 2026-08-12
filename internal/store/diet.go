package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
)

// DietReport is the measured result of removing storage inherited from a
// retired feature. BytesBefore and BytesAfter bracket the final VACUUM.
type DietReport struct {
	BytesBefore             int64 `json:"bytes_before"`
	BytesAfter              int64 `json:"bytes_after"`
	EmbeddingsDropped       bool  `json:"embeddings_dropped"`
	EmbeddingIndexesDropped int   `json:"embedding_indexes_dropped"`
	Vacuumed                bool  `json:"vacuumed"`
}

type dietPlan struct {
	embeddingIndexes int
}

func inspectDiet(ctx context.Context, db *DB) (*dietPlan, error) {
	var exists int
	if err := db.sql.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master
		WHERE type='table' AND name='embeddings'`).Scan(&exists); err != nil {
		return nil, fmt.Errorf("inspect the embeddings table: %w", err)
	}
	if exists == 0 {
		return nil, nil
	}
	rows, err := db.sql.QueryContext(ctx, `SELECT type, name FROM sqlite_master
		WHERE type IN ('view', 'trigger') AND lower(COALESCE(sql, '')) LIKE '%embeddings%'
		ORDER BY type, name`)
	if err != nil {
		return nil, fmt.Errorf("check references to the embeddings table: %w", err)
	}
	defer rows.Close()
	var references []string
	for rows.Next() {
		var kind, name string
		if err := rows.Scan(&kind, &name); err != nil {
			return nil, fmt.Errorf("read a reference to the embeddings table: %w", err)
		}
		references = append(references, kind+" "+name)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("check references to the embeddings table: %w", err)
	}
	if len(references) != 0 {
		return nil, fmt.Errorf("the embeddings table is still referenced by %s; it was not dropped",
			strings.Join(references, ", "))
	}
	plan := &dietPlan{}
	if err := db.sql.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master
		WHERE type='index' AND tbl_name='embeddings'`).Scan(&plan.embeddingIndexes); err != nil {
		return nil, fmt.Errorf("inspect the embeddings indexes: %w", err)
	}
	return plan, nil
}

func applyDiet(ctx context.Context, db *DB, plan *dietPlan) (*DietReport, error) {
	if plan == nil {
		return nil, nil
	}
	if _, err := db.sql.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		return nil, fmt.Errorf("checkpoint before dropping embeddings: %w", err)
	}
	report := &DietReport{
		BytesBefore:             databaseFileSize(db.path),
		EmbeddingIndexesDropped: plan.embeddingIndexes,
	}
	if err := db.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DROP TABLE embeddings`); err != nil {
			return fmt.Errorf("drop the unused embeddings table: %w", err)
		}
		return nil
	}); err != nil {
		return report, err
	}
	report.EmbeddingsDropped = true
	if _, err := db.sql.ExecContext(ctx, `VACUUM`); err != nil {
		return report, fmt.Errorf("vacuum after dropping embeddings: %w", err)
	}
	if _, err := db.sql.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		return report, fmt.Errorf("checkpoint after dropping embeddings: %w", err)
	}
	report.Vacuumed = true
	report.BytesAfter = databaseFileSize(db.path)
	return report, nil
}

func databaseFileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}
