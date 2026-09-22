// Package securefile owns the small file primitives used for operator state.
package securefile

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

var (
	errAtomicNoReplaceUnsupported = errors.New("atomic no-replace publication is unsupported")
	errAtomicReplaceUnsupported   = errors.New("atomic replacement publication is unsupported")
	renameNoReplaceFile           = renameNoReplace
	renameReplaceFile             = renameReplace
	beforePublication             = func(string) {}
)

// Write replaces path atomically after the new bytes and permissions are durable.
// It serializes with the conditional publication boundary for the same path.
func Write(path string, data []byte, mode, dirMode os.FileMode) error {
	_, err := publish(path, data, nil, mode, dirMode, true, false)
	return err
}

// CreatePreservingParentMode atomically creates a file without replacing a
// path that already exists or changing an existing parent directory's mode.
func CreatePreservingParentMode(path string, data []byte, mode, dirMode os.FileMode) error {
	_, err := CreatePreservingParentModeWithResult(path, data, mode, dirMode)
	return err
}

// CreatePreservingParentModeWithResult is CreatePreservingParentMode plus the
// identity of the inode that was published.
func CreatePreservingParentModeWithResult(path string, data []byte, mode, dirMode os.FileMode) (Publication, error) {
	return publish(path, data, nil, mode, dirMode, false, true)
}

// Replace atomically replaces an operator-owned file while preserving its mode.
// previous is the exact preimage. A nil previous means that the caller expects
// the path not to exist; it is never an instruction to overwrite an unknown
// file. Use Write when unconditional replacement is intentional.
func Replace(path string, data, previous []byte) error {
	_, err := ReplaceWithResult(path, data, previous)
	return err
}

// ReplaceWithResult is Replace plus the identity of the inode that was
// published. Callers that later remove or roll back the file must compare this
// identity rather than taking a new path sample after publication.
func ReplaceWithResult(path string, data, previous []byte) (Publication, error) {
	original, err := expectedOriginal(path, previous)
	if err != nil {
		return Publication{}, err
	}
	expected := &conditionalFile{original: original, previous: previous, checkMode: original != nil}
	return publish(path, data, expected, 0, 0o700, false, original == nil)
}

// ReplaceWithIdentity conditionally publishes against the exact inode returned
// by an earlier publication. It closes the cleanup/rollback race in which a
// concurrent writer installs byte-identical content between an identity check
// and the next conditional write.
func ReplaceWithIdentity(path string, data, previous []byte,
	identity FileIdentity) (Publication, error) {
	if !identity.Valid() {
		return Publication{}, fmt.Errorf("refuse to replace %s without a published identity", path)
	}
	expected := &conditionalFile{original: identity.info, previous: previous}
	return publish(path, data, expected, identity.info.Mode().Perm(), 0o700, false, false)
}

// ReplaceRegular stages a replacement for a previously inspected regular file
// while preserving its mode. Before publication, it refuses if the path no
// longer names that file or its expected bytes changed.
func ReplaceRegular(path string, data, previous []byte, original os.FileInfo) error {
	_, err := ReplaceRegularWithResult(path, data, previous, original)
	return err
}

// ReplaceRegularWithResult is ReplaceRegular plus the identity of the inode
// that was published.
func ReplaceRegularWithResult(path string, data, previous []byte,
	original os.FileInfo) (Publication, error) {
	if original == nil || !original.Mode().IsRegular() {
		return Publication{}, fmt.Errorf("refuse to replace non-regular file %s", path)
	}
	expected := &conditionalFile{original: original, previous: previous, checkMode: true}
	return publish(path, data, expected, original.Mode().Perm(), 0o700, false, false)
}

// BackUp preserves previous bytes beside path without overwriting older copies.
func BackUp(path string, previous []byte) (string, error) {
	for index := 0; ; index++ {
		backup := path + ".roca.bak"
		if index > 0 {
			backup = fmt.Sprintf("%s.roca.bak.%d", path, index)
		}
		file, err := os.OpenFile(backup, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if os.IsExist(err) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("inspect backup %s: %w", backup, err)
		}
		if _, err := file.Write(previous); err != nil {
			file.Close()
			os.Remove(backup)
			return "", fmt.Errorf("back up %s: %w", path, err)
		}
		if err := file.Close(); err != nil {
			os.Remove(backup)
			return "", fmt.Errorf("close backup %s: %w", backup, err)
		}
		return backup, nil
	}
}

type conditionalFile struct {
	original  os.FileInfo
	previous  []byte
	checkMode bool
}

