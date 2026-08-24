package store

import (
	"context"
	"errors"
	"io/fs"
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

type dirEntryStub struct {
	name string
	err  error
}

func (e dirEntryStub) Name() string               { return e.name }
func (e dirEntryStub) IsDir() bool                { return true }
func (e dirEntryStub) Type() fs.FileMode          { return fs.ModeDir }
func (e dirEntryStub) Info() (fs.FileInfo, error) { return nil, e.err }

func TestScavengeToleratesConcurrentlyRemovedSnapshot(t *testing.T) {
	// A snapshot directory can be removed by its owner between the directory
	// listing and the per-entry stat; scavenging must skip it, not fail.
	err := scavengeSnapshotEntries(context.Background(), t.TempDir(), []fs.DirEntry{
		dirEntryStub{name: snapshotDirectoryPrefix + "vanished", err: fs.ErrNotExist},
	}, time.Now())
	if err != nil {
		t.Fatalf("scavenge failed on a concurrently removed snapshot: %v", err)
	}
}

func TestScavengeStillSurfacesRealSnapshotErrors(t *testing.T) {
	boom := errors.New("boom")
	err := scavengeSnapshotEntries(context.Background(), t.TempDir(), []fs.DirEntry{
		dirEntryStub{name: snapshotDirectoryPrefix + "stale", err: boom},
	}, time.Now())
	if !errors.Is(err, boom) {
		t.Fatalf("scavenge error = %v, want it to wrap %v", err, boom)
	}
}
