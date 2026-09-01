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
	errAtomicExchangeUnsupported  = errors.New("atomic exchange is unsupported")
	renameNoReplaceFile           = renameNoReplace
	exchangeFiles                 = exchange
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
	if remove {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			return nil
		}
	}
	staged, err := stage(filepath.Dir(path), filepath.Base(path), data, mode)
	if err != nil {
		return err
	}
	keep := false
	defer func() {
		if !keep {
			os.Remove(staged)
		}
	}()
	if err := exchangeFiles(staged, path); err != nil {
		return err
	}
	current, err := os.ReadFile(staged)
	if err != nil || string(current) != string(expected) {
		cause := err
		if cause == nil {
			cause = fmt.Errorf("%s changed while it was being edited: close the runtime that owns it and try again", path)
		}
		if restoreErr := exchangeFiles(staged, path); restoreErr != nil {
			keep = true
			return fmt.Errorf("%v; displaced file preserved at %s: %w", cause, staged, restoreErr)
		}
		return cause
	}
	if remove {
		if err := os.Remove(path); err != nil {
			keep = true
			return fmt.Errorf("remove %s; previous file preserved at %s: %w", path, staged, err)
		}
	}
	return nil
}

func stage(dir, base string, data []byte, mode os.FileMode) (string, error) {
	file, err := os.CreateTemp(dir, "."+base+"-exchange-")
	if err != nil {
		return "", err
	}
	path := file.Name()
	if err := file.Chmod(mode); err != nil {
		file.Close()
		os.Remove(path)
		return "", err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		os.Remove(path)
		return "", err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		os.Remove(path)
		return "", err
	}
	if err := file.Close(); err != nil {
		os.Remove(path)
		return "", err
	}
	return path, nil
}

func conditionalExchange(staged, path string, previous []byte) error {
	if err := exchangeFiles(staged, path); err != nil {
		return err
	}
	current, err := os.ReadFile(staged)
	if err == nil && string(current) == string(previous) {
		return nil
	}
	cause := err
	if cause == nil {
		cause = fmt.Errorf("%s changed while it was being edited: close the runtime that owns it and try again", path)
	}
	if restoreErr := exchangeFiles(staged, path); restoreErr != nil {
		recovery := staged + ".recovery"
		if renameErr := os.Rename(staged, recovery); renameErr != nil {
			return fmt.Errorf("%v; preserve displaced file %s: %v; restore: %w", cause, staged, renameErr, restoreErr)
		}
		return fmt.Errorf("%v; displaced file preserved at %s: %w", cause, recovery, restoreErr)
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
		os.Remove(staged)
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
	} else if previous != nil {
		if err = conditionalExchange(staged, path, previous); err != nil {
			return err
		}
	} else if err = os.Rename(staged, path); err != nil {
		return err
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
