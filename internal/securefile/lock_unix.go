//go:build !windows

package securefile

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// Lock takes an advisory cross-process lock on a file beside the protected data.
func Lock(path string) (func() error, error) {
	return lock(path, os.O_CREATE|os.O_RDWR, false)
}

func LockExisting(path string) (func() error, error) {
	return lock(path, os.O_RDWR, false)
}

// TryLock acquires an exclusive lock without waiting. The file must already
// exist. ErrBusy means another process holds it.
func TryLock(path string) (func() error, error) {
	return lock(path, os.O_RDWR, true)
}

func lock(path string, flags int, nonBlocking bool) (func() error, error) {
	file, err := os.OpenFile(path, flags, 0o600)
	if err != nil {
		return nil, err
	}
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		return nil, err
	}
	flockFlags := unix.LOCK_EX
	if nonBlocking {
		flockFlags |= unix.LOCK_NB
	}
	if err := unix.Flock(int(file.Fd()), flockFlags); err != nil {
		file.Close()
		if nonBlocking && (errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN)) {
			return nil, ErrBusy
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
