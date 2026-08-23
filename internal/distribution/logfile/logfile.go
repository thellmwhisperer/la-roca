// Package logfile writes the product's credential-safe JSONL traces.
package logfile

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/thellmwhisperer/la-roca/internal/distribution/callhistory"
	"github.com/thellmwhisperer/la-roca/internal/securefile"
)

const (
	DirName       = "logs"
	RetentionDays = 30
	Executions    = "executions"
	MCPAudit      = "mcp-audit"
	Ingest        = "ingest"
	Migrations    = "migrations"
	Companions    = "companions"
	lockName      = ".roca.lock"
	maxFileBytes  = int64(5 << 20)
	maxFiles      = 6
)

type Writer struct {
	dir          string
	opsDatabase  string
	now          func() time.Time
	maxFileBytes int64
	maxFiles     int
}

// CallRecord is the stable common contract shared by every CLI command and MCP
// tool call. Query-only fields stay empty for other calls, while row_count is
// always present so consumers never have to infer whether zero was omitted.
type CallRecord struct {
	CallID        string    `json:"call_id,omitempty"`
	Timestamp     time.Time `json:"timestamp"`
	Source        string    `json:"source"`
	Args          any       `json:"args"`
	OK            bool      `json:"ok"`
	Error         string    `json:"error,omitempty"`
	ErrorType     string    `json:"error_type,omitempty"`
	DurationMS    int64     `json:"duration_ms"`
	CorrelationID string    `json:"correlation_id,omitempty"`

	Question       string  `json:"question,omitempty"`
	SQL            string  `json:"sql,omitempty"`
	RawSQL         string  `json:"raw_sql,omitempty"`
	SQLProvider    string  `json:"sql_provider,omitempty"`
	SQLModel       string  `json:"sql_model,omitempty"`
	RowCount       int     `json:"row_count"`
	Degraded       string  `json:"degraded,omitempty"`
	FallbackReason string  `json:"fallback_reason,omitempty"`
	RetryReason    string  `json:"retry_reason,omitempty"`
	QueryPlan      any     `json:"queryplan,omitempty"`
	ProviderNote   string  `json:"sql_provider_note,omitempty"`
	Path           string  `json:"path,omitempty"`
	Retried        bool    `json:"retried,omitempty"`
	RetriedSQL     bool    `json:"retried_sql,omitempty"`
	RetryType      string  `json:"retry_type,omitempty"`
	ModelSQL       *string `json:"model_sql,omitempty"`
	FirstModelSQL  string  `json:"first_model_sql,omitempty"`

	SQLProviderLatencyMS      *int64 `json:"sql_provider_latency_ms,omitempty"`
	SQLInferenceMS            *int64 `json:"sql_inference_ms,omitempty"`
	SQLRetryProviderLatencyMS *int64 `json:"sql_retry_provider_latency_ms,omitempty"`
	SQLRetryInferenceMS       *int64 `json:"sql_retry_inference_ms,omitempty"`
	ExecutionMS               *int64 `json:"execution_ms,omitempty"`
	InterpretationProvider    string `json:"interpretation_provider,omitempty"`
	InterpretationModel       string `json:"interpretation_model,omitempty"`
	InterpretationMS          *int64 `json:"interpretation_ms,omitempty"`
}

type ExecutionRecord struct {
	CallRecord
	Command      string         `json:"command"`
	Flags        map[string]any `json:"flags"`
	DatabasePath string         `json:"database_path,omitempty"`
	ExitCode     int            `json:"exit_code"`
	Result       any            `json:"result,omitempty"`
}

type MCPRecord struct {
	CallRecord
	Tool string `json:"tool"`
}

type RunRecord struct {
	Timestamp  time.Time `json:"timestamp"`
	OK         bool      `json:"ok"`
	DurationMS int64     `json:"duration_ms"`
	Error      string    `json:"error,omitempty"`
	ErrorType  string    `json:"error_type,omitempty"`
	Result     any       `json:"result"`
}

type IngestRecord = RunRecord
type MigrationRecord = RunRecord

func New(dataDir string) *Writer {
	return &Writer{
		dir: filepath.Join(dataDir, DirName), now: time.Now,
		maxFileBytes: maxFileBytes, maxFiles: maxFiles,
	}
}

func (w *Writer) Append(stream string, record any) error {
	return w.append(stream, record, true)
}

func (w *Writer) AppendExisting(stream string, record any) error {
	return w.append(stream, record, false)
}

