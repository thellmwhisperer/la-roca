//go:build !windows

package vector

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIndexLockRejectsSymlinkWithoutTouchingTarget(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(directory, "target")
	path := filepath.Join(directory, "index.lock")
	if err := os.WriteFile(target, []byte("untouched"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}

	if _, _, err := tryLockIndex(path); err == nil {
		t.Fatal("symlink lock was accepted")
	}
	content, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "untouched" {
		t.Fatalf("symlink target changed to %q", content)
	}
}
