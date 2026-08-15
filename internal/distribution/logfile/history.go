package logfile

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/thellmwhisperer/la-roca/internal/distribution/callhistory"
	"github.com/thellmwhisperer/la-roca/internal/securefile"
)

func NewWithOps(dataDir, database string) *Writer {
	writer := New(dataDir)
	writer.opsDatabase = database
	return writer
}

func (w *Writer) Backfill() error {
	if w == nil || !callhistory.Available(w.opsDatabase) {
		return nil
	}
	release, err := securefile.LockExisting(w.LockPath())
	if err != nil {
		if _, statErr := os.Stat(w.dir); os.IsNotExist(statErr) {
			return callhistory.ImportSegments(context.Background(), w.opsDatabase,
				callhistory.Import{CheckedAt: w.now().UTC()})
		}
		return fmt.Errorf("lock the log directory: %w", err)
	}
	defer func() { _ = release() }()
	return w.backfillLocked(context.Background())
}

func (w *Writer) BackfillIfNeeded() error {
	if w == nil || !callhistory.Available(w.opsDatabase) {
		return nil
	}
	present, err := callhistory.HasParityState(context.Background(), w.opsDatabase)
	if err != nil || present {
		return err
	}
	return w.Backfill()
}

func (w *Writer) backfillLocked(ctx context.Context) error {
	if w == nil || !callhistory.Available(w.opsDatabase) {
		return nil
	}
	source := callhistory.Import{CheckedAt: w.now().UTC()}
	imported, err := callhistory.ImportedSegments(ctx, w.opsDatabase)
	if err != nil {
		return err
	}
	for _, stream := range []string{Executions, MCPAudit} {
		matches, err := filepath.Glob(filepath.Join(w.dir, stream+"-*.jsonl"))
		if err != nil {
			source.Unreadable++
			continue
		}
		sort.Strings(matches)
		for _, path := range matches {
			segment, readable := readHistorySegment(stream, path, imported)
			// A segment whose read stopped short is unreadable and stays out of the
			// import: recording its digest would claim the unread tail as imported
			// and let parity arm over a hole.
			if !readable || segment.LineCount < 0 {
				source.Unreadable++
				continue
			}
			source.Segments = append(source.Segments, segment)
		}
	}
	return callhistory.ImportSegments(ctx, w.opsDatabase, source)
}

func readHistorySegment(stream, path string,
	imported callhistory.Imported) (callhistory.Segment, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return callhistory.Segment{}, false
	}
	digest := sha256.Sum256(raw)
	segment := callhistory.Segment{
		Stream: stream, SourceFile: filepath.Base(path), ByteSize: int64(len(raw)),
		Digest: hex.EncodeToString(digest[:]),
	}
	if previous, exists := imported.ByDigest[segment.Digest]; exists && previous.ByteSize == segment.ByteSize {
		segment.LineCount, segment.Parsed, segment.Malformed =
			previous.LineCount, previous.Parsed, previous.Malformed
		segment.Unchanged = previous.SourceFile == segment.SourceFile
		return segment, true
	}
	offset := resumeOffset(raw, imported.ByFile[segment.SourceFile])
	if offset > 0 {
		previous := imported.ByFile[segment.SourceFile]
		segment.LineCount, segment.Parsed, segment.Malformed =
			previous.LineCount, previous.Parsed, previous.Malformed
	}
	scanner := bufio.NewScanner(bytes.NewReader(raw[offset:]))
	scanner.Buffer(make([]byte, 64*1024), int(maxFileBytes)+1)
	for scanner.Scan() {
		segment.LineCount++
		record, err := durableRecord(stream, segment.SourceFile, segment.LineCount, scanner.Bytes())
		if err != nil {
			segment.Malformed++
			continue
		}
		segment.Parsed++
		segment.Records = append(segment.Records, record)
	}
	if scanner.Err() != nil {
		segment.LineCount = -1
	}
	return segment, true
}

// resumeOffset is the byte a growing segment resumes at. The prefix a previous
// backfill read must still be the same bytes and must end a line, so the tail
// keeps the source-line identities the stored rows already hold; anything else
// is read from zero.
func resumeOffset(raw []byte, previous callhistory.Segment) int64 {
	if previous.Digest == "" || previous.LineCount < 0 ||
		previous.ByteSize <= 0 || previous.ByteSize >= int64(len(raw)) ||
		raw[previous.ByteSize-1] != '\n' {
		return 0
	}
	prefix := sha256.Sum256(raw[:previous.ByteSize])
	if hex.EncodeToString(prefix[:]) != previous.Digest {
		return 0
	}
	return previous.ByteSize
}

