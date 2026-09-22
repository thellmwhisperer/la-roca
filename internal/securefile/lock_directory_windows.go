//go:build windows

package securefile

import "golang.org/x/sys/windows"

func lockDirectory(path string) (func() error, error) {
	file, err := openWindowsFile(path, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS)
	if err != nil {
		return nil, err
	}
	return lockWindowsFile(file)
}
