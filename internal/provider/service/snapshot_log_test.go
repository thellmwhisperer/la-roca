package service

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thellmwhisperer/la-roca/internal/store"
)

func TestReadOnlyOpenWritesSnapshotTelemetry(t *testing.T) {
	dataDir := t.TempDir()
	tempRoot := t.TempDir()
	t.Setenv("TMPDIR", tempRoot)
	t.Setenv("TMP", tempRoot)
	t.Setenv("TEMP", tempRoot)
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

func TestReadOnlyServicesKeepSnapshotTelemetrySeparate(t *testing.T) {
	tempRoot := t.TempDir()
	t.Setenv("TMPDIR", tempRoot)
	t.Setenv("TMP", tempRoot)
	t.Setenv("TEMP", tempRoot)
	dataA := t.TempDir()
	dataB := t.TempDir()
	pathA := filepath.Join(dataA, "source-a.db")
	pathB := filepath.Join(dataB, "source-b.db")
	for _, path := range []string{pathA, pathB} {
		seed, err := store.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := seed.Close(); err != nil {
			t.Fatal(err)
		}
	}
	svcA, err := openWithContext(t.Context(), Options{DBPath: pathA, DataDir: dataA, ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svcA.Close() })
	svcB, err := openWithContext(t.Context(), Options{DBPath: pathB, DataDir: dataB, ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svcB.Close() })

	extra := filepath.Join(dataA, "extra-a.db")
	seed, err := store.Open(extra)
	if err != nil {
		t.Fatal(err)
	}
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.OpenReadOnlySnapshot(svcA.snapshotContext(t.Context()), extra)
	if err != nil {
		t.Fatal(err)
	}
	if err := snapshot.Close(); err != nil {
		t.Fatal(err)
	}

	countA := snapshotLogLineCount(t, dataA)
	countB := snapshotLogLineCount(t, dataB)
	if countA != 2 || countB != 1 {
		t.Fatalf("snapshot telemetry lines = (%d, %d), want (2, 1)", countA, countB)
	}
}

func snapshotLogLineCount(t *testing.T, dataDir string) int {
	t.Helper()
	path := filepath.Join(dataDir, "logs", "snapshots-"+time.Now().UTC().Format(time.DateOnly)+".jsonl")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return bytes.Count(raw, []byte("\n"))
}
