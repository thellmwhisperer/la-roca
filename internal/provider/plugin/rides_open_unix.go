//go:build !windows

package plugin

import (
	"os"

	"golang.org/x/sys/unix"
)

func openOperatorRide(path string, nonblock bool) (*os.File, error) {
	flags := unix.O_RDONLY
	if nonblock {
		flags |= unix.O_NONBLOCK
	}
	return os.OpenFile(path, flags, 0)
}
