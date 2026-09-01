package rocavector

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	relocationLockFilename = ".roca-vector.relocation.lock"
	vectorDatabaseFilename = "vector.db"
	workerClaimFilename    = ".worker"
)

type workerReservation struct {
	info os.FileInfo
}

func RelocationLockPath(root string) string {
	return filepath.Join(root, relocationLockFilename)
}

func WorkerRunning(root string) bool {
	directories := []string{
		filepath.Join(root, LegacyName, StateDir),
		filepath.Join(root, Name, StateDir),
		filepath.Join(root, "."+Name+".previous", StateDir),
	}
	if configured := strings.TrimSpace(os.Getenv("ROCA_VECTOR_STATE_DIR")); configured != "" {
		if absolute, err := filepath.Abs(configured); err == nil {
			directories = append(directories, absolute)
		}
	}
	for _, directory := range directories {
		if root == "" && !filepath.IsAbs(directory) {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(directory, workerClaimFilename))
		if err != nil {
			continue
		}
		pid := workerPID(raw)
		if pid > 0 && processAlive(pid) {
			return true
		}
	}
	return false
}

func validateExclusiveFileLock(path string, file *os.File, release func() error) (func() error, bool, error) {
	held, err := file.Stat()
	if err != nil {
		_ = release()
		return nil, false, err
	}
	current, err := os.Stat(path)
	if err != nil || !os.SameFile(held, current) {
		_ = release()
		if err != nil {
			return nil, false, err
		}
		return nil, false, &os.PathError{Op: "lock", Path: path, Err: os.ErrNotExist}
	}
	return release, false, nil
}

func migrationGuard(root string) (func() error, error) {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("create plugin directory: %w", err)
	}
	releaseRelocation, busy, err := tryExclusiveFileLock(RelocationLockPath(root))
	if err != nil {
		return nil, fmt.Errorf("lock vector relocation: %w", err)
	}
	if busy {
		return nil, fmt.Errorf("vector state is active; retry the update after vector commands finish")
	}
	states := []string{
		filepath.Join(root, LegacyName, StateDir),
		filepath.Join(root, Name, StateDir),
		filepath.Join(root, "."+Name+".previous", StateDir),
	}
	var releases []func() error
	var reservations []workerReservation
	release := func() error {
		var releaseErr error
		for index := len(releases) - 1; index >= 0; index-- {
			releaseErr = errors.Join(releaseErr, releases[index]())
		}
		for _, reservation := range reservations {
			for _, state := range states {
				path := filepath.Join(state, workerClaimFilename)
				if info, err := os.Lstat(path); err == nil && os.SameFile(reservation.info, info) {
					releaseErr = errors.Join(releaseErr, os.Remove(path))
				}
			}
		}
		return errors.Join(releaseErr, releaseRelocation())
	}
	fail := func(err error) (func() error, error) {
		_ = release()
		return nil, err
	}
	for _, state := range states {
		info, err := os.Stat(state)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return fail(fmt.Errorf("inspect vector state %s: %w", state, err))
		}
		if !info.IsDir() {
			return fail(fmt.Errorf("vector state path %s is not a directory", state))
		}
		reservation, err := reserveWorker(state)
		if err != nil {
			return fail(err)
		}
		reservations = append(reservations, reservation)
		lockPath := filepath.Join(state, vectorDatabaseFilename+".index.lock")
		releaseIndex, busy, err := tryExclusiveFileLock(lockPath)
		if err != nil {
			return fail(fmt.Errorf("lock vector index at %s: %w", lockPath, err))
		}
		if busy {
			return fail(fmt.Errorf("vector index is active at %s; retry the update after it finishes", lockPath))
		}
		releases = append(releases, releaseIndex)
	}
	return release, nil
}

func reserveWorker(state string) (workerReservation, error) {
	path := filepath.Join(state, workerClaimFilename)
	for attempt := 0; attempt < 2; attempt++ {
		file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			if _, err := fmt.Fprintf(file, "%d\n", os.Getpid()); err != nil {
				file.Close()
				_ = os.Remove(path)
				return workerReservation{}, err
			}
			if err := file.Sync(); err != nil {
				file.Close()
				_ = os.Remove(path)
				return workerReservation{}, err
			}
			if err := file.Close(); err != nil {
				_ = os.Remove(path)
				return workerReservation{}, err
			}
			info, err := os.Lstat(path)
			return workerReservation{info: info}, err
		}
		if !os.IsExist(err) {
			return workerReservation{}, fmt.Errorf("reserve vector worker state: %w", err)
		}
		info, statErr := os.Stat(path)
		raw, readErr := os.ReadFile(path)
		if statErr != nil {
			return workerReservation{}, fmt.Errorf("inspect vector worker claim: %w", statErr)
		}
		if readErr != nil {
			return workerReservation{}, fmt.Errorf("read vector worker claim: %w", readErr)
		}
		pid := workerPID(raw)
		fresh := time.Since(info.ModTime()) < 5*time.Minute
		if (pid > 0 && processAlive(pid)) || (pid == 0 && fresh) {
			return workerReservation{}, fmt.Errorf("an active vector worker holds %s; retry the update after it finishes", state)
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return workerReservation{}, fmt.Errorf("remove stale vector worker claim: %w", err)
		}
	}
	return workerReservation{}, fmt.Errorf("vector worker claim changed while it was inspected")
}

func workerPID(raw []byte) int {
	fields := strings.Fields(string(raw))
	if len(fields) == 0 {
		return 0
	}
	pid, _ := strconv.Atoi(fields[0])
	return pid
}
