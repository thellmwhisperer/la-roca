//go:build windows

package securefile

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// Lock takes an advisory cross-process lock on a file beside the protected data.
func Lock(path string) (func() error, error) {
	return lock(path, windows.OPEN_ALWAYS, false)
}

func LockExisting(path string) (func() error, error) {
	return lock(path, windows.OPEN_EXISTING, false)
}

// TryLock acquires an exclusive lock without waiting. The file must already
// exist. ErrBusy means another process holds it.
func TryLock(path string) (func() error, error) {
	return lock(path, windows.OPEN_EXISTING, true)
}

func lock(path string, disposition uint32, nonBlocking bool) (func() error, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(name,
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, disposition, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	file := os.NewFile(uintptr(handle), path)
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		return nil, err
	}
	lockFlags := windows.LOCKFILE_EXCLUSIVE_LOCK
	if nonBlocking {
		lockFlags |= windows.LOCKFILE_FAIL_IMMEDIATELY
	}
	overlapped := &windows.Overlapped{}
	if err := windows.LockFileEx(windows.Handle(file.Fd()), lockFlags, 0, 1, 0, overlapped); err != nil {
		file.Close()
		if nonBlocking && (errors.Is(err, windows.ERROR_LOCK_VIOLATION) || errors.Is(err, windows.ERROR_IO_PENDING)) {
			return nil, ErrBusy
		}
		return nil, err
	}
	release := func() error {
		unlockErr := windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, overlapped)
		closeErr := file.Close()
		if unlockErr != nil {
			return unlockErr
		}
		return closeErr
	}
	if disposition == windows.OPEN_EXISTING {
		if err := validateExistingLock(path, file, release); err != nil {
			return nil, err
		}
	}
	return release, nil
}
