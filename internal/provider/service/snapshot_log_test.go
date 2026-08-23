package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thellmwhisperer/la-roca/internal/store"
)

func TestReadOnlyOpenWritesSnapshotTelemetry(t *testing.T) {
	dataDir := t.TempDir()
	t.Cleanup(func() { EnableSnapshotLogs("") })
	t.Setenv("TMPDIR", t.TempDir())
	logDir := filepath.Join(dataDir, "logs")
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(logDir, "snapshots-2000-01-01.jsonl")
	if err := os.WriteFile(old, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dataDir, "token=synthetic-secret-value.db")
	seed, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}

	svc, err := openWithContext(t.Context(), Options{DBPath: path, DataDir: dataDir, ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	matches, err := filepath.Glob(filepath.Join(dataDir, "logs", "snapshots-*.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) == 0 {
		t.Fatal("read-only open wrote no snapshot telemetry")
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("snapshot telemetry retained expired segment: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(logDir,
		"snapshots-"+time.Now().UTC().Format(time.DateOnly)+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "synthetic-secret-value") ||
		!strings.Contains(string(raw), "[REDACTED]") {
		t.Fatalf("snapshot telemetry was not redacted: %s", raw)
	}
	if err := svc.Close(); err != nil {
		t.Fatal(err)
	}
}
