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

func TestAppendExistingDoesNotRecreateRemovedDirectories(t *testing.T) {
	root := filepath.Join(t.TempDir(), "removed")
	writer := New(root)
	if err := writer.AppendExisting(Executions, ExecutionRecord{}); err == nil {
		t.Fatal("append unexpectedly created a missing log directory")
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("removed data directory was recreated: %v", err)
	}
}

func TestRedactCoversTheDocumentedCredentialShapes(t *testing.T) {
	secrets := []string{
		"github_pat_11AA22BB33CC44DD55EE66FF77GG88HH99II",
		"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.signature123",
		"AKIAIOSFODNN7EXAMPLE",
		"AIzaSyA12345678901234567890123456789012",
	}
	redacted := Redact(map[string]any{"question": strings.Join(secrets, " ")})
	raw, err := json.Marshal(redacted)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range secrets {
		if strings.Contains(string(raw), secret) {
			t.Errorf("credential shape survived redaction: %s", secret)
		}
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

// The redaction is what makes these traces safe to keep, so the names and the
// shapes it recognizes are pinned. Every pattern is BOUNDED: `keyword` contains
// "key" and is an ordinary field name, so it must survive.
func TestTheRedactionCoversBoundedCredentialNamesAndKeyShapes(t *testing.T) {
	for _, want := range []struct {
		name      string
		sensitive bool
	}{
		{name: "access_key", sensitive: true},
		{name: "private_key", sensitive: true},
		{name: "signing-key", sensitive: true},
		{name: "session_key", sensitive: true},
		{name: "api_key", sensitive: true},
		{name: "authorization", sensitive: true},
		{name: "keyword", sensitive: false},
		{name: "monkey", sensitive: false},
		{name: "database_path", sensitive: false},
	} {
		if got := SensitiveName(want.name); got != want.sensitive {
			t.Errorf("SensitiveName(%q) = %v, want %v", want.name, got, want.sensitive)
		}
	}
}

func TestTheRedactionRecognizesKeysByTheirShape(t *testing.T) {
	for _, secret := range []string{
		"AKIAIOSFODNN7EXAMPLE",
		"ASIAIOSFODNN7EXAMPLE",
		"AIzaSyA0abcdefghijklmnopqrstuvwxyz01234",
	} {
		redacted := Redact(map[string]any{"note": "it said " + secret + " out loud"})
		encoded, err := json.Marshal(redacted)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(encoded), secret) {
			t.Errorf("%s reached the log: %s", secret, encoded)
		}
	}
	// An ordinary sentence that merely starts like one of them is not a key.
	redacted := Redact(map[string]any{"note": "AKIA is a prefix"})
	encoded, _ := json.Marshal(redacted)
	if strings.Contains(string(encoded), "REDACTED") {
		t.Errorf("ordinary prose was redacted: %s", encoded)
	}
}
