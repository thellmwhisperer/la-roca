package securefile

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Identity is a filesystem owner: numeric ids plus the login name when known.
type Identity struct {
	UID  uint32
	Name string
}

// Repair is the exact chown that returns a foreign-owned state path to the
// current user. Doctor prints Command as-is.
type Repair struct {
	Path    string
	Owner   string
	Command string
}

var (
	lookupOwner     = realStatIdentity
	currentIdentity = realCurrentIdentity
	currentEUID     = os.Geteuid
	chownPath       = os.Chown
)

// ScanForeignOwned walks root and reports every path whose owner is not the
// current user. It is read-only: it never chowns, creates, or deletes.
func ScanForeignOwned(root string) []Repair {
	return scanForeignOwned(root, currentIdentity(), lookupOwner)
}

func scanForeignOwned(root string, current Identity, lookup func(string) (Identity, error)) []Repair {
	if root == "" {
		return nil
	}
	resolved, err := resolveStateRoot(root)
	if err != nil || resolved == "" {
		return nil
	}
	if current.Name == "" {
		current.Name = fmt.Sprintf("%d", current.UID)
	}
	var found []Repair
	_ = filepath.WalkDir(resolved, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		owner, err := lookup(path)
		if err != nil || owner.UID == current.UID {
			return nil
		}
		name := owner.Name
		if name == "" {
			name = fmt.Sprintf("%d", owner.UID)
		}
		command := fmt.Sprintf("sudo chown %s %s", current.Name, shellQuote(path))
		if entry.Type()&os.ModeSymlink != 0 {
			command = fmt.Sprintf("sudo chown -h %s %s", current.Name, shellQuote(path))
		}
		found = append(found, Repair{
			Path:    path,
			Owner:   name,
			Command: command,
		})
		return nil
	})
	return found
}

// RefuseRootOverUserState errors when the process is root and root already
// belongs to a different user. Running as root there is what leaves root-owned
// locks and other state the operator can no longer open.
func RefuseRootOverUserState(root string) error {
	return refuseRootOverUserState(root, currentEUID(), lookupOwner)
}

func refuseRootOverUserState(root string, euid int, lookup func(string) (Identity, error)) error {
	if euid != 0 || root == "" {
		return nil
	}
	resolved, err := resolveStateRoot(root)
	if err != nil {
		return err
	}
	if resolved == "" {
		return nil
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return nil
	}
	owner, err := lookup(resolved)
	if err != nil {
		return err
	}
	if owner.UID == 0 {
		return nil
	}
	name := owner.Name
	if name == "" {
		name = fmt.Sprintf("%d", owner.UID)
	}
	return fmt.Errorf("running as root over %s owned by %s would leave root-owned state files; re-run as %s",
		root, name, name)
}

// AlignToParentOwner gives path the uid of its parent directory. A lock created
// while the process is root then stays the operator's, not root's.
func AlignToParentOwner(path string) error {
	return alignToParentOwner(path, lookupOwner, chownPath)
}

func alignToParentOwner(path string, lookup func(string) (Identity, error), chown func(string, int, int) error) error {
	if path == "" {
		return nil
	}
	parent, err := lookup(filepath.Dir(path))
	if err != nil {
		return err
	}
	current, err := lookup(path)
	if err != nil {
		return err
	}
	if parent.UID == current.UID {
		return nil
	}
	return chown(path, int(parent.UID), -1)
}

func resolveStateRoot(root string) (string, error) {
	info, err := os.Lstat(root)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return root, nil
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return resolved, nil
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

// OverrideIdentityLookups replaces owner resolution for tests. The returned
// function restores the previous lookups.
func OverrideIdentityLookups(current Identity, foreign map[string]Identity) func() {
	previousLookup, previousCurrent, previousEUID := lookupOwner, currentIdentity, currentEUID
	currentIdentity = func() Identity { return current }
	currentEUID = func() int { return int(current.UID) }
	lookupOwner = func(path string) (Identity, error) {
		if owner, ok := foreign[path]; ok {
			return owner, nil
		}
		return current, nil
	}
	return func() {
		lookupOwner, currentIdentity, currentEUID = previousLookup, previousCurrent, previousEUID
	}
}

// OverrideEffectiveUID replaces euid for tests. The returned function restores
// the previous function.
func OverrideEffectiveUID(euid int) func() {
	previous := currentEUID
	currentEUID = func() int { return euid }
	return func() { currentEUID = previous }
}

// OverrideChown replaces chown for tests. The returned function restores the
// previous function.
func OverrideChown(chown func(string, int, int) error) func() {
	previous := chownPath
	chownPath = chown
	return func() { chownPath = previous }
}
