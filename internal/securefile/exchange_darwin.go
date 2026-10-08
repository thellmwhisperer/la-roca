//go:build darwin

package securefile

import "golang.org/x/sys/unix"

func exchange(staged, target string) error {
	err := unix.RenamexNp(staged, target, unix.RENAME_SWAP)
	switch err {
	case unix.ENOSYS, unix.EINVAL, unix.ENOTSUP:
		return errAtomicExchangeUnsupported
	default:
		return err
	}
}
