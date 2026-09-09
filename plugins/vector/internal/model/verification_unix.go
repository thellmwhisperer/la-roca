//go:build linux || darwin

package model

import (
	"fmt"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

const verificationAttribute = "user.la-roca.model-verification"

// A content pin, device, inode, size or nanosecond mtime change invalidates
// verification. This is a local cache, not authentication against an owner
// deliberately restoring metadata or forging extended attributes.
func verifiedIdentity(info os.FileInfo, checksum string) string {
	stat := info.Sys().(*syscall.Stat_t)
	return fmt.Sprintf("%s:%d:%d:%d:%d", checksum, stat.Dev, stat.Ino, info.Size(), info.ModTime().UnixNano())
}

func lockVerification(file *os.File) (func(), error) {
	fd := int(file.Fd())
	if err := unix.Flock(fd, unix.LOCK_EX); err != nil {
		return nil, err
	}
	return func() { _ = unix.Flock(fd, unix.LOCK_UN) }, nil
}

func readVerification(file *os.File) string {
	var buffer [256]byte
	n, err := unix.Fgetxattr(int(file.Fd()), verificationAttribute, buffer[:])
	if err != nil {
		return ""
	}
	return string(buffer[:n])
}

func writeVerification(file *os.File, identity string) {
	_ = unix.Fsetxattr(int(file.Fd()), verificationAttribute, []byte(identity), 0)
}
