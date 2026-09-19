package vector

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestIndexLockNamesItsHolderAndHonoursCancellation(t *testing.T) {
	directory := t.TempDir()
	contested := filepath.Join(directory, "contested.index.lock")
	release, err := lockFile(contested)
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	held := ""
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := lockIndex(ctx, contested, func(holder string) { held = holder }); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want a deadline instead of an unbounded wait", err)
	}
	if held != contested {
		t.Fatalf("contention reported %q, want %q", held, contested)
	}

	free := ""
	acquired, err := lockIndex(context.Background(), filepath.Join(directory, "free.index.lock"),
		func(holder string) { free = holder })
	if err != nil {
		t.Fatal(err)
	}
	if free != "" {
		t.Fatalf("an uncontended lock reported %q", free)
	}
	if err := acquired(); err != nil {
		t.Fatal(err)
	}
}

func TestClearUnheldIndexLocksRemovesAStaleFileAndLeavesAHeldOne(t *testing.T) {
	directory := t.TempDir()
	stale := filepath.Join(directory, "stale.vector.db")
	live := filepath.Join(directory, "live.vector.db")
	if err := os.WriteFile(stale+".index.lock", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	release, err := lockFile(live + ".index.lock")
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	ClearUnheldIndexLocks([]string{stale, live})
	if _, err := os.Stat(stale + ".index.lock"); !os.IsNotExist(err) {
		t.Fatalf("stale lock still present: %v", err)
	}
	if _, err := os.Stat(live + ".index.lock"); err != nil {
		t.Fatalf("held lock was removed: %v", err)
	}
}