func durableRecord(stream, sourceFile string, sourceLine int, raw []byte) (callhistory.Record, error) {
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		return callhistory.Record{}, err
	}
	redacted, ok := Redact(document).(map[string]any)
	if !ok {
		return callhistory.Record{}, fmt.Errorf("redacted call is not an object")
	}
	payload, err := json.Marshal(redacted)
	if err != nil {
		return callhistory.Record{}, err
	}
	var call CallRecord
	if err := json.Unmarshal(payload, &call); err != nil {
		return callhistory.Record{}, err
	}
	args, err := json.Marshal(redacted["args"])
	if err != nil {
		return callhistory.Record{}, err
	}
	var queryPlan json.RawMessage
	if value, exists := redacted["queryplan"]; exists {
		queryPlan, err = json.Marshal(value)
		if err != nil {
			return callhistory.Record{}, err
		}
	}
	operation, _ := redacted["command"].(string)
	if stream == MCPAudit {
		operation, _ = redacted["tool"].(string)
	}
	sourceName := call.Source
	if sourceName == "" {
		sourceName = "cli"
		if stream == MCPAudit {
			sourceName = "mcp"
		}
	}
	callOK := call.OK
	if _, exists := redacted["ok"]; !exists {
		exitCode, _ := redacted["exit_code"].(float64)
		callOK = exitCode == 0
	}
	record := callhistory.Record{
		Stream: stream, Source: sourceName, Operation: operation,
		SourceFile: sourceFile, SourceLine: sourceLine, Timestamp: call.Timestamp,
		Args: args, QueryPlan: queryPlan, RecordJSON: payload, OK: callOK,
		Error: call.Error, ErrorType: call.ErrorType, DurationMS: call.DurationMS,
		CorrelationID: call.CorrelationID, Question: call.Question, SQL: call.SQL,
		RawSQL: call.RawSQL, SQLProvider: call.SQLProvider, SQLModel: call.SQLModel,
		RowCount: call.RowCount, Degraded: call.Degraded,
		FallbackReason: call.FallbackReason, RetryReason: call.RetryReason,
		ProviderNote: call.ProviderNote, Path: call.Path, Retried: call.Retried,
		RetriedSQL: call.RetriedSQL, RetryType: call.RetryType,
		ModelSQL: call.ModelSQL, FirstModelSQL: call.FirstModelSQL,
		SQLProviderLatencyMS:      call.SQLProviderLatencyMS,
		SQLInferenceMS:            call.SQLInferenceMS,
		SQLRetryProviderLatencyMS: call.SQLRetryProviderLatencyMS,
		SQLRetryInferenceMS:       call.SQLRetryInferenceMS, ExecutionMS: call.ExecutionMS,
		InterpretationProvider: call.InterpretationProvider,
		InterpretationModel:    call.InterpretationModel,
		InterpretationMS:       call.InterpretationMS,
	}
	var legacy queryFailureRecord
	if err := json.Unmarshal(payload, &legacy); err == nil {
		if failure, failedQuery := legacy.queryFailure(time.Time{}); failedQuery {
			record.Source, record.Operation = failure.Source, failure.Operation
			record.Question, record.Error = failure.Question, failure.Error
			record.ErrorType = failure.ErrorType
		}
	}
	record.ID = call.CallID
	if record.ID == "" {
		record.ID = callhistory.RecordID(call.CorrelationID, stream, sourceFile, sourceLine, payload)
	}
	record.RecordDigest = callhistory.PayloadDigest(payload)
	return record, nil
}

func withCallID(stream, sourceFile string, sourceLine int, payload []byte) ([]byte, error) {
	var document map[string]any
	if err := json.Unmarshal(payload, &document); err != nil {
		return nil, err
	}
	if _, exists := document["call_id"]; !exists {
		correlationID, _ := document["correlation_id"].(string)
		document["call_id"] = callhistory.RecordID(
			correlationID, stream, sourceFile, sourceLine, payload)
	}
	return json.Marshal(document)
}

func (w *Writer) persistCall(stream, sourceFile string, sourceLine int, payload []byte) error {
	if stream != Executions && stream != MCPAudit || !callhistory.Available(w.opsDatabase) {
		return nil
	}
	record, err := durableRecord(stream, sourceFile, sourceLine, payload)
	if err != nil {
		return fmt.Errorf("prepare the durable %s call: %w", stream, err)
	}
	return callhistory.Persist(context.Background(), w.opsDatabase, record)
}

func (w *Writer) recentDurableQueryFailures(now time.Time, window time.Duration,
	limit int) (QueryFailureSummary, bool, error) {
	if w == nil || !callhistory.Available(w.opsDatabase) {
		return QueryFailureSummary{}, false, nil
	}
	if err := w.Backfill(); err != nil {
		return QueryFailureSummary{}, false, err
	}
	since := now.UTC().Add(-window)
	durable, ready, err := callhistory.RecentQueryFailures(
		context.Background(), w.opsDatabase, now, window, limit,
		func(sourceFile, stream string) bool { return reaches(sourceFile, stream, since) })
	if err != nil || !ready {
		return QueryFailureSummary{}, ready, err
	}
	summary := QueryFailureSummary{
		Since: durable.Since, Count: durable.Count, Malformed: durable.Malformed,
		Unreadable: durable.Unreadable,
	}
	for _, failure := range durable.Recent {
		summary.Recent = append(summary.Recent, QueryFailure{
			Timestamp: failure.Timestamp, Source: failure.Source,
			Operation: failure.Operation, Question: failure.Question,
			Error: failure.Error, ErrorType: failure.ErrorType,
			CorrelationID: failure.CorrelationID,
		})
	}
	return summary, true, nil
}

// durableSinkError marks an append the JSONL sink completed and only the
// durable sink refused. Callers warn on any append error, but one that carries
// this marker still has its record on disk, so what the caller shows its user
// must not change because the other sink failed.
type durableSinkError struct{ err error }

func (e durableSinkError) Error() string { return e.err.Error() }

func (e durableSinkError) Unwrap() error { return e.err }

// FileAppendFailed answers whether the JSONL record itself was not written. It
// is false for a nil error and for a durable-only failure.
func FileAppendFailed(err error) bool {
	if err == nil {
		return false
	}
	var durable durableSinkError
	return !errors.As(err, &durable)
}

func combineLogErrors(fileErr, databaseErr error) error {
	switch {
	case fileErr == nil && databaseErr == nil:
		return nil
	case fileErr == nil:
		return durableSinkError{databaseErr}
	case databaseErr == nil:
		return fileErr
	default:
		return errors.Join(fileErr, databaseErr)
	}
}
