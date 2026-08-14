//go:build !windows

package plugininstall

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func openRegularSource(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		info, statErr := os.Lstat(path)
		if errors.Is(err, unix.ELOOP) || statErr != nil || !info.Mode().IsRegular() {
			return nil, errSourceNotRegular
		}
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	file := os.NewFile(uintptr(fd), path)
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() {
		file.Close()
		return nil, errSourceNotRegular
	}
	return file, nil
}
