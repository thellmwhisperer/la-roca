//go:build windows

package securefile

import (
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

var replaceFileW = windows.NewLazySystemDLL("kernel32.dll").NewProc("ReplaceFileW")

func exchange(staged, target string) error {
	replaced, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	replacement, err := windows.UTF16PtrFromString(staged)
	if err != nil {
		return err
	}
	displacedPath := staged + ".displaced"
	displaced, err := windows.UTF16PtrFromString(displacedPath)
	if err != nil {
		return err
	}
	result, _, callErr := replaceFileW.Call(
		uintptr(unsafe.Pointer(replaced)),
		uintptr(unsafe.Pointer(replacement)),
		uintptr(unsafe.Pointer(displaced)),
		1, 0, 0,
	)
	if result == 0 {
		switch callErr {
		case windows.ERROR_INVALID_FUNCTION, windows.ERROR_NOT_SUPPORTED:
			return errAtomicExchangeUnsupported
		default:
			return callErr
		}
	}
	return os.Rename(displacedPath, staged)
}
