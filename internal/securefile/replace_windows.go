//go:build windows

package securefile

import (
	"os"

	"golang.org/x/sys/windows"
)

func renameReplace(staged, target string) error {
	from, to, err := windowsPathPair(staged, target)
	if err != nil {
		return err
	}
	if err := windows.MoveFileEx(from, to,
		windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH); err != nil {
		return &os.PathError{Op: "replace", Path: target, Err: err}
	}
	return nil
}
