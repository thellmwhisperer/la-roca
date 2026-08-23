package securefile

import (
	"errors"
	"os"
)

// ErrBusy is returned by TryLock when another process already holds the lease.
var ErrBusy = errors.New("file is locked")

func validateExistingLock(path string, file *os.File, release func() error) error {
	held, err := file.Stat()
	if err != nil {
		release()
		return err
	}
	current, err := os.Stat(path)
	if err != nil {
		release()
		return err
	}
	if os.SameFile(held, current) {
		return nil
	}
	release()
	return &os.PathError{Op: "lock", Path: path, Err: os.ErrNotExist}
}
