package cli

import (
	"fmt"

	"golang.org/x/sys/unix"
)

func zcodeRootIdentity(path string) (string, error) {
	identity, err := zcodeUnixRootIdentity(path)
	if err != nil {
		return "", err
	}
	var statx unix.Statx_t
	if err := unix.Statx(unix.AT_FDCWD, path, unix.AT_SYMLINK_NOFOLLOW,
		unix.STATX_BTIME, &statx); err != nil || statx.Mask&unix.STATX_BTIME == 0 {
		return identity, nil
	}
	return fmt.Sprintf("%s:%x:%x", identity, statx.Btime.Sec, statx.Btime.Nsec), nil
}
