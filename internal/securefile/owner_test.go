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
	operator := Identity{UID: 501, Name: "operator"}
	rootOwner := Identity{UID: 0, Name: "root"}
	sameLookup := func(string) (Identity, error) { return operator, nil }

	t.Run("scan reports exact chown for a foreign owner", func(t *testing.T) {
		got := scanForeignOwned(root, operator, func(path string) (Identity, error) {
			if path == lock {
				return rootOwner, nil
			}
			return operator, nil
		})
		want := "sudo chown -h operator '" + lock + "'"
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
	t.Run("scan follows a symlinked state root", func(t *testing.T) {
		target := t.TempDir()
		targetLock := filepath.Join(target, "vector.db.index.lock")
		if err := os.WriteFile(targetLock, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(t.TempDir(), "state")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		resolvedTarget, err := filepath.EvalSymlinks(target)
		if err != nil {
			t.Fatal(err)
		}
		resolvedTargetLock := filepath.Join(resolvedTarget, "vector.db.index.lock")
		got := scanForeignOwned(link, operator, func(path string) (Identity, error) {
			if path == resolvedTargetLock {
				return rootOwner, nil
			}
			return operator, nil
		})
		if len(got) != 1 || got[0].Path != resolvedTargetLock {
			t.Fatalf("symlink scan = %#v, want %q", got, resolvedTargetLock)
		}
		if err := refuseRootOverUserState(link, 0, func(path string) (Identity, error) {
			if path == resolvedTarget {
				return operator, nil
			}
			return rootOwner, nil
		}); err == nil {
			t.Fatal("root over symlinked user state was accepted")
		}
	})
	t.Run("scan emits no-follow chown for a symlink", func(t *testing.T) {
		target := filepath.Join(root, "real-lock")
		link := filepath.Join(root, "link-lock")
		if err := os.WriteFile(target, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		got := scanForeignOwned(root, operator, func(path string) (Identity, error) {
			if path == link {
				return rootOwner, nil
			}
			return operator, nil
		})
		if len(got) != 1 || got[0].Command != "sudo chown -h operator '"+link+"'" {
			t.Fatalf("symlink repair = %#v", got)
		}
	})
	t.Run("scan emits recursive chown for a foreign directory", func(t *testing.T) {
		foreignDir := filepath.Join(root, "foreign")
		foreignLock := filepath.Join(foreignDir, "vector.db.index.lock")
		if err := os.MkdirAll(foreignDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(foreignLock, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		got := scanForeignOwned(root, operator, func(path string) (Identity, error) {
			if path == foreignDir || path == foreignLock {
				return rootOwner, nil
			}
			return operator, nil
		})
		want := "sudo chown -R operator '" + foreignDir + "'"
		if len(got) != 1 || got[0].Path != foreignDir || got[0].Command != want {
			t.Fatalf("recursive repair = %#v, want command %q", got, want)
		}
	})
}
