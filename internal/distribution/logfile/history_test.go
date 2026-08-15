package logfile

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/thellmwhisperer/la-roca/internal/distribution/rocaops"
	_ "modernc.org/sqlite"
)

func TestDualWriteRoundTripsEveryCallFieldWithoutCredentials(t *testing.T) {
	root, database := historyFixture(t)
	value := int64(17)
	modelSQL := "SELECT content FROM memories"
	now := time.Date(2026, 8, 15, 12, 13, 14, 15, time.UTC)
	record := MCPRecord{CallRecord: CallRecord{
		Timestamp: now, Source: "mcp",
		Args: map[string]any{"query": "token=synthetic-secret", "api_key": "sk-synthetic123"},
		OK:   false, Error: "synthetic failure", ErrorType: "invalid_sql",
		DurationMS: 19, CorrelationID: "qf_synthetic_round_trip",
		Question: "find synthetic history", SQL: "SELECT content FROM memories LIMIT 1000",
		RawSQL:      "```sql\nSELECT content FROM memories\n```",
		SQLProvider: "codex", SQLModel: "gpt-synthetic", RowCount: 3,
		Degraded: "invalid_sql", FallbackReason: "invalid_sql",
		RetryReason: "synthetic rejection", QueryPlan: map[string]any{"route": "literal"},
		ProviderNote: "synthetic note", Path: "keyword", Retried: true,
		RetriedSQL: true, RetryType: "gate_rejection", ModelSQL: &modelSQL,
		FirstModelSQL:        "SELECT missing FROM memories",
		SQLProviderLatencyMS: &value, SQLInferenceMS: &value,
		SQLRetryProviderLatencyMS: &value, SQLRetryInferenceMS: &value,
		ExecutionMS: &value, InterpretationProvider: "ollama",
		InterpretationModel: "qwen-synthetic", InterpretationMS: &value,
	}, Tool: "roca_query"}
	writer := NewWithOps(root, database)
	writer.now = func() time.Time { return now }
	if err := writer.Append(MCPAudit, record); err != nil {
		t.Fatal(err)
	}

	db := openHistoryDB(t, database)
	defer db.Close()
	var id, storedJSON, args, queryPlan string
	var modelPresent bool
	if err := db.QueryRow(`SELECT id, record_json, args, queryplan, model_sql_present
		FROM call_history`).Scan(&id, &storedJSON, &args, &queryPlan, &modelPresent); err != nil {
		t.Fatal(err)
	}
	if id != record.CorrelationID || !modelPresent || queryPlan != `{"route":"literal"}` {
		t.Fatalf("durable identity/model/query plan = %q/%v/%s", id, modelPresent, queryPlan)
	}
	for _, secret := range []string{"synthetic-secret", "sk-synthetic123"} {
		if strings.Contains(storedJSON, secret) || strings.Contains(args, secret) {
			t.Fatalf("credential reached durable ops history: %s / %s", storedJSON, args)
		}
	}
	var got MCPRecord
	if err := json.Unmarshal([]byte(storedJSON), &got); err != nil {
		t.Fatal(err)
	}
	expectedRaw, err := json.Marshal(Redact(record))
	if err != nil {
		t.Fatal(err)
	}
	var expected MCPRecord
	if err := json.Unmarshal(expectedRaw, &expected); err != nil {
		t.Fatal(err)
	}
	expected.CallID = got.CallID
	if got.CallID == "" || !reflect.DeepEqual(got, expected) {
		t.Fatalf("call contract did not round-trip:\n got: %#v\nwant: %#v", got, expected)
	}
}

