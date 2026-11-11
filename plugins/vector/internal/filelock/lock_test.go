package filelock

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestLockCreatesPrivateFileAndExcludesSecondHolder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.lock")
	release, err := Lock(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if mode := info.Mode().Perm(); mode != 0o600 {
			t.Fatalf("lock file mode = %o, want 600", mode)
		}
	}

	acquired := make(chan func() error)
	go func() {
		second, err := Lock(path)
		if err != nil {
			t.Error(err)
			second = func() error { return nil }
		}
		acquired <- second
	}()
	select {
	case second := <-acquired:
		_ = second()
		t.Fatal("second Lock returned while the first still held the lock")
	default:
	}

	if err := release(); err != nil {
		t.Fatal(err)
	}
	if err := (<-acquired)(); err != nil {
		t.Fatal(err)
	}
}
