package logfile

import (
	"testing"
	"time"
)

func TestRecentQueryFailuresReadsTheCommonContractAcrossSurfaces(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	writer := New(root)
	writer.now = func() time.Time { return now }
	records := []struct {
		stream string
		value  any
	}{
		{Executions, ExecutionRecord{CallRecord: CallRecord{
			Timestamp: now.Add(-2 * time.Hour), Source: "cli", OK: false,
			Error: "the generated SQL was rejected", ErrorType: "invalid_sql",
			CorrelationID: "qf_cli", Question: "find the synthetic lighthouse",
		}, Command: "query"}},
		{MCPAudit, MCPRecord{CallRecord: CallRecord{
			Timestamp: now.Add(-time.Hour), Source: "mcp", OK: false,
			Error: "the provider stopped", ErrorType: "model_error",
			CorrelationID: "qf_mcp", Question: "count synthetic memories",
		}, Tool: "roca_query"}},
		{Executions, ExecutionRecord{CallRecord: CallRecord{
			Timestamp: now.Add(-30 * time.Minute), Source: "cli", OK: true,
		}, Command: "query"}},
		{Executions, ExecutionRecord{CallRecord: CallRecord{
			Timestamp: now.Add(-48 * time.Hour), Source: "cli", OK: false,
			Error: "expired", ErrorType: "invalid_sql",
		}, Command: "query"}},
	}
	for _, record := range records {
		if err := writer.Append(record.stream, record.value); err != nil {
			t.Fatal(err)
		}
	}

	summary, err := writer.RecentQueryFailures(now, 24*time.Hour, 5)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Count != 2 || len(summary.Recent) != 2 {
		t.Fatalf("summary = %+v, want two recent failures", summary)
	}
	if summary.Recent[0].CorrelationID != "qf_mcp" || summary.Recent[1].CorrelationID != "qf_cli" {
		t.Fatalf("recent failures are not newest first: %+v", summary.Recent)
	}
}