func expectedOriginal(path string, previous []byte) (os.FileInfo, error) {
	original, err := os.Lstat(path)
	switch {
	case os.IsNotExist(err):
		if previous != nil {
			return nil, changedFileError(path)
		}
		return nil, nil
	case err != nil:
		return nil, fmt.Errorf("inspect %s: %w", path, err)
	case !original.Mode().IsRegular():
		return nil, fmt.Errorf("refuse to replace non-regular file %s", path)
	case previous == nil:
		return nil, changedFileError(path)
	default:
		return original, nil
	}
}

func publish(path string, data []byte, expected *conditionalFile, mode, dirMode os.FileMode,
	restrictDir, createOnly bool) (result Publication, err error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return result, err
	}
	if restrictDir {
		if err := os.Chmod(dir, dirMode); err != nil {
			return result, fmt.Errorf("restrict directory permissions: %w", err)
		}
	}

	// The directory lock is the shared publication boundary. All La Roca
	// writers use the same lock, so a second writer cannot land between the
	// preimage check and the atomic rename. The public path is never moved
	// aside or left absent, and no lock artifact is left beside it.
	release, err := lockPublication(dir)
	if err != nil {
		return result, fmt.Errorf("lock %s for publication: %w", path, err)
	}
	defer func() {
		if releaseErr := release(); err == nil && releaseErr != nil {
			err = fmt.Errorf("release publication lock for %s: %w", path, releaseErr)
		}
	}()

	if expected != nil {
		if err = verifyExpected(path, expected); err != nil {
			return result, err
		}
		if mode == 0 && expected.original != nil {
			mode = expected.original.Mode().Perm()
		}
	}
	if mode == 0 {
		mode = 0o600
	}

	temporary, err := os.CreateTemp(dir, "."+filepath.Base(path)+"-*")
	if err != nil {
		return result, err
	}
	staged := temporary.Name()
	defer func() {
		temporary.Close()
		if err != nil {
			_ = os.Remove(staged)
		}
	}()
	if err = temporary.Chmod(mode); err != nil {
		return result, err
	}
	if _, err = temporary.Write(data); err != nil {
		return result, err
	}
	if err = temporary.Sync(); err != nil {
		return result, err
	}
	stagedInfo, err := temporary.Stat()
	if err != nil {
		return result, fmt.Errorf("inspect staged %s: %w", path, err)
	}
	if err = temporary.Close(); err != nil {
		return result, err
	}

	// Recheck after staging: staging can be slow, and a cooperative writer may
	// have been waiting for the boundary when the caller first inspected path.
	if expected != nil {
		if err = verifyExpected(path, expected); err != nil {
			return result, err
		}
	}
	beforePublication(path)
	if expected != nil {
		if err = verifyExpected(path, expected); err != nil {
			return result, err
		}
	}

	if createOnly {
		if err = renameNoReplaceFile(staged, path); err != nil {
			if os.IsExist(err) {
				return result, createCollisionError(path)
			}
			if errors.Is(err, errAtomicNoReplaceUnsupported) {
				return result, fmt.Errorf("cannot safely create %s: %w", path, err)
			}
			return result, fmt.Errorf("atomically create %s: %w", path, err)
		}
	} else if err = renameReplaceFile(staged, path); err != nil {
		if errors.Is(err, errAtomicReplaceUnsupported) {
			return result, fmt.Errorf("cannot safely replace %s: %w", path, err)
		}
		return result, err
	}
	result.Identity = identityFromInfo(stagedInfo)

	if runtime.GOOS == "windows" {
		return result, nil
	}
	directory, err := os.Open(dir)
	if err != nil {
		return result, err
	}
	defer directory.Close()
	return result, directory.Sync()
}

func verifyExpected(path string, expected *conditionalFile) error {
	if expected.original == nil {
		if _, err := os.Lstat(path); err == nil {
			return createCollisionError(path)
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("inspect %s: %w", path, err)
		}
		return nil
	}
	if err := requireSameRegularFile(path, expected.original, expected.checkMode); err != nil {
		return err
	}
	current, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("re-read %s: %w", path, err)
	}
	if !bytes.Equal(current, expected.previous) {
		return changedFileError(path)
	}
	return nil
}

func createCollisionError(path string) error {
	return fmt.Errorf("%s appeared before it could be created; existing file was preserved", path)
}

func requireSameRegularFile(path string, original os.FileInfo, checkMode bool) error {
	current, err := os.Lstat(path)
	if err != nil || !current.Mode().IsRegular() || !os.SameFile(original, current) ||
		(checkMode && current.Mode().Perm() != original.Mode().Perm()) {
		return changedFileError(path)
	}
	return nil
}

func changedFileError(path string) error {
	return fmt.Errorf(
		"%s changed while it was being edited: close the runtime that owns it and try again",
		path)
}
