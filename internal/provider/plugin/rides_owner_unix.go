//go:build !windows

package plugin

import (
	"fmt"
	"os"
	"syscall"
)

func operatorRideFilePermissionsAllowed(_ *os.File, info os.FileInfo) error {
	if info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("is writable by group or others; refuse to run its rides")
	}
	return nil
}

func operatorRideFileOwnedByUser(_ *os.File, info os.FileInfo, euid int) (bool, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false, fmt.Errorf("file owner is unavailable")
	}
	return int(stat.Uid) == euid, nil
}
