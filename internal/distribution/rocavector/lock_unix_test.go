//go:build !windows

package rocavector

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/securefile"
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

func TestExistingIndexLockIsNotReowned(t *testing.T) {
	state := t.TempDir()
	path := filepath.Join(state, "vector.db.index.lock")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	restoreIdentity := securefile.OverrideIdentityLookups(
		securefile.Identity{UID: 501, Name: "operator"},
		map[string]securefile.Identity{path: {UID: 0, Name: "root"}},
	)
	t.Cleanup(restoreIdentity)
	chownCalled := false
	restoreChown := securefile.OverrideChown(func(string, int, int) error {
		chownCalled = true
		return nil
	})
	t.Cleanup(restoreChown)

	release, busy, err := tryExclusiveFileLock(path, true)
	if err != nil || busy {
		t.Fatalf("lock: busy=%v err=%v", busy, err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	if chownCalled {
		t.Fatal("existing lock was reowned")
	}
}

func TestCreatedIndexLockIsRemovedWhenOwnershipAlignmentFails(t *testing.T) {
	state := t.TempDir()
	path := filepath.Join(state, "vector.db.index.lock")
	restoreIdentity := securefile.OverrideIdentityLookups(
		securefile.Identity{UID: 501, Name: "operator"},
		map[string]securefile.Identity{path: {UID: 0, Name: "root"}},
	)
	t.Cleanup(restoreIdentity)
	restoreChown := securefile.OverrideChown(func(string, int, int) error {
		return errors.New("chown failed")
	})
	t.Cleanup(restoreChown)

	if _, busy, err := tryExclusiveFileLock(path, true); err == nil || busy {
		t.Fatalf("lock: busy=%v err=%v, want alignment failure", busy, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("created lock stat error = %v, want removed lock", err)
	}
}
