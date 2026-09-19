package vector

import (
	"context"
	"errors"
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

// ClearUnheldIndexLocks removes leftover sidecar lock files after an ingest
// that still owns the worker claim. A file another process holds is left
// alone. Status still reports a crash leftover as stale until the next ingest.
func ClearUnheldIndexLocks(sidecarPaths []string) error {
	var cleanupErr error
	for _, sidecar := range sidecarPaths {
		if sidecar == "" {
			continue
		}
		path := sidecar + ".index.lock"
		held, err := os.Stat(path)
		if err != nil {
			continue
		}
		release, busy, err := tryLockExisting(path)
		if err != nil || busy {
			continue
		}
		current, err := os.Stat(path)
		if err != nil || !os.SameFile(held, current) {
			cleanupErr = errors.Join(cleanupErr, release())
			continue
		}
		removeErr := os.Remove(path)
		releaseErr := release()
		if removeErr != nil && !os.IsNotExist(removeErr) {
			cleanupErr = errors.Join(cleanupErr, removeErr)
		}
		cleanupErr = errors.Join(cleanupErr, releaseErr)
	}
	return cleanupErr
}

func ensureLockFile(path string) error {
	return ensureLockFilePlatform(path)
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
