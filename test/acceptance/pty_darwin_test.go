//go:build acceptance

package acceptance

import (
	"bytes"
	"fmt"
	"os"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// openPTY returns a pseudo-terminal pair so the real binary sees a terminal
// on stdin, the only condition under which it asks a [y/N] question.
func openPTY() (master, slave *os.File, err error) {
	master, err = os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		return nil, nil, err
	}
	fd := master.Fd()
	var name [128]byte
	for _, step := range []struct {
		request uintptr
		arg     uintptr
	}{
		{unix.TIOCPTYGRANT, 0}, {unix.TIOCPTYUNLK, 0},
		{unix.TIOCPTYGNAME, uintptr(unsafe.Pointer(&name[0]))},
	} {
		if _, _, errno := unix.Syscall(unix.SYS_IOCTL, fd, step.request, step.arg); errno != 0 {
			master.Close()
			return nil, nil, fmt.Errorf("pty ioctl %#x: %w", step.request, errno)
		}
	}
	path := string(name[:bytes.IndexByte(name[:], 0)])
	slave, err = os.OpenFile(path, os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		master.Close()
		return nil, nil, err
	}
	return master, slave, nil
}
