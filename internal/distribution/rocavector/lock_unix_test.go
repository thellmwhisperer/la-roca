//go:build !windows

package rocavector

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestIndexLockTakesTheStateDirectoryOwner(t *testing.T) {
	state := t.TempDir()
	path := filepath.Join(state, "vector.db.index.lock")
	release, busy, err := tryExclusiveFileLock(path, true)
	if err != nil || busy {
		t.Fatalf("lock: busy=%v err=%v", busy, err)
	}
	t.Cleanup(func() { _ = release() })

	dirInfo, err := os.Stat(state)
	if err != nil {
		t.Fatal(err)
	}
	lockInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	dirStat, ok := dirInfo.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatal("directory stat has no unix owner")
	}
	lockStat, ok := lockInfo.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatal("lock stat has no unix owner")
	}
	if lockStat.Uid != dirStat.Uid || lockStat.Gid != dirStat.Gid {
		t.Fatalf("lock owner %d:%d, want directory owner %d:%d",
			lockStat.Uid, lockStat.Gid, dirStat.Uid, dirStat.Gid)
	}
}
