package securefile

import (
	"errors"
	"os"
)

// ErrHeld means another lock holder prevented a non-blocking acquisition.
var ErrHeld = errors.New("lock is held")

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
