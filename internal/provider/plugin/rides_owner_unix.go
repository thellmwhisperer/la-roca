//go:build !windows

package plugin

import (
	"fmt"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func openOperatorRide(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}

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
