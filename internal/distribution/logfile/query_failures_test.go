package logfile

import (
	"os"
	"path/filepath"
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
			Timestamp: now.Add(-45 * time.Minute), Source: "cli", OK: false,
			Error: "deep interpretation stopped", ErrorType: "model_error",
			CorrelationID: "qf_explore_cli", Question: "synthetic",
		}, Command: "explore"}},
		{MCPAudit, MCPRecord{CallRecord: CallRecord{
			Timestamp: now.Add(-40 * time.Minute), Source: "mcp", OK: false,
			Error: "deep interpretation stopped", ErrorType: "model_error",
			CorrelationID: "qf_explore_mcp", Question: "synthetic",
		}, Tool: "roca_explore"}},
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
	if summary.Count != 4 || len(summary.Recent) != 4 {
		t.Fatalf("summary = %+v, want four recent failures", summary)
	}
	if summary.Recent[0].CorrelationID != "qf_explore_mcp" ||
		summary.Recent[1].CorrelationID != "qf_explore_cli" ||
		summary.Recent[2].CorrelationID != "qf_mcp" || summary.Recent[3].CorrelationID != "qf_cli" {
		t.Fatalf("recent failures are not newest first: %+v", summary.Recent)
	}

	stale := filepath.Join(root, DirName, Executions+"-2026-07-01.jsonl")
	if err := os.WriteFile(stale, []byte("{}\n"), 0o000); err != nil {
		t.Fatal(err)
	}
	bounded, err := writer.RecentQueryFailures(now, 24*time.Hour, 5)
	if err != nil {
		t.Fatalf("a segment dated before the window was opened: %v", err)
	}
	if bounded.Count != 4 || bounded.Unreadable != 0 {
		t.Fatalf("summary = %+v, want the window unchanged", bounded)
	}

	// The permission bits are the only way to make this read fail while the file
	// stays a segment the reader still pairs, and they do not bind a privileged
	// process: as root the read succeeds and this would fail for a reason that is
	// not the product's.
	t.Run("an unreadable segment is a gap, not the verdict", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("running as root: chmod cannot make a file unreadable")
		}
		unreadable := filepath.Join(root, DirName, Executions+"-"+
			now.Format(time.DateOnly)+"-1.jsonl")
		if err := os.WriteFile(unreadable, []byte("{}\n"), 0o000); err != nil {
			t.Fatal(err)
		}
		partial, err := writer.RecentQueryFailures(now, 24*time.Hour, 1)
		if err == nil {
			t.Fatal("an unreadable segment was not reported as a warning")
		}
		if partial.Count != 4 || partial.Unreadable != 1 {
			t.Fatalf("summary = %+v, want two failures and one unreadable segment", partial)
		}
		if len(partial.Recent) != 1 || partial.Recent[0].CorrelationID != "qf_explore_mcp" {
			t.Fatalf("a partial reading was neither sorted nor cut: %+v", partial.Recent)
		}
	})
}
