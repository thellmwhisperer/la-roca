// Package telemetry records local-only operational facts about the embedding
// engine: load, pre-warm, per-query embed, batch throughput, backend, memory
// high-water, and errors. It never stores query or document text.
package telemetry

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

const (
	KindLoad    = "load"
	KindPrewarm = "prewarm"
	KindEmbed   = "embed"
	KindBatch   = "batch"
	KindError   = "error"
	Filename    = "engine.db"
)

type Record struct {
	Kind       string
	Backend    string
	Fallback   string
	DurationMS int64
	BatchSize  int
	Throughput float64
	MemoryHWM  int64
	Err        string
}

type Store struct {
	db *sql.DB
}

func Path(stateDir string) string {
	return filepath.Join(stateDir, Filename)
}

func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create engine telemetry directory: %w", err)
	}
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, fmt.Errorf("open engine telemetry: %w", err)
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply engine telemetry schema: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) Record(ctx context.Context, record Record) error {
	if s == nil || s.db == nil {
		return nil
	}
	if record.Kind == "" {
		return fmt.Errorf("engine telemetry kind is required")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO engine_telemetry(
		recorded_at, kind, backend, fallback_reason, duration_ms, batch_size, throughput,
		memory_hwm_bytes, error) VALUES (?,?,?,?,?,?,?,?,?)`,
		time.Now().UTC().Format(time.RFC3339Nano), record.Kind, record.Backend, record.Fallback,
		record.DurationMS, record.BatchSize, record.Throughput, record.MemoryHWM, record.Err)
	if err != nil {
		return fmt.Errorf("record engine telemetry: %w", err)
	}
	return nil
}

func (s *Store) Query(ctx context.Context, statement string) ([]map[string]any, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("engine telemetry is closed")
	}
	rows, err := s.db.QueryContext(ctx, statement)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	var results []map[string]any
	for rows.Next() {
		values := make([]any, len(columns))
		dest := make([]any, len(columns))
		for i := range values {
			dest[i] = &values[i]
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		row := make(map[string]any, len(columns))
		for i, column := range columns {
			switch value := values[i].(type) {
			case []byte:
				row[column] = string(value)
			default:
				row[column] = value
			}
		}
		results = append(results, row)
	}
	return results, rows.Err()
}

const schema = `
CREATE TABLE IF NOT EXISTS engine_telemetry (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  recorded_at TEXT NOT NULL,
  kind TEXT NOT NULL,
  backend TEXT NOT NULL DEFAULT '',
  fallback_reason TEXT NOT NULL DEFAULT '',
  duration_ms INTEGER NOT NULL DEFAULT 0,
  batch_size INTEGER NOT NULL DEFAULT 0,
  throughput REAL NOT NULL DEFAULT 0,
  memory_hwm_bytes INTEGER NOT NULL DEFAULT 0,
  error TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_engine_telemetry_kind_time ON engine_telemetry(kind, recorded_at);
`
