package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
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
}
