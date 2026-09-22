//go:build windows

package securefile

import (
	"errors"
	"fmt"
	"os"
	"runtime"

	"golang.org/x/sys/windows"
)

func lockDirectory(path string) (func() error, error) {
	mutex, err := openDirectoryMutex(path)
	if err != nil {
		return nil, err
	}
	runtime.LockOSThread()
	status, err := windows.WaitForSingleObject(mutex, windows.INFINITE)
	if err != nil || (status != windows.WAIT_OBJECT_0 && status != windows.WAIT_ABANDONED) {
		runtime.UnlockOSThread()
		_ = windows.CloseHandle(mutex)
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("wait for publication mutex: unexpected status %d", status)
	}
	return func() error {
		defer runtime.UnlockOSThread()
		return errors.Join(windows.ReleaseMutex(mutex), windows.CloseHandle(mutex))
	}, nil
}

func openDirectoryMutex(path string) (windows.Handle, error) {
	directory, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer directory.Close()
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(directory.Fd()), &info); err != nil {
		return 0, err
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 {
		return 0, &os.PathError{Op: "lock directory", Path: path, Err: windows.ERROR_DIRECTORY}
	}
	name, err := windows.UTF16PtrFromString(fmt.Sprintf(`Global\LaRoca.securefile.%08x.%08x%08x`,
		info.VolumeSerialNumber, info.FileIndexHigh, info.FileIndexLow))
	if err != nil {
		return 0, err
	}
	mutex, err := windows.CreateMutex(nil, false, name)
	if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		err = nil
	}
	return mutex, err
}
