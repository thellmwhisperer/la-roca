// Package callhistory owns durable redacted call records in the bundled ops database.
package callhistory

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/thellmwhisperer/la-roca/internal/distribution/bundledplugin"
)

type Record struct {
	ID, Stream, Source, Operation, SourceFile, RecordDigest string
	SourceLine                                              int
	Timestamp                                               time.Time
	Args, QueryPlan, RecordJSON                             json.RawMessage
	OK                                                      bool
	Error, ErrorType                                        string
	DurationMS                                              int64
	CorrelationID                                           string
	Question, SQL, RawSQL, SQLProvider, SQLModel            string
	RowCount                                                int
	Degraded, FallbackReason, RetryReason                   string
	ProviderNote, Path, RetryType, FirstModelSQL            string
	Retried, RetriedSQL                                     bool
	ModelSQL                                                *string
	SQLProviderLatencyMS, SQLInferenceMS                    *int64
	SQLRetryProviderLatencyMS, SQLRetryInferenceMS          *int64
	ExecutionMS, InterpretationMS                           *int64
	InterpretationProvider, InterpretationModel             string
}

type Segment struct {
	Stream, SourceFile, Digest   string
	ByteSize                     int64
	LineCount, Parsed, Malformed int
	Records                      []Record
	Unchanged                    bool
}

type Import struct {
	Segments   []Segment
	Unreadable int
	CheckedAt  time.Time
}

// Imported is what a previous backfill already recorded. ByDigest answers
// whether a whole file is already in, including under an older name after
// rotation; ByFile answers how much of a file that is still growing was read,
// so the next backfill resumes at that byte instead of parsing from zero.
type Imported struct {
	ByDigest map[string]Segment
	ByFile   map[string]Segment
}

type QueryFailure struct {
	Timestamp     time.Time
	Source        string
	Operation     string
	Question      string
	Error         string
	ErrorType     string
	CorrelationID string
}

type FailureSummary struct {
	Since      time.Time
	Count      int
	Recent     []QueryFailure
	Malformed  int
	Unreadable int
}