func TestRetainedSegmentsBackfillOnceAndGateDurableDoctorReads(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	legacy := New(root)
	legacy.now = func() time.Time { return now }
	if err := legacy.Append(Executions, ExecutionRecord{CallRecord: CallRecord{
		Timestamp: now.Add(-time.Hour), Source: "cli", Args: []string{"synthetic"},
		OK: false, Error: "synthetic invalid SQL", ErrorType: "invalid_sql",
		CorrelationID: "qf_backfill_failure",
	}, Command: "query", ExitCode: 1}); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Append(MCPAudit, MCPRecord{CallRecord: CallRecord{
		Timestamp: now, Source: "mcp", Args: map[string]any{}, OK: true,
	}, Tool: "roca_health"}); err != nil {
		t.Fatal(err)
	}
	executionPath := filepath.Join(root, DirName, "executions-2026-08-15.jsonl")
	file, err := os.OpenFile(executionPath, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("{malformed retained line}\n"); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	_, database := historyFixtureAt(t, root)
	writer := NewWithOps(root, database)
	writer.now = func() time.Time { return now }
	if err := writer.Backfill(); err != nil {
		t.Fatal(err)
	}

	db := openHistoryDB(t, database)
	defer db.Close()
	assertHistoryCount(t, db, 2)
	var importedAt string
	if err := db.QueryRow(`SELECT imported_at FROM call_history_segments
		WHERE source_file = 'mcp-audit-2026-08-15.jsonl'`).Scan(&importedAt); err != nil {
		t.Fatal(err)
	}
	writer.now = func() time.Time { return now.Add(time.Hour) }
	if err := writer.Backfill(); err != nil {
		t.Fatal(err)
	}
	var importedAgain string
	if err := db.QueryRow(`SELECT imported_at FROM call_history_segments
		WHERE source_file = 'mcp-audit-2026-08-15.jsonl'`).Scan(&importedAgain); err != nil {
		t.Fatal(err)
	}
	if importedAgain != importedAt {
		t.Fatalf("unchanged segment was re-imported: %q then %q", importedAt, importedAgain)
	}
	var parity bool
	var malformed, unreadable int
	if err := db.QueryRow(`SELECT parity_verified, malformed_lines, unreadable_files
		FROM call_history_state WHERE singleton = 1`).Scan(&parity, &malformed, &unreadable); err != nil {
		t.Fatal(err)
	}
	if !parity || malformed != 1 || unreadable != 0 {
		t.Fatalf("parity/malformed/unreadable = %v/%d/%d", parity, malformed, unreadable)
	}
	summary, err := writer.RecentQueryFailures(now, 24*time.Hour, 5)
	if err != nil || summary.Count != 1 || summary.Malformed != 1 ||
		len(summary.Recent) != 1 || summary.Recent[0].CorrelationID != "qf_backfill_failure" {
		t.Fatalf("durable doctor summary = %+v, err %v", summary, err)
	}

	mcpPath := filepath.Join(root, DirName, "mcp-audit-2026-08-15.jsonl")
	if err := os.Rename(mcpPath, filepath.Join(root, DirName, "mcp-audit-2026-08-15-1.jsonl")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Backfill(); err != nil {
		t.Fatal(err)
	}
	assertHistoryCount(t, db, 2)
}

func TestDurableFailureWindowUsesChronologicalFractionalSeconds(t *testing.T) {
	root, database := historyFixture(t)
	writer := NewWithOps(root, database)
	base := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	if err := writer.Append(Executions, ExecutionRecord{CallRecord: CallRecord{
		Timestamp: base.Add(500 * time.Millisecond), Source: "cli", Args: []string{},
		OK: false, Error: "fractional failure", ErrorType: "invalid_sql",
		CorrelationID: "qf_fractional_failure",
	}, Command: "query", ExitCode: 1}); err != nil {
		t.Fatal(err)
	}
	summary, err := writer.RecentQueryFailures(base.Add(time.Second), time.Second, 5)
	if err != nil || summary.Count != 1 || len(summary.Recent) != 1 ||
		summary.Recent[0].CorrelationID != "qf_fractional_failure" {
		t.Fatalf("fractional failure window = %+v, err %v", summary, err)
	}
}

func TestBackfillReadDoesNotCreateTheLogDirectory(t *testing.T) {
	root, database := historyFixture(t)
	writer := NewWithOps(root, database)
	if err := writer.Backfill(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, DirName)); !os.IsNotExist(err) {
		t.Fatalf("backfill read created logs: %v", err)
	}
}

func TestEitherCallSinkCanFailWithoutLosingTheOther(t *testing.T) {
	t.Run("jsonl failure leaves the durable call", func(t *testing.T) {
		root, database := historyFixture(t)
		if err := os.WriteFile(filepath.Join(root, DirName), []byte("not a directory"), 0o600); err != nil {
			t.Fatal(err)
		}
		writer := NewWithOps(root, database)
		err := writer.Append(Executions, ExecutionRecord{CallRecord: CallRecord{
			Timestamp: time.Now(), Source: "cli", Args: []string{}, OK: true,
		}, Command: "query"})
		if err == nil {
			t.Fatal("broken JSONL sink was not reported")
		}
		db := openHistoryDB(t, database)
		defer db.Close()
		assertHistoryCount(t, db, 1)
	})

	t.Run("ops failure leaves the JSONL call", func(t *testing.T) {
		root, database := historyFixture(t)
		dropHistoryTable(t, database)
		writer := NewWithOps(root, database)
		err := writer.Append(MCPAudit, MCPRecord{CallRecord: CallRecord{
			Timestamp: time.Now(), Source: "mcp", Args: map[string]any{}, OK: true,
		}, Tool: "roca_health"})
		if err == nil {
			t.Fatal("broken ops sink was not reported")
		}
		matches, globErr := filepath.Glob(filepath.Join(root, DirName, "mcp-audit-*.jsonl"))
		if globErr != nil || len(matches) != 1 {
			t.Fatalf("JSONL fallback = %v, err %v", matches, globErr)
		}
	})
}

func TestDoctorRollsBackToJSONLWhenTheVerifiedOpsReadBreaks(t *testing.T) {
	root, database := historyFixture(t)
	now := time.Now().UTC()
	writer := NewWithOps(root, database)
	if err := writer.Append(Executions, ExecutionRecord{CallRecord: CallRecord{
		Timestamp: now, Source: "cli", Args: []string{}, OK: false,
		Error: "synthetic rollback failure", ErrorType: "invalid_sql",
		CorrelationID: "qf_doctor_rollback",
	}, Command: "query", ExitCode: 1}); err != nil {
		t.Fatal(err)
	}
	if err := writer.Backfill(); err != nil {
		t.Fatal(err)
	}
	dropHistoryTable(t, database)
	summary, err := writer.RecentQueryFailures(now.Add(time.Minute), 24*time.Hour, 5)
	if err != nil || summary.Count != 1 || summary.Recent[0].CorrelationID != "qf_doctor_rollback" {
		t.Fatalf("JSONL rollback summary = %+v, err %v", summary, err)
	}
}

func dropHistoryTable(t *testing.T, database string) {
	t.Helper()
	db := openHistoryDB(t, database)
	if _, err := db.Exec(`DROP TABLE call_history`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

func historyFixture(t *testing.T) (string, string) {
	t.Helper()
	return historyFixtureAt(t, t.TempDir())
}

func historyFixtureAt(t *testing.T, root string) (string, string) {
	t.Helper()
	directory := filepath.Join(root, "plugins", rocaops.Name)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	database := filepath.Join(directory, rocaops.DatabaseFilename)
	if err := rocaops.ApplySchema(database); err != nil {
		t.Fatal(err)
	}
	return root, database
}

func openHistoryDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func assertHistoryCount(t *testing.T, db *sql.DB, want int) {
	t.Helper()
	var got int
	if err := db.QueryRow(`SELECT COUNT(*) FROM call_history`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("durable calls = %d, want %d", got, want)
	}
}
