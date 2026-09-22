package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/thellmwhisperer/la-roca/internal/recall"
)

// recallLogPath is derived from the configured source home so tests and
// disposable installations never read the process owner's live home by
// accident.
func (s *Service) recallLogPath() string {
	if s == nil || s.opts.Sources.Home == "" {
		return ""
	}
	return filepath.Join(s.opts.Sources.Home, ".roca", "logs", "recall.jsonl")
}

// ingestRecallEvents imports the hook's JSONL audit into the ops-owned table.
// It is deliberately separate from conversation ingest: recall is operational
// provenance, not corpus content, and must remain queryable through its owner.
func (s *Service) ingestRecallEvents(ctx context.Context) (int, error) {
	if s == nil || s.ops == nil || s.opts.ReadOnly {
		return 0, nil
	}
	path := s.recallLogPath()
	if path == "" {
		return 0, nil
	}
	events, err := recall.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read recall provenance: %w", err)
	}
	if len(events) == 0 {
		return 0, nil
	}
	written := 0
	err = s.ops.Write(ctx, func(tx *sql.Tx) error {
		const statement = `INSERT OR IGNORE INTO recall_events
			(ts, action, tool, query_sha, query, session_id, exchange_id,
			 raw, hits, top_score, ids, scores, dates, cands, elapsed_ms, event_sha)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
		for _, event := range events {
			ids, err := json.Marshal(event.IDs)
			if err != nil {
				return fmt.Errorf("encode recall hit ids: %w", err)
			}
			scores, err := json.Marshal(event.Scores)
			if err != nil {
				return fmt.Errorf("encode recall scores: %w", err)
			}
			dates, err := json.Marshal(event.Dates)
			if err != nil {
				return fmt.Errorf("encode recall dates: %w", err)
			}
			result, err := tx.ExecContext(ctx, statement,
				event.TS, event.Action, nullableString(event.Tool), event.QuerySHA,
				nullableString(event.Query), nullableString(event.SessionID), event.ExchangeID,
				event.Raw, event.Hits, event.TopScore, string(ids), string(scores), string(dates),
				event.Cands, event.ElapsedMS, event.EventSHA)
			if err != nil {
				return fmt.Errorf("store recall provenance: %w", err)
			}
			if count, err := result.RowsAffected(); err == nil {
				written += int(count)
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return written, nil
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}
