//go:build unix

package store

import (
	"errors"

	"golang.org/x/sys/unix"
)

func pidExists(pid int) (bool, error) {
	err := unix.Kill(pid, 0)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, unix.ESRCH) {
		return false, nil
	}
	if errors.Is(err, unix.EPERM) {
		return true, nil
	}
	return false, err
}
