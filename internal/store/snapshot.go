package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"
)

var snapshotArtifacts = [...]string{"", "-wal", "-journal"}

const (
	snapshotDirectoryPrefix = "roca-read-only-snapshot-"
	snapshotCopyBufferSize  = 128 * 1024
)

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
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	tempRoot := os.TempDir()
	if err := reapReadOnlySnapshots(ctx, tempRoot, time.Now()); err != nil {
		return nil, err
	}
	directory, err := os.MkdirTemp(tempRoot, snapshotDirectoryPrefix)
	if err != nil {
		return nil, fmt.Errorf("create read-only snapshot directory: %w", err)
	}
	if err := writeSnapshotLease(directory, currentSnapshotLease()); err != nil {
		return nil, cleanupSnapshotDirectory(directory, fmt.Errorf("write snapshot lease: %w", err))
	}
	for attempt := range 3 {
		if err := ctx.Err(); err != nil {
			return nil, cleanupSnapshotDirectory(directory, err)
		}
		before, err := inspectSnapshotSource(abs)
		if err != nil {
			return nil, cleanupSnapshotDirectory(directory, err)
		}
		attemptDirectory := filepath.Join(directory, fmt.Sprintf("%d", attempt))
		if err := os.Mkdir(attemptDirectory, 0o700); err != nil {
			return nil, cleanupSnapshotDirectory(directory, err)
		}
		destination := filepath.Join(attemptDirectory, filepath.Base(abs))
		if err := copySnapshotSource(ctx, abs, destination); err != nil {
			after, inspectErr := inspectSnapshotSource(abs)
			if inspectErr == nil && before != after {
				if cleanupErr := cleanupSnapshotDirectory(attemptDirectory, nil); cleanupErr != nil {
					return nil, cleanupSnapshotDirectory(directory, cleanupErr)
				}
				continue
			}
			return nil, cleanupSnapshotDirectory(directory, err)
		}
		after, err := inspectSnapshotSource(abs)
		if err != nil {
			return nil, cleanupSnapshotDirectory(directory, err)
		}
		if before != after {
			if err := cleanupSnapshotDirectory(attemptDirectory, nil); err != nil {
				return nil, cleanupSnapshotDirectory(directory, err)
			}
			continue
		}
		snapshot, err := openCopiedSnapshot(ctx, destination, directory)
		if err != nil {
			final, inspectErr := inspectSnapshotSource(abs)
			if inspectErr == nil && before != final {
				if cleanupErr := cleanupSnapshotDirectory(attemptDirectory, nil); cleanupErr != nil {
					return nil, cleanupSnapshotDirectory(directory, cleanupErr)
				}
				continue
			}
			return nil, cleanupSnapshotDirectory(directory, err)
		}
		final, err := inspectSnapshotSource(abs)
		if err == nil && before == final {
			return snapshot, nil
		}
		closeErr := snapshot.database.Close()
		cleanupErr := cleanupSnapshotDirectory(attemptDirectory, nil)
		if closeErr != nil || cleanupErr != nil {
			return nil, cleanupSnapshotDirectory(directory, errors.Join(closeErr, cleanupErr))
		}
		if err != nil {
			return nil, cleanupSnapshotDirectory(directory, err)
		}
	}
	return nil, cleanupSnapshotDirectory(directory,
		fmt.Errorf("database %q kept changing while it was snapshotted", abs))
}

func cleanupSnapshotDirectory(directory string, cause error) error {
	if err := os.RemoveAll(directory); err != nil {
		return errors.Join(cause, fmt.Errorf("remove read-only snapshot %q: %w", directory, err))
	}
	return cause
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

func copySnapshotSource(ctx context.Context, source, destination string) error {
	for _, suffix := range snapshotArtifacts {
		if err := ctx.Err(); err != nil {
			return err
		}
		input, err := os.Open(source + suffix)
		if os.IsNotExist(err) && suffix != "" {
			continue
		}
		if err != nil {
			return fmt.Errorf("open snapshot source %q: %w", source+suffix, err)
		}
		output, err := os.OpenFile(destination+suffix, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return fmt.Errorf("create snapshot copy %q: %w", destination+suffix,
				errors.Join(err, input.Close()))
		}
		_, copyErr := io.CopyBuffer(output, &contextReader{ctx: ctx, reader: input},
			make([]byte, snapshotCopyBufferSize))
		err = errors.Join(copyErr, ctx.Err(), input.Close(), output.Close())
		if err != nil {
			return fmt.Errorf("copy snapshot source %q: %w", source+suffix, err)
		}
	}
	return nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (reader *contextReader) Read(buffer []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	return reader.reader.Read(buffer)
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
		snapshot.err = cleanupSnapshotDirectory(snapshot.directory, snapshot.database.Close())
	})
	return snapshot.err
}
