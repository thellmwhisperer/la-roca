package securefile

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestTryLockFailsWhileExclusiveLockIsHeld(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lease")
	if err := os.WriteFile(path, []byte("1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	release, err := Lock(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = release() })

	_, err = TryLock(path)
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("TryLock = %v, want ErrBusy", err)
	}
}

func TestTryLockMissingFile(t *testing.T) {
	_, err := TryLock(filepath.Join(t.TempDir(), "missing"))
	if !os.IsNotExist(err) {
		t.Fatalf("TryLock missing = %v, want not exist", err)
	}
}

func TestTryLockSucceedsOnUnlockedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lease")
	if err := os.WriteFile(path, []byte("1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	release, err := TryLock(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
}
