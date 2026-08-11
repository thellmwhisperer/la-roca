package securefile

import "os"

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
