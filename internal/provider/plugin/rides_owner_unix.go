//go:build !windows

package plugin

import (
	"fmt"
	"os"
	"syscall"
)

func operatorRideFileOwnedByUser(_ *os.File, info os.FileInfo, euid int) (bool, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false, fmt.Errorf("file owner is unavailable")
	}
	return int(stat.Uid) == euid, nil
}
