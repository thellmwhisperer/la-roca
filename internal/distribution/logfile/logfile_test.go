package logfile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAppendWritesOneCredentialFreeDatedLine(t *testing.T) {
	root := t.TempDir()
	writer := New(root)
	writer.now = func() time.Time { return time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC) }
	record := MCPRecord{
		Timestamp: writer.now(), Tool: "roca_store", OK: true,
		Args: map[string]any{"content": "token=top-secret", "api_key": "sk-private123"},
	}
	if err := writer.Append(MCPAudit, record); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, DirName, "mcp-audit-2026-08-10.jsonl")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "top-secret") || strings.Contains(string(raw), "sk-private123") {
		t.Fatalf("credential leaked into log: %s", raw)
	}
	if strings.Count(string(raw), "\n") != 1 {
		t.Fatalf("log is not one JSONL record: %q", raw)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("invalid JSONL record: %v", err)
	}
}

func TestAppendPrunesFilesOutsideTheRetentionWindow(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, DirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	expired := filepath.Join(dir, "executions-2026-07-11.jsonl")
	kept := filepath.Join(dir, "executions-2026-07-12.jsonl")
	for _, path := range []string{expired, kept} {
		if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writer := New(root)
	writer.now = func() time.Time { return time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC) }
	if err := writer.Append(Executions, ExecutionRecord{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(expired); !os.IsNotExist(err) {
		t.Fatalf("expired file still exists: %v", err)
	}
	if _, err := os.Stat(kept); err != nil {
		t.Fatalf("retained file is missing: %v", err)
	}
}