func (w *Writer) Prepare() error {
	release, err := w.Lock()
	if err != nil {
		return err
	}
	if err := release(); err != nil {
		return fmt.Errorf("release the log lock: %w", err)
	}
	return nil
}

func (w *Writer) Lock() (func() error, error) {
	if w == nil || w.dir == "" {
		return nil, fmt.Errorf("the log directory is not configured")
	}
	if err := os.MkdirAll(w.dir, 0o700); err != nil {
		return nil, fmt.Errorf("create the log directory: %w", err)
	}
	release, err := securefile.Lock(w.LockPath())
	if err != nil {
		return nil, fmt.Errorf("lock the log directory: %w", err)
	}
	return release, nil
}

func (w *Writer) LockPath() string {
	if w == nil {
		return ""
	}
	return filepath.Join(w.dir, lockName)
}

func (w *Writer) append(stream string, record any, createDir bool) error {
	if w == nil || w.dir == "" {
		return fmt.Errorf("the log directory is not configured")
	}
	now := w.now().UTC()
	path := filepath.Join(w.dir, stream+"-"+now.Format(time.DateOnly)+".jsonl")
	line, err := json.Marshal(Redact(record))
	if err != nil {
		return fmt.Errorf("encode the %s log record: %w", stream, err)
	}
	if int64(len(line)+1) > w.maxFileBytes {
		return fmt.Errorf("encode the %s log record: %d bytes exceeds the %d-byte record cap",
			stream, len(line)+1, w.maxFileBytes)
	}
	sourceLine := 0
	// Only the two call streams carry a durable twin, and only an installation
	// that has the ops database needs the line that identifies it. Everything
	// else must not pay a second full read of the segment to learn a number
	// nobody consumes.
	durable := (stream == Executions || stream == MCPAudit) &&
		callhistory.Available(w.opsDatabase)
	var historyErr error
	fileErr := func() error {
		if createDir {
			if err := w.Prepare(); err != nil {
				return err
			}
		}
		release, err := securefile.LockExisting(w.LockPath())
		if err != nil {
			return fmt.Errorf("lock the log directory: %w", err)
		}
		defer release()
		if info, err := os.Stat(w.dir); err != nil || !info.IsDir() {
			if err == nil {
				err = fmt.Errorf("not a directory")
			}
			return fmt.Errorf("open the log directory: %w", err)
		}
		rotating, err := w.needsRotation(path, int64(len(line)+1))
		if err != nil {
			return fmt.Errorf("inspect the %s log for rotation: %w", stream, err)
		}
		if rotating && durable {
			historyErr = w.backfillLocked(context.Background())
		}
		if err := w.rotate(path, int64(len(line)+1)); err != nil {
			return fmt.Errorf("rotate the %s log: %w", stream, err)
		}
		if durable {
			sourceLine, err = nextSourceLine(path)
			if err != nil {
				return fmt.Errorf("inspect the %s log: %w", stream, err)
			}
			line, err = withCallID(stream, filepath.Base(path), sourceLine, line)
			if err != nil {
				return fmt.Errorf("identify the %s log record: %w", stream, err)
			}
			if int64(len(line)+1) > w.maxFileBytes {
				return fmt.Errorf("encode the %s log record: %d bytes exceeds the %d-byte record cap",
					stream, len(line)+1, w.maxFileBytes)
			}
		}
		file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return fmt.Errorf("open the %s log: %w", stream, err)
		}
		_, writeErr := file.Write(append(line, '\n'))
		closeErr := file.Close()
		if writeErr != nil {
			return fmt.Errorf("append the %s log: %w", stream, writeErr)
		}
		if closeErr != nil {
			return fmt.Errorf("close the %s log: %w", stream, closeErr)
		}
		// The record is written and closed, so the append succeeded. Rotation is
		// housekeeping after the fact: a file that could not be removed is not a
		// reason to tell the caller its trace was not written.
		w.prune(stream, now)
		return nil
	}()
	databaseErr := errors.Join(historyErr,
		w.persistCall(stream, filepath.Base(path), sourceLine, line))
	return combineLogErrors(fileErr, databaseErr)
}

func nextSourceLine(path string) (int, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return 1, nil
	}
	if err != nil {
		return 0, err
	}
	return bytes.Count(raw, []byte{'\n'}) + 1, nil
}

func (w *Writer) needsRotation(path string, incoming int64) (bool, error) {
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return info.Size() != 0 && info.Size()+incoming > w.maxFileBytes, nil
}

