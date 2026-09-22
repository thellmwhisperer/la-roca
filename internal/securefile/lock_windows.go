//go:build windows

package securefile

import (
	"os"

	"golang.org/x/sys/windows"
)

// Lock takes an advisory cross-process lock on a file beside the protected data.
func Lock(path string) (func() error, error) {
	return lock(path, windows.OPEN_ALWAYS)
}

func LockExisting(path string) (func() error, error) {
	return lock(path, windows.OPEN_EXISTING)
}

func lock(path string, disposition uint32) (func() error, error) {
	file, err := openWindowsFile(path, disposition, windows.FILE_ATTRIBUTE_NORMAL)
	if err != nil {
		return nil, err
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return nil, err
	}
	release, err := lockWindowsFile(file)
	if err != nil {
		return nil, err
	}
	if disposition == windows.OPEN_EXISTING {
		if err := validateExistingLock(path, file, release); err != nil {
			return nil, err
		}
	}
	return release, nil
}

func openWindowsFile(path string, disposition, flags uint32) (*os.File, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(name,
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, disposition, flags, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(handle), path), nil
}

func lockWindowsFile(file *os.File) (func() error, error) {
	overlapped := &windows.Overlapped{}
	if err := windows.LockFileEx(windows.Handle(file.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, overlapped); err != nil {
		_ = file.Close()
		return nil, err
	}
	return func() error {
		unlockErr := windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, overlapped)
		closeErr := file.Close()
		if unlockErr != nil {
			return unlockErr
		}
		return closeErr
	}, nil
}
