//go:build !windows

package securefile

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// Lock takes an advisory cross-process lock on a file beside the protected data.
func Lock(path string) (func() error, error) {
	return lock(path, os.O_CREATE|os.O_RDWR, unix.LOCK_EX)
}

func LockExisting(path string) (func() error, error) {
	return lock(path, os.O_RDWR, unix.LOCK_EX)
}

// TryLock takes an existing lock without waiting; it returns ErrHeld if busy.
func TryLock(path string) (func() error, error) {
	return lock(path, os.O_RDWR, unix.LOCK_EX|unix.LOCK_NB)
}

func lock(path string, flags, operation int) (func() error, error) {
	file, err := os.OpenFile(path, flags, 0o600)
	if err != nil {
		return nil, err
	}
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		return nil, err
	}
	if err := unix.Flock(int(file.Fd()), operation); err != nil {
		file.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			err = ErrHeld
		}
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
