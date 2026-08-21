package store

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sync"
)

var snapshotArtifacts = [...]string{"", "-wal", "-journal"}

type ReadOnlySnapshot struct {
	database  *sql.DB
	uri       string
	directory string
	once      sync.Once
	err       error
}

type snapshotArtifactState struct {
	exists  bool
	size    int64
	modTime int64
}

type snapshotSourceState [len(snapshotArtifacts)]snapshotArtifactState

func OpenReadOnlySnapshot(ctx context.Context, path string) (*ReadOnlySnapshot, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	directory, err := os.MkdirTemp("", "roca-read-only-snapshot-")
	if err != nil {
		return nil, fmt.Errorf("create read-only snapshot directory: %w", err)
	}
	for attempt := range 3 {
		before, err := inspectSnapshotSource(abs)
		if err != nil {
			_ = os.RemoveAll(directory)
			return nil, err
		}
		attemptDirectory := filepath.Join(directory, fmt.Sprintf("%d", attempt))
		if err := os.Mkdir(attemptDirectory, 0o700); err != nil {
			_ = os.RemoveAll(directory)
			return nil, err
		}
		destination := filepath.Join(attemptDirectory, filepath.Base(abs))
		if err := copySnapshotSource(abs, destination); err != nil {
			after, inspectErr := inspectSnapshotSource(abs)
			if inspectErr == nil && before != after {
				_ = os.RemoveAll(attemptDirectory)
				continue
			}
			_ = os.RemoveAll(directory)
			return nil, err
		}
		after, err := inspectSnapshotSource(abs)
		if err != nil {
			_ = os.RemoveAll(directory)
			return nil, err
		}
		if before != after {
			_ = os.RemoveAll(attemptDirectory)
			continue
		}
		snapshot, err := openCopiedSnapshot(ctx, destination, directory)
		if err != nil {
			final, inspectErr := inspectSnapshotSource(abs)
			if inspectErr == nil && before != final {
				_ = os.RemoveAll(attemptDirectory)
				continue
			}
			_ = os.RemoveAll(directory)
			return nil, err
		}
		final, err := inspectSnapshotSource(abs)
		if err == nil && before == final {
			return snapshot, nil
		}
		_ = snapshot.database.Close()
		_ = os.RemoveAll(attemptDirectory)
		if err != nil {
			_ = os.RemoveAll(directory)
			return nil, err
		}
	}
	_ = os.RemoveAll(directory)
	return nil, fmt.Errorf("database %q kept changing while it was snapshotted", abs)
}

func inspectSnapshotSource(path string) (snapshotSourceState, error) {
	var state snapshotSourceState
	for index, suffix := range snapshotArtifacts {
		info, err := os.Stat(path + suffix)
		if err == nil {
			if !info.Mode().IsRegular() {
				return snapshotSourceState{}, fmt.Errorf("snapshot source %q is not a regular file", path+suffix)
			}
			state[index] = snapshotArtifactState{
				exists: true, size: info.Size(), modTime: info.ModTime().UnixNano(),
			}
			continue
		}
		if !os.IsNotExist(err) || suffix == "" {
			return snapshotSourceState{}, fmt.Errorf("inspect snapshot source %q: %w", path+suffix, err)
		}
	}
	return state, nil
}

func copySnapshotSource(source, destination string) error {
	for _, suffix := range snapshotArtifacts {
		input, err := os.Open(source + suffix)
		if os.IsNotExist(err) && suffix != "" {
			continue
		}
		if err != nil {
			return fmt.Errorf("open snapshot source %q: %w", source+suffix, err)
		}
		output, err := os.OpenFile(destination+suffix, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			input.Close()
			return fmt.Errorf("create snapshot copy %q: %w", destination+suffix, err)
		}
		_, err = io.Copy(output, input)
		input.Close()
		if closeErr := output.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			return fmt.Errorf("copy snapshot source %q: %w", source+suffix, err)
		}
	}
	return nil
}

func openCopiedSnapshot(ctx context.Context, path, directory string) (*ReadOnlySnapshot, error) {
	_, writableURI, err := sqliteFileDSN(path, url.Values{
		"mode": {"rw"},
		"_pragma": {
			fmt.Sprintf("busy_timeout(%d)", busyTimeout.Milliseconds()),
			"query_only(1)",
		},
	})
	if err != nil {
		return nil, err
	}
	database, err := sql.Open("sqlite", writableURI)
	if err != nil {
		return nil, err
	}
	database.SetMaxOpenConns(1)
	var schemaVersion int
	if err := database.QueryRowContext(ctx, "PRAGMA schema_version").Scan(&schemaVersion); err != nil {
		database.Close()
		return nil, err
	}
	_, uri, err := sqliteFileDSN(path, url.Values{
		"mode": {"ro"},
		"_pragma": {
			fmt.Sprintf("busy_timeout(%d)", busyTimeout.Milliseconds()),
			"query_only(1)",
		},
	})
	if err != nil {
		database.Close()
		return nil, err
	}
	return &ReadOnlySnapshot{database: database, uri: uri, directory: directory}, nil
}

func (snapshot *ReadOnlySnapshot) SQL() *sql.DB {
	return snapshot.database
}

func (snapshot *ReadOnlySnapshot) URI() string {
	return snapshot.uri
}

func (snapshot *ReadOnlySnapshot) Close() error {
	snapshot.once.Do(func() {
		snapshot.err = snapshot.database.Close()
		if err := os.RemoveAll(snapshot.directory); snapshot.err == nil {
			snapshot.err = err
		}
	})
	return snapshot.err
}
