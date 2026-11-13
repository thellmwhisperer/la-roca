//go:build acceptance

package acceptance

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// ptySlaveName unlocks the slave side of master and names it.
func ptySlaveName(master *os.File) (string, error) {
	fd := int(master.Fd())
	if err := unix.IoctlSetPointerInt(fd, unix.TIOCSPTLCK, 0); err != nil {
		return "", fmt.Errorf("unlock pty: %w", err)
	}
	number, err := unix.IoctlGetUint32(fd, unix.TIOCGPTN)
	if err != nil {
		return "", fmt.Errorf("pty number: %w", err)
	}
	return fmt.Sprintf("/dev/pts/%d", number), nil
}
