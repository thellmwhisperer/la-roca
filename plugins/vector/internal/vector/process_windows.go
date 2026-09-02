//go:build windows

package vector

import (
	"errors"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

const stillActiveExitCode = 259

var replaceFileW = windows.NewLazySystemDLL("kernel32.dll").NewProc("ReplaceFileW")

func processAlive(pid int) bool {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(handle)
	var code uint32
	return windows.GetExitCodeProcess(handle, &code) == nil && code == stillActiveExitCode
}

func processStartUnixNano(pid int) (int64, error) {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return 0, err
	}
	defer windows.CloseHandle(handle)
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(handle, &created, &exited, &kernel, &user); err != nil {
		return 0, err
	}
	return created.Nanoseconds(), nil
}

func replaceFile(source, destination string) error {
	replacement, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	replaced, err := windows.UTF16PtrFromString(destination)
	if err != nil {
		return err
	}
	if _, err := os.Stat(destination); errors.Is(err, os.ErrNotExist) {
		if err := windows.MoveFileEx(replacement, replaced, windows.MOVEFILE_WRITE_THROUGH); err != nil {
			return os.NewSyscallError("MoveFileExW", err)
		}
		return nil
	} else if err != nil {
		return err
	}
	result, _, callErr := replaceFileW.Call(
		uintptr(unsafe.Pointer(replaced)),
		uintptr(unsafe.Pointer(replacement)),
		0,
		0,
		0,
		0,
	)
	if result == 0 {
		return os.NewSyscallError("ReplaceFileW", callErr)
	}
	return nil
}
