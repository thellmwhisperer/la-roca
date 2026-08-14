package vector

import (
	"context"
	"errors"
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
