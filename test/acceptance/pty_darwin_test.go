//go:build acceptance

package acceptance

import (
	"bytes"
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/unix"
)

// ptySlaveName grants and unlocks the slave side of master and names it.
func ptySlaveName(master *os.File) (string, error) {
	var name [128]byte
	for _, step := range []struct {
		request uintptr
		arg     uintptr
	}{
		{unix.TIOCPTYGRANT, 0}, {unix.TIOCPTYUNLK, 0},
		{unix.TIOCPTYGNAME, uintptr(unsafe.Pointer(&name[0]))},
	} {
		if _, _, errno := unix.Syscall(unix.SYS_IOCTL, master.Fd(), step.request, step.arg); errno != 0 {
			return "", fmt.Errorf("pty ioctl %#x: %w", step.request, errno)
		}
	}
	return string(name[:bytes.IndexByte(name[:], 0)]), nil
}
