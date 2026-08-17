//go:build !windows

package vector

import (
	"errors"
	"os"
	"syscall"
)

func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func replaceFile(source, destination string) error {
	return os.Rename(source, destination)
}
