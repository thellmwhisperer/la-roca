//go:build !windows

package vector

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func lockFile(path string) (func() error, error) {
	return lock(path, os.O_CREATE|os.O_RDWR)
}

func tryLockExisting(path string) (func() error, bool, error) {
	file, err := os.OpenFile(path, os.O_RDWR, 0o600)
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
	if flags&os.O_CREATE == 0 {
		if err := validateExistingLock(path, file, release); err != nil {
			return nil, err
		}
	}
	return release, nil
}
