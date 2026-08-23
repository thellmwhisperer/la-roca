package service

import (
	"path/filepath"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/store"
)

func TestReadOnlyOpenWritesSnapshotTelemetry(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("TMPDIR", t.TempDir())
	path := filepath.Join(dataDir, "roca.db")
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
	if err := svc.Close(); err != nil {
		t.Fatal(err)
	}
}
