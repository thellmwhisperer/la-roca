//go:build darwin

package securefile

import "golang.org/x/sys/unix"

func renameNoReplace(staged, target string) error {
	err := unix.RenamexNp(staged, target, unix.RENAME_EXCL)
	switch err {
	case unix.ENOSYS, unix.EINVAL, unix.ENOTSUP:
		return errAtomicNoReplaceUnsupported
	default:
		return err
	}
}
