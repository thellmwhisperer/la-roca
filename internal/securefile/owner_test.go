package securefile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStateOwnershipScenarios(t *testing.T) {
	root := t.TempDir()
	lock := filepath.Join(root, "vector.db.index.lock")
	if err := os.WriteFile(lock, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	operator := Identity{UID: 501, GID: 20, Name: "operator"}
	rootOwner := Identity{UID: 0, GID: 0, Name: "root"}
	sameLookup := func(string) (Identity, error) { return operator, nil }

	t.Run("scan reports exact chown for a foreign owner", func(t *testing.T) {
		got := scanForeignOwned(root, operator, func(path string) (Identity, error) {
			if path == lock {
				return rootOwner, nil
			}
			return operator, nil
		})
		want := "chown operator " + lock
		if len(got) != 1 || got[0].Command != want || got[0].Owner != "root" || got[0].Path != lock {
			t.Fatalf("repair = %#v, want command %q", got, want)
		}
	})
	t.Run("scan ignores the current owner", func(t *testing.T) {
		if got := scanForeignOwned(root, operator, sameLookup); len(got) != 0 {
			t.Fatalf("same-owner scan = %#v, want none", got)
		}
	})
	t.Run("root over user state is refused", func(t *testing.T) {
		err := refuseRootOverUserState(root, 0, sameLookup)
		if err == nil {
			t.Fatal("root over user state was accepted")
		}
		for _, want := range []string{"running as root", root, "operator", "re-run as operator"} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("refuse error %q does not carry %q", err, want)
			}
		}
	})
	t.Run("operator or root-owned state is accepted", func(t *testing.T) {
		if err := refuseRootOverUserState(root, 501, sameLookup); err != nil {
			t.Fatalf("operator over own state: %v", err)
		}
		if err := refuseRootOverUserState(root, 0, func(string) (Identity, error) {
			return rootOwner, nil
		}); err != nil {
			t.Fatalf("root over root state: %v", err)
		}
		if err := refuseRootOverUserState(filepath.Join(root, "missing"), 0, sameLookup); err != nil {
			t.Fatalf("root over missing state: %v", err)
		}
	})
	t.Run("lock chowns to the directory owner", func(t *testing.T) {
		var gotPath string
		var gotUID, gotGID int
		err := alignToParentOwner(lock, func(name string) (Identity, error) {
			if name == root {
				return operator, nil
			}
			return rootOwner, nil
		}, func(name string, uid, gid int) error {
			gotPath, gotUID, gotGID = name, uid, gid
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if gotPath != lock || gotUID != 501 || gotGID != 20 {
			t.Fatalf("chown(%q, %d, %d), want (%q, 501, 20)", gotPath, gotUID, gotGID, lock)
		}
	})
	t.Run("lock skips chown when owners match", func(t *testing.T) {
		err := alignToParentOwner(lock, sameLookup, func(string, int, int) error {
			t.Fatal("chown called for matching owner")
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	})
}