func (w *Writer) rotate(path string, incoming int64) error {
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Size() == 0 || info.Size()+incoming <= w.maxFileBytes {
		return nil
	}
	stem := strings.TrimSuffix(path, ".jsonl")
	for sequence := 1; ; sequence++ {
		archive := fmt.Sprintf("%s-%d.jsonl", stem, sequence)
		if _, err := os.Stat(archive); os.IsNotExist(err) {
			return os.Rename(path, archive)
		} else if err != nil {
			return err
		}
	}
}

// prune drops the dated files past the retention window. It reports nothing: one
// file it cannot remove must not stop it from removing the rest, and it must not
// become the verdict of the append that called it.
func (w *Writer) prune(stream string, now time.Time) {
	matches, err := filepath.Glob(filepath.Join(w.dir, stream+"-*.jsonl"))
	if err != nil {
		return
	}
	cutoff := now.AddDate(0, 0, -(RetentionDays - 1)).Format(time.DateOnly)
	for _, path := range matches {
		name := strings.TrimPrefix(filepath.Base(path), stream+"-")
		if len(name) < len(time.DateOnly) {
			continue
		}
		date := name[:len(time.DateOnly)]
		if _, err := time.Parse(time.DateOnly, date); err != nil || date >= cutoff {
			continue
		}
		os.Remove(path)
	}
	matches, err = filepath.Glob(filepath.Join(w.dir, stream+"-*.jsonl"))
	if err != nil || len(matches) <= w.maxFiles {
		return
	}
	sort.Slice(matches, func(i, j int) bool {
		left, leftErr := os.Stat(matches[i])
		right, rightErr := os.Stat(matches[j])
		if leftErr != nil || rightErr != nil || left.ModTime().Equal(right.ModTime()) {
			return matches[i] < matches[j]
		}
		return left.ModTime().Before(right.ModTime())
	})
	for _, path := range matches[:len(matches)-w.maxFiles] {
		os.Remove(path)
	}
}

var (
	secretKey = regexp.MustCompile(`(?i)(api[_-]?key|access[_-]?key|private[_-]?key|` +
		`signing[_-]?key|session[_-]?key|token|password|passwd|secret|credential|` +
		`authorization|cookie)`)
	secretText = []*regexp.Regexp{
		regexp.MustCompile(`(?i)\b(bearer\s+)[A-Za-z0-9._~+/=-]+`),
		regexp.MustCompile(`(?i)\b(api[_-]?key|token|password|passwd|secret)\s*[:=]\s*[^\s,;]+`),
		regexp.MustCompile(`\b(sk-[A-Za-z0-9_-]{8,}|gh[pousr]_[A-Za-z0-9_]{8,}|github_pat_[A-Za-z0-9_]{20,}|xox[baprs]-[A-Za-z0-9-]{8,})\b`),
		regexp.MustCompile(`\beyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\b`),
		// ASIA beside AKIA: a temporary AWS session credential is the same shape
		// and the same secret as a long-lived access key id.
		regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`),
		regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}\b`),
		regexp.MustCompile(`(?s)-----BEGIN [^-]*PRIVATE KEY-----.*?-----END [^-]*PRIVATE KEY-----`),
	}
)

func SensitiveName(name string) bool { return secretKey.MatchString(name) }

func Redact(value any) any {
	raw, err := json.Marshal(value)
	if err != nil {
		return map[string]any{"error": "unencodable log record"}
	}
	var document any
	if err := json.Unmarshal(raw, &document); err != nil {
		return map[string]any{"error": "unencodable log record"}
	}
	return redactValue("", document)
}

func redactValue(key string, value any) any {
	if SensitiveName(key) {
		return "[REDACTED]"
	}
	switch typed := value.(type) {
	case map[string]any:
		for field, item := range typed {
			typed[field] = redactValue(field, item)
		}
		return typed
	case []any:
		for index, item := range typed {
			typed[index] = redactValue(key, item)
		}
		return typed
	case string:
		for _, pattern := range secretText {
			typed = pattern.ReplaceAllStringFunc(typed, redactSecretText)
		}
		return typed
	default:
		return value
	}
}

func redactSecretText(match string) string {
	lower := strings.ToLower(match)
	if strings.HasPrefix(lower, "bearer ") {
		return match[:7] + "[REDACTED]"
	}
	if index := strings.IndexAny(match, ":="); index >= 0 {
		return strings.TrimSpace(match[:index+1]) + "[REDACTED]"
	}
	return "[REDACTED]"
}
