//go:build !windows

package vector

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

func lockFile(path string) (func() error, error) {
	if err := ensureLockFilePlatform(path); err != nil {
		return nil, err
	}
	return lock(path, os.O_RDWR|unix.O_NOFOLLOW)
}

func ensureLockFilePlatform(path string) error {
	created, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR|unix.O_NOFOLLOW, 0o600)
	if err == nil {
		if err := alignCreatedLockOwner(path, created); err != nil {
			removeCreatedLock(path, created)
			return err
		}
		return created.Close()
	}
	if !os.IsExist(err) {
		return err
	}
	existing, err := os.OpenFile(path, os.O_RDWR|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	return existing.Close()
}

func removeCreatedLock(path string, file *os.File) {
	createdInfo, statErr := file.Stat()
	currentInfo, currentErr := os.Stat(path)
	if statErr == nil && currentErr == nil && os.SameFile(createdInfo, currentInfo) {
		_ = os.Remove(path)
	}
	_ = file.Close()
}

func alignCreatedLockOwner(path string, file *os.File) error {
	parent, err := os.Stat(filepath.Dir(path))
	if err != nil {
		return err
	}
	parentStat, ok := parent.Sys().(*syscall.Stat_t)
	if !ok {
		return nil
	}
	return file.Chown(int(parentStat.Uid), -1)
}

func lockSharedFile(path string) (func() error, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_SH); err != nil {
		file.Close()
		return nil, err
	}
	release := func() error {
		unlockErr := unix.Flock(int(file.Fd()), unix.LOCK_UN)
		closeErr := file.Close()
		if unlockErr != nil {
			return unlockErr
		}
		return closeErr
	}
	if err := validateExistingLock(path, file, release); err != nil {
		return nil, err
	}
	return release, nil
}

func tryLockExisting(path string) (func() error, bool, error) {
	file, err := os.OpenFile(path, os.O_RDWR|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, false, err
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		file.Close()
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, true, nil
		}
		return nil, false, err
	}
	release := func() error {
		unlockErr := unix.Flock(int(file.Fd()), unix.LOCK_UN)
		closeErr := file.Close()
		if unlockErr != nil {
			return unlockErr
		}
		return closeErr
	}
	if err := validateExistingLock(path, file, release); err != nil {
		return nil, false, err
	}
	return release, false, nil
}

func lock(path string, flags int) (func() error, error) {
	file, err := os.OpenFile(path, flags, 0o600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX); err != nil {
		file.Close()
		return nil, err
	}
	release := func() error {
		unlockErr := unix.Flock(int(file.Fd()), unix.LOCK_UN)
		closeErr := file.Close()
		if unlockErr != nil {
			return unlockErr
		}
		return closeErr
	}
	if err := validateExistingLock(path, file, release); err != nil {
		return nil, err
	}
	return release, nil
}
