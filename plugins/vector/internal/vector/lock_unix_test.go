//go:build !windows

package vector

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestCreatedIndexLockTakesStateDirectoryOwnerThroughDescriptor(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "index.lock")
	called := false
	previousChown := chownCreatedLock
	chownCreatedLock = func(file *os.File, uid, gid int) error {
		called = true
		return file.Chown(uid, gid)
	}
	t.Cleanup(func() { chownCreatedLock = previousChown })

	release, busy, err := tryLockIndex(path)
	if err != nil || busy {
		t.Fatalf("lock: busy=%v err=%v", busy, err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("new lock did not align ownership through its open descriptor")
	}
	directoryInfo, err := os.Stat(directory)
	if err != nil {
		t.Fatal(err)
	}
	lockInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	directoryStat := directoryInfo.Sys().(*syscall.Stat_t)
	lockStat := lockInfo.Sys().(*syscall.Stat_t)
	if lockStat.Uid != directoryStat.Uid {
		t.Fatalf("lock owner %d, want directory owner %d", lockStat.Uid, directoryStat.Uid)
	}
	t.Logf("state directory owner: %d; created lock owner: %d; descriptor chown: called",
		directoryStat.Uid, lockStat.Uid)
}

func TestPreExistingIndexLockIsNotReowned(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "index.lock")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	previousChown := chownCreatedLock
	chownCreatedLock = func(*os.File, int, int) error {
		t.Fatal("pre-existing lock was reowned")
		return nil
	}
	t.Cleanup(func() { chownCreatedLock = previousChown })

	release, busy, err := tryLockIndex(path)
	if err != nil || busy {
		t.Fatalf("lock: busy=%v err=%v", busy, err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
}

func TestCreatedIndexLockIsRemovedAfterDescriptorChownFailure(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "index.lock")
	previousChown := chownCreatedLock
	chownCreatedLock = func(*os.File, int, int) error { return errors.New("chown failed") }
	t.Cleanup(func() { chownCreatedLock = previousChown })

	if _, _, err := tryLockIndex(path); err == nil {
		t.Fatal("descriptor chown failure was accepted")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("created lock stat error = %v, want removed lock", err)
	}
}

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
