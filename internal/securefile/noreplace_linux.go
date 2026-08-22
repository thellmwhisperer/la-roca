//go:build linux

package securefile

import "golang.org/x/sys/unix"

func renameNoReplace(staged, target string) error {
	err := unix.Renameat2(unix.AT_FDCWD, staged, unix.AT_FDCWD, target, unix.RENAME_NOREPLACE)
	switch err {
	case unix.ENOSYS, unix.EINVAL, unix.EOPNOTSUPP:
		return errAtomicNoReplaceUnsupported
	default:
		return err
	}
}
