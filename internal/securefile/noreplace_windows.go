//go:build windows

package securefile

import (
	"os"

	"golang.org/x/sys/windows"
)

func renameNoReplace(staged, target string) error {
	from, err := windows.UTF16PtrFromString(staged)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	err = windows.MoveFile(from, to)
	switch err {
	case windows.ERROR_ALREADY_EXISTS, windows.ERROR_FILE_EXISTS:
		return os.ErrExist
	case windows.ERROR_INVALID_FUNCTION, windows.ERROR_NOT_SUPPORTED:
		return errAtomicNoReplaceUnsupported
	default:
		return err
	}
}
