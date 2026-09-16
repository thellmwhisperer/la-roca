//go:build !windows

package rocavector

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

var chownCreatedLock = func(file *os.File, uid, gid int) error {
	return file.Chown(uid, gid)
}

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
		if err := alignCreatedLockOwner(path, file); err != nil {
			removeCreatedLock(path, file)
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

func removeCreatedLock(path string, file *os.File) {
	createdInfo, statErr := file.Stat()
	currentInfo, currentErr := os.Stat(path)
	if statErr == nil && currentErr == nil && os.SameFile(createdInfo, currentInfo) {
		_ = os.Remove(path)
	}
	_ = file.Close()
}

func alignCreatedLockOwner(path string, file *os.File) error {
	parent, err := os.Stat(filepath.Dir(path))
	if err != nil {
		return err
	}
	parentStat, ok := parent.Sys().(*syscall.Stat_t)
	if !ok {
		return nil
	}
	return chownCreatedLock(file, int(parentStat.Uid), -1)
}