func Available(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func RecordID(correlationID, stream, sourceFile string, sourceLine int, payload []byte) string {
	if correlationID != "" {
		return correlationID
	}
	digest := sha256.New()
	fmt.Fprintf(digest, "%s\x00%s\x00%d\x00", stream, sourceFile, sourceLine)
	digest.Write(payload)
	return hex.EncodeToString(digest.Sum(nil))
}

func PayloadDigest(payload []byte) string {
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

func ImportedSegments(ctx context.Context, path string) (Imported, error) {
	imported := Imported{ByDigest: map[string]Segment{}, ByFile: map[string]Segment{}}
	if !Available(path) {
		return imported, nil
	}
	db, err := bundledplugin.OpenDatabase(path, true)
	if err != nil {
		return Imported{}, err
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, `SELECT source_file, stream, content_digest,
		byte_size, line_count, parsed_count, malformed_count
		FROM call_history_segments ORDER BY imported_at, source_file`)
	if err != nil {
		return Imported{}, fmt.Errorf("read call history segment identities: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var segment Segment
		if err := rows.Scan(&segment.SourceFile, &segment.Stream, &segment.Digest,
			&segment.ByteSize, &segment.LineCount, &segment.Parsed,
			&segment.Malformed); err != nil {
			return Imported{}, fmt.Errorf("scan call history segment identity: %w", err)
		}
		if _, exists := imported.ByDigest[segment.Digest]; !exists {
			imported.ByDigest[segment.Digest] = segment
		}
		imported.ByFile[segment.SourceFile] = segment
	}
	if err := rows.Err(); err != nil {
		return Imported{}, err
	}
	return imported, nil
}

func HasParityState(ctx context.Context, path string) (bool, error) {
	if !Available(path) {
		return false, nil
	}
	db, err := bundledplugin.OpenDatabase(path, true)
	if err != nil {
		return false, err
	}
	defer db.Close()
	var present bool
	if err := db.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM call_history_state WHERE singleton = 1)`).Scan(&present); err != nil {
		return false, fmt.Errorf("inspect call history parity: %w", err)
	}
	return present, nil
}

func Persist(ctx context.Context, path string, record Record) error {
	if !Available(path) {
		return nil
	}
	db, err := bundledplugin.OpenDatabase(path, false)
	if err != nil {
		return err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin durable call write: %w", err)
	}
	defer tx.Rollback()
	if err := insertRecord(ctx, tx, record); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit durable call write: %w", err)
	}
	return nil
}

func ImportSegments(ctx context.Context, path string, source Import) error {
	if !Available(path) {
		return nil
	}
	db, err := bundledplugin.OpenDatabase(path, false)
	if err != nil {
		return err
	}
	defer db.Close()
	malformed := 0
	for _, segment := range source.Segments {
		malformed += segment.Malformed
	}
	if err := setParity(ctx, db, false, malformed, source.Unreadable, source.CheckedAt); err != nil {
		return err
	}
	for _, segment := range source.Segments {
		if segment.Unchanged {
			continue
		}
		if err := importSegment(ctx, db, segment, source.CheckedAt); err != nil {
			return err
		}
	}
	return setParity(ctx, db, source.Unreadable == 0, malformed,
		source.Unreadable, source.CheckedAt)
}

func importSegment(ctx context.Context, db *sql.DB, segment Segment, checkedAt time.Time) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin call history segment %s: %w", segment.SourceFile, err)
	}
	defer tx.Rollback()
	for _, record := range segment.Records {
		if err := insertRecord(ctx, tx, record); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO call_history_segments
			(source_file, stream, content_digest, byte_size, line_count, parsed_count,
			 malformed_count, imported_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(source_file) DO UPDATE SET
			stream=excluded.stream, content_digest=excluded.content_digest,
			byte_size=excluded.byte_size, line_count=excluded.line_count,
			parsed_count=excluded.parsed_count, malformed_count=excluded.malformed_count,
			imported_at=excluded.imported_at`, segment.SourceFile, segment.Stream,
		segment.Digest, segment.ByteSize, segment.LineCount, segment.Parsed,
		segment.Malformed, formatTimestamp(checkedAt))
	if err != nil {
		return fmt.Errorf("record call history segment %s: %w", segment.SourceFile, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit call history segment %s: %w", segment.SourceFile, err)
	}
	return nil
}

func setParity(ctx context.Context, db *sql.DB, parity bool, malformed, unreadable int,
	checkedAt time.Time) error {
	_, err := db.ExecContext(ctx, `INSERT INTO call_history_state
		(singleton, parity_verified, malformed_lines, unreadable_files, checked_at)
		VALUES (1, ?, ?, ?, ?)
		ON CONFLICT(singleton) DO UPDATE SET
		parity_verified=excluded.parity_verified,
		malformed_lines=excluded.malformed_lines,
		unreadable_files=excluded.unreadable_files,
		checked_at=excluded.checked_at`, parity, malformed, unreadable,
		formatTimestamp(checkedAt))
	if err != nil {
		return fmt.Errorf("record call history parity: %w", err)
	}
	return nil
}

// RecentQueryFailures answers the durable half of doctor's failure history.
// reaches decides which retained segment a file name belongs to relative to the
// window, so the malformed count keeps the population the JSONL reader counts:
// the segments that can hold a record inside the window, not every segment the
// retention still keeps.
func RecentQueryFailures(ctx context.Context, path string, now time.Time,
	window time.Duration, limit int,
	reaches func(sourceFile, stream string) bool) (FailureSummary, bool, error) {
	summary := FailureSummary{Since: now.UTC().Add(-window)}
	if !Available(path) {
		return summary, false, nil
	}
	db, err := bundledplugin.OpenDatabase(path, true)
	if err != nil {
		return summary, false, err
	}
	defer db.Close()
	var parity bool
	if err := db.QueryRowContext(ctx, `SELECT parity_verified, malformed_lines,
		unreadable_files FROM call_history_state WHERE singleton = 1`).Scan(
		&parity, &summary.Malformed, &summary.Unreadable); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return summary, false, nil
		}
		return summary, false, fmt.Errorf("read call history parity: %w", err)
	}
	if !parity {
		return summary, false, nil
	}
	if reaches != nil {
		summary.Malformed, err = malformedInWindow(ctx, db, reaches)
		if err != nil {
			return summary, false, err
		}
	}
	const predicate = `timestamp >= ? AND ok = 0 AND
		((source = 'cli' AND operation IN ('query', 'explore')) OR
		 (source = 'mcp' AND operation IN ('roca_query', 'roca_explore', 'roca_sql')))`
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM call_history WHERE `+predicate,
		formatTimestamp(summary.Since)).Scan(&summary.Count); err != nil {
		return summary, false, fmt.Errorf("count durable query failures: %w", err)
	}
	query := `SELECT timestamp, source, operation, question, error, error_type,
		correlation_id FROM call_history WHERE ` + predicate + `
		ORDER BY timestamp DESC, id DESC`
	args := []any{formatTimestamp(summary.Since)}
	if limit >= 0 {
		query += " LIMIT ?"
		args = append(args, limit)
	}
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return summary, false, fmt.Errorf("read durable query failures: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var timestamp string
		var failure QueryFailure
		if err := rows.Scan(&timestamp, &failure.Source, &failure.Operation,
			&failure.Question, &failure.Error, &failure.ErrorType,
			&failure.CorrelationID); err != nil {
			return summary, false, fmt.Errorf("scan durable query failure: %w", err)
		}
		failure.Timestamp, err = time.Parse(time.RFC3339Nano, timestamp)
		if err != nil {
			return summary, false, fmt.Errorf("parse durable query failure timestamp: %w", err)
		}
		summary.Recent = append(summary.Recent, failure)
	}
	if err := rows.Err(); err != nil {
		return summary, false, fmt.Errorf("read durable query failures: %w", err)
	}
	return summary, true, nil
}

func malformedInWindow(ctx context.Context, db *sql.DB,
	reaches func(sourceFile, stream string) bool) (int, error) {
	rows, err := db.QueryContext(ctx, `SELECT source_file, stream, malformed_count
		FROM call_history_segments`)
	if err != nil {
		return 0, fmt.Errorf("read call history segment malformed counts: %w", err)
	}
	defer rows.Close()
	malformed := 0
	for rows.Next() {
		var sourceFile, stream string
		var count int
		if err := rows.Scan(&sourceFile, &stream, &count); err != nil {
			return 0, fmt.Errorf("scan call history segment malformed count: %w", err)
		}
		if reaches(sourceFile, stream) {
			malformed += count
		}
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("read call history segment malformed counts: %w", err)
	}
	return malformed, nil
}

func insertRecord(ctx context.Context, tx *sql.Tx, record Record) error {
	if record.ID == "" || record.Stream == "" || record.SourceFile == "" ||
		len(record.Args) == 0 || len(record.RecordJSON) == 0 {
		return fmt.Errorf("durable call record lacks its identity or JSON contract")
	}
	modelSQLPresent := record.ModelSQL != nil
	var modelSQL any
	if record.ModelSQL != nil {
		modelSQL = *record.ModelSQL
	}
	var queryPlan any
	if len(record.QueryPlan) > 0 {
		queryPlan = string(record.QueryPlan)
	}
	result, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO call_history (
		id, timestamp, stream, source, operation, args, ok, error, error_type,
		duration_ms, correlation_id, question, sql, raw_sql, sql_provider, sql_model,
		row_count, degraded, fallback_reason, retry_reason, queryplan, provider_note,
		path, retried, retried_sql, retry_type, model_sql, model_sql_present,
		first_model_sql, sql_provider_latency_ms, sql_inference_ms,
		sql_retry_provider_latency_ms, sql_retry_inference_ms, execution_ms,
		interpretation_provider, interpretation_model, interpretation_ms,
		source_file, source_line, record_digest, record_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?,
			?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		record.ID, formatTimestamp(record.Timestamp), record.Stream,
		record.Source, record.Operation, string(record.Args), record.OK, record.Error,
		record.ErrorType, record.DurationMS, record.CorrelationID, record.Question,
		record.SQL, record.RawSQL, record.SQLProvider, record.SQLModel, record.RowCount,
		record.Degraded, record.FallbackReason, record.RetryReason, queryPlan,
		record.ProviderNote, record.Path, record.Retried, record.RetriedSQL,
		record.RetryType, modelSQL, modelSQLPresent, record.FirstModelSQL,
		record.SQLProviderLatencyMS, record.SQLInferenceMS,
		record.SQLRetryProviderLatencyMS, record.SQLRetryInferenceMS,
		record.ExecutionMS, record.InterpretationProvider, record.InterpretationModel,
		record.InterpretationMS, record.SourceFile, record.SourceLine,
		record.RecordDigest, string(record.RecordJSON))
	if err != nil {
		return fmt.Errorf("write durable call record: %w", err)
	}
	inserted, err := result.RowsAffected()
	if err != nil || inserted != 0 {
		return err
	}
	var digest, sourceFile string
	var sourceLine int
	if err := tx.QueryRowContext(ctx, `SELECT record_digest, source_file, source_line
		FROM call_history WHERE id = ?`, record.ID).Scan(&digest, &sourceFile, &sourceLine); err != nil {
		return fmt.Errorf("verify durable call identity: %w", err)
	}
	// A stored row derived from the same retained line is the same call, so a
	// payload the current build redacts or encodes differently keeps the row it
	// already has. Only the same identity claimed by a different line is a real
	// collision, and that is what must stop the segment.
	if digest != record.RecordDigest &&
		(sourceFile != record.SourceFile || sourceLine != record.SourceLine) {
		return fmt.Errorf("call history identity %q has conflicting payloads", record.ID)
	}
	return nil
}

const timestampLayout = "2006-01-02T15:04:05.000000000Z"

func formatTimestamp(value time.Time) string {
	return value.UTC().Format(timestampLayout)
}
