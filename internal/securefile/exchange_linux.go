//go:build linux

package securefile

import "golang.org/x/sys/unix"

func exchange(staged, target string) error {
	err := unix.Renameat2(unix.AT_FDCWD, staged, unix.AT_FDCWD, target, unix.RENAME_EXCHANGE)
	switch err {
	case unix.ENOSYS, unix.EINVAL, unix.EOPNOTSUPP:
		return errAtomicExchangeUnsupported
	default:
		return err
	}
}
