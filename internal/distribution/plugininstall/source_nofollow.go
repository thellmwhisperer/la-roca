//go:build !windows

package plugininstall

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// openRegularSource opens a payload only if the path itself is a regular file,
// and returns the descriptor its caller must read the bytes from: a check that
// only stats the path leaves room for the file to be replaced before the read.
// O_NOFOLLOW is what refuses a symlink at open time, so ELOOP here is the swap
// being caught rather than an error to report as one. O_NONBLOCK keeps a fifo
// left in the source from hanging the install before the mode is looked at.
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
