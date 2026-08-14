//go:build !windows

package rocacron

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// coreLockFree probes the flock without creating it and releases immediately.
// The train never holds the core lock while a ride runs; the invoked command
// remains the only owner of its own lock discipline.
func coreLockFree(path string) (bool, error) {
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if os.IsNotExist(err) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	defer file.Close()
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return false, nil
		}
		return false, err
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_UN); err != nil {
		return false, err
	}
	return true, nil
}
