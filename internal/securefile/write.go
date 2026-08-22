// Package securefile owns the small file primitives used for operator state.
package securefile

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

var linkFile = os.Link

// Write replaces path atomically after the new bytes and permissions are durable.
func Write(path string, data []byte, mode, dirMode os.FileMode) (err error) {
	return publish(path, data, nil, mode, dirMode, true, false)
}

// CreatePreservingParentMode atomically creates a file without replacing a
// path that already exists or changing an existing parent directory's mode.
func CreatePreservingParentMode(path string, data []byte, mode, dirMode os.FileMode) error {
	return publish(path, data, nil, mode, dirMode, false, true)
}

// Replace atomically replaces an operator-owned file while preserving its mode.
// When previous is non-nil, a concurrent change makes the replacement fail.
func Replace(path string, data, previous []byte) error {
	mode := os.FileMode(0o600)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	return publish(path, data, previous, mode, 0o700, false, false)
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

func publish(path string, data, previous []byte, mode, dirMode os.FileMode,
	restrictDir, createOnly bool) (err error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return err
	}
	if restrictDir {
		if err := os.Chmod(dir, dirMode); err != nil {
			return fmt.Errorf("restrict directory permissions: %w", err)
		}
	}
	temporary, err := os.CreateTemp(dir, "."+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	staged := temporary.Name()
	defer func() {
		temporary.Close()
		if err != nil {
			os.Remove(staged)
		}
	}()
	if err = temporary.Chmod(mode); err != nil {
		return err
	}
	if _, err = temporary.Write(data); err != nil {
		return err
	}
	if err = temporary.Sync(); err != nil {
		return err
	}
	if err = temporary.Close(); err != nil {
		return err
	}
	if previous != nil {
		current, readErr := os.ReadFile(path)
		if readErr != nil {
			return fmt.Errorf("re-read %s: %w", path, readErr)
		}
		if string(current) != string(previous) {
			return fmt.Errorf(
				"%s changed while it was being edited: close the runtime that owns it and try again",
				path)
		}
	}
	if createOnly {
		if linkErr := linkFile(staged, path); linkErr != nil {
			if os.IsExist(linkErr) {
				return createCollisionError(path)
			}
			if err = createExclusive(path, data, mode); err != nil {
				return err
			}
		}
		if err = os.Remove(staged); err != nil {
			return err
		}
	} else if err = os.Rename(staged, path); err != nil {
		return err
	}
	if !createOnly {
		if err = os.Chmod(path, mode); err != nil {
			return err
		}
	}
	if runtime.GOOS == "windows" {
		return nil
	}
	directory, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func createExclusive(path string, data []byte, mode os.FileMode) (err error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		if os.IsExist(err) {
			return createCollisionError(path)
		}
		return err
	}
	created, err := file.Stat()
	if err != nil {
		file.Close()
		return err
	}
	defer func() {
		if closeErr := file.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
		if err != nil {
			current, statErr := os.Stat(path)
			if statErr == nil && os.SameFile(created, current) {
				_ = os.Remove(path)
			}
		}
	}()

	if err = file.Chmod(mode); err != nil {
		return err
	}
	if _, err = file.Write(data); err != nil {
		return err
	}
	return file.Sync()
}

func createCollisionError(path string) error {
	return fmt.Errorf("%s appeared before it could be created; existing file was preserved", path)
}
