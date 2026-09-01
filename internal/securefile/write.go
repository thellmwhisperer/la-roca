// Package securefile owns the small file primitives used for operator state.
package securefile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

var (
	errAtomicNoReplaceUnsupported = errors.New("atomic no-replace publication is unsupported")
	renameNoReplaceFile           = renameNoReplace
	isolateFile                   = os.Rename
)

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

func ReplaceExact(path string, data, expected []byte, mode os.FileMode) error {
	return exchangeExact(path, data, expected, mode, false)
}

func Remove(path string, expected []byte) error {
	return exchangeExact(path, nil, expected, 0, true)
}

func exchangeExact(path string, data, expected []byte, mode os.FileMode, remove bool) error {
	dir := filepath.Dir(path)
	transaction, err := os.MkdirTemp(dir, "."+filepath.Base(path)+"-exchange-")
	if err != nil {
		return err
	}
	cleanup := true
	defer func() {
		if cleanup {
			os.RemoveAll(transaction)
		}
	}()

	staged := filepath.Join(transaction, "replacement")
	if !remove {
		file, err := os.OpenFile(staged, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
		if err != nil {
			return err
		}
		if _, err := file.Write(data); err != nil {
			file.Close()
			return err
		}
		if err := file.Sync(); err != nil {
			file.Close()
			return err
		}
		if err := file.Close(); err != nil {
			return err
		}
	}

	held := filepath.Join(transaction, "current")
	if err := isolateFile(path, held); err != nil {
		if remove && os.IsNotExist(err) {
			return nil
		}
		return err
	}
	current, err := os.ReadFile(held)
	if err != nil {
		return preserveExchangedFile(path, held, err, &cleanup)
	}
	if string(current) != string(expected) {
		return preserveExchangedFile(path, held, fmt.Errorf(
			"%s changed while it was being edited: close the runtime that owns it and try again",
			path), &cleanup)
	}
	if remove {
		if err := os.Remove(held); err != nil {
			cleanup = false
			return fmt.Errorf("remove %s; isolated file preserved at %s: %w", path, held, err)
		}
		return nil
	}
	if err := renameNoReplaceFile(staged, path); err != nil {
		cleanup = false
		return fmt.Errorf("restore %s; replacement preserved at %s: %w", path, staged, err)
	}
	if err := os.Remove(held); err != nil {
		cleanup = false
		return fmt.Errorf("remove replaced %s; isolated file preserved at %s: %w", path, held, err)
	}
	return nil
}

func preserveExchangedFile(path, held string, cause error, cleanup *bool) error {
	if err := renameNoReplaceFile(held, path); err != nil {
		*cleanup = false
		return fmt.Errorf("%v; current file preserved at %s: %w", cause, held, err)
	}
	return cause
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
		if err = renameNoReplaceFile(staged, path); err != nil {
			if os.IsExist(err) {
				return createCollisionError(path)
			}
			if errors.Is(err, errAtomicNoReplaceUnsupported) {
				return fmt.Errorf("cannot safely create %s: %w", path, err)
			}
			return fmt.Errorf("atomically create %s: %w", path, err)
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

func createCollisionError(path string) error {
	return fmt.Errorf("%s appeared before it could be created; existing file was preserved", path)
}
