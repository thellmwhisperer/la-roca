package vector

import (
	"context"
	"os"
)

type lockAttempt struct {
	release func() error
	err     error
}

func lockIndex(ctx context.Context, path string, contended func(string)) (func() error, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := ensureLockFile(path); err != nil {
		return nil, err
	}
	release, busy, err := tryLockExisting(path)
	switch {
	case err == nil && !busy:
		return release, nil
	case err != nil && !os.IsNotExist(err):
		return nil, err
	case busy && contended != nil:
		contended(path)
	}
	acquisition := make(chan lockAttempt, 1)
	go func() {
		blocking, err := lockFile(path)
		acquisition <- lockAttempt{release: blocking, err: err}
	}()
	select {
	case attempt := <-acquisition:
		return attempt.release, attempt.err
	case <-ctx.Done():
		go func() {
			if attempt := <-acquisition; attempt.err == nil {
				_ = attempt.release()
			}
		}()
		return nil, ctx.Err()
	}
}

func tryLockIndex(path string) (func() error, bool, error) {
	if err := ensureLockFile(path); err != nil {
		return nil, false, err
	}
	return tryLockExisting(path)
}

func ensureLockFile(path string) error {
	created, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	return created.Close()
}

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
