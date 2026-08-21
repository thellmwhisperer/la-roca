package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestReadOnlySnapshotLifecycle(t *testing.T) {
	t.Run("canceled before copy", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		snapshot, err := OpenReadOnlySnapshot(ctx, filepath.Join(t.TempDir(), "missing.db"))
		if snapshot != nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("snapshot = %v, error = %v", snapshot, err)
		}
	})

	t.Run("stale directories are scavenged", func(t *testing.T) {
		root := t.TempDir()
		stale, err := os.MkdirTemp(root, snapshotDirectoryPrefix)
		if err != nil {
			t.Fatal(err)
		}
		fresh, err := os.MkdirTemp(root, snapshotDirectoryPrefix)
		if err != nil {
			t.Fatal(err)
		}
		unrelated := filepath.Join(root, "unrelated")
		if err := os.Mkdir(unrelated, 0o700); err != nil {
			t.Fatal(err)
		}
		now := time.Now()
		old := now.Add(-snapshotStaleAfter - time.Hour)
		if err := os.Chtimes(stale, old, old); err != nil {
			t.Fatal(err)
		}
		if err := scavengeReadOnlySnapshots(context.Background(), root, now); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(stale); !os.IsNotExist(err) {
			t.Fatalf("stale snapshot still exists: %v", err)
		}
		for _, path := range []string{fresh, unrelated} {
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("retained directory %q: %v", path, err)
			}
		}
	})
}
