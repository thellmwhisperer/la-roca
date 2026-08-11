//go:build !windows

package securefile

import (
	"os"

	"golang.org/x/sys/unix"
)

// Lock takes an advisory cross-process lock on a file beside the protected data.
func Lock(path string) (func() error, error) {
	return lock(path, os.O_CREATE|os.O_RDWR)
}

func LockExisting(path string) (func() error, error) {
	return lock(path, os.O_RDWR)
}

func lock(path string, flags int) (func() error, error) {
	file, err := os.OpenFile(path, flags, 0o600)
	if err != nil {
		return nil, err
	}
	if err := file.Chmod(0o600); err != nil {
		file.Close()
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
		held, heldErr := file.Stat()
		current, currentErr := os.Stat(path)
		if heldErr != nil || currentErr != nil || !os.SameFile(held, current) {
			release()
			switch {
			case heldErr != nil:
				return nil, heldErr
			case currentErr != nil:
				return nil, currentErr
			default:
				return nil, &os.PathError{Op: "lock", Path: path, Err: os.ErrNotExist}
			}
		}
	}
	return release, nil
}
