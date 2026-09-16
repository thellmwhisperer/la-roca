//go:build !windows

package rocavector

import (
	"errors"
	"os"

	"github.com/thellmwhisperer/la-roca/internal/securefile"
	"golang.org/x/sys/unix"
)

func tryExclusiveFileLock(path string, alignOwner bool) (func() error, bool, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	created := err == nil
	if err != nil {
		if !os.IsExist(err) {
			return nil, false, err
		}
		file, err = os.OpenFile(path, os.O_RDWR, 0o600)
		if err != nil {
			return nil, false, err
		}
	}
	if alignOwner && created {
		if err := securefile.AlignToParentOwner(path); err != nil {
			file.Close()
			return nil, false, err
		}
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		file.Close()
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, true, nil
		}
		return nil, false, err
	}
	release := func() error {
		unlockErr := unix.Flock(int(file.Fd()), unix.LOCK_UN)
		closeErr := file.Close()
		return errors.Join(unlockErr, closeErr)
	}
	return validateExclusiveFileLock(path, file, release)
}
