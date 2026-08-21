package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"net/url"
	"os"
	"sync"
	"sync/atomic"

	sqlite "modernc.org/sqlite"
)

type sqliteBackuper interface {
	NewBackup(string) (*sqlite.Backup, error)
}

type ReadOnlySnapshot struct {
	database *sql.DB
	uri      string
	once     sync.Once
	err      error
}

var snapshotSequence atomic.Uint64

type snapshotConnector struct {
	mu         sync.Mutex
	connection driver.Conn
}

func (connector *snapshotConnector) Connect(context.Context) (driver.Conn, error) {
	connector.mu.Lock()
	defer connector.mu.Unlock()
	if connector.connection == nil {
		return nil, fmt.Errorf("in-memory snapshot connection is unavailable")
	}
	connection := connector.connection
	connector.connection = nil
	return connection, nil
}

func (*snapshotConnector) Driver() driver.Driver {
	return &sqlite.Driver{}
}

func OpenReadOnlySnapshot(ctx context.Context, path string) (*ReadOnlySnapshot, error) {
	for range 3 {
		state, err := inspectSnapshotSource(path)
		if err != nil {
			return nil, err
		}
		snapshot, immutable, err := openReadOnlySnapshot(ctx, path, state)
		if err != nil {
			return nil, err
		}
		if !immutable {
			return snapshot, nil
		}
		after, err := inspectSnapshotSource(path)
		if err == nil && state == after {
			return snapshot, nil
		}
		_ = snapshot.Close()
		if err != nil {
			return nil, err
		}
	}
	return nil, fmt.Errorf("database %q kept changing while it was snapshotted", path)
}

type snapshotSourceState struct {
	mainSize    int64
	mainModTime int64
	walExists   bool
	walSize     int64
	walModTime  int64
	shmExists   bool
}

func inspectSnapshotSource(path string) (snapshotSourceState, error) {
	main, err := os.Stat(path)
	if err != nil {
		return snapshotSourceState{}, err
	}
	state := snapshotSourceState{mainSize: main.Size(), mainModTime: main.ModTime().UnixNano()}
	wal, err := os.Stat(path + "-wal")
	if err == nil {
		state.walExists = true
		state.walSize = wal.Size()
		state.walModTime = wal.ModTime().UnixNano()
	} else if !os.IsNotExist(err) {
		return snapshotSourceState{}, fmt.Errorf("inspect the WAL for %q: %w", path, err)
	}
	if _, err := os.Stat(path + "-shm"); err == nil {
		state.shmExists = true
	} else if !os.IsNotExist(err) {
		return snapshotSourceState{}, fmt.Errorf("inspect the shared index for %q: %w", path, err)
	}
	return state, nil
}

func openReadOnlySnapshot(ctx context.Context, path string,
	state snapshotSourceState) (*ReadOnlySnapshot, bool, error) {
	values := url.Values{
		"mode": {"ro"},
		"_pragma": {
			fmt.Sprintf("busy_timeout(%d)", busyTimeout.Milliseconds()),
			"query_only(1)",
		},
	}
	immutable := !state.walExists || state.walSize <= 32
	if immutable {
		values.Set("immutable", "1")
	} else if !state.shmExists {
		return nil, false, fmt.Errorf("open the live WAL for %q without changing it: shared index is missing", path)
	}
	_, sourceURI, err := sqliteFileDSN(path, values)
	if err != nil {
		return nil, false, err
	}
	source, err := sql.Open("sqlite", sourceURI)
	if err != nil {
		return nil, false, err
	}
	source.SetMaxOpenConns(1)
	defer source.Close()
	connection, err := source.Conn(ctx)
	if err != nil {
		return nil, false, err
	}
	defer connection.Close()
	if _, err := connection.ExecContext(ctx, "BEGIN"); err != nil {
		return nil, false, err
	}
	defer connection.ExecContext(context.Background(), "ROLLBACK")
	var schemaVersion int
	if err := connection.QueryRowContext(ctx, "PRAGMA schema_version").Scan(&schemaVersion); err != nil {
		return nil, false, err
	}
	uri := fmt.Sprintf("file:roca-read-only-snapshot-%d?mode=memory&cache=shared",
		snapshotSequence.Add(1))
	var snapshotConnection driver.Conn
	if err := connection.Raw(func(driverConnection any) error {
		backuper, ok := driverConnection.(sqliteBackuper)
		if !ok {
			return fmt.Errorf("SQLite driver cannot back up a snapshot")
		}
		backup, err := backuper.NewBackup(uri)
		if err != nil {
			return err
		}
		for more := true; more; {
			more, err = backup.Step(-1)
			if err != nil {
				_ = backup.Finish()
				return err
			}
		}
		snapshotConnection, err = backup.Commit()
		return err
	}); err != nil {
		return nil, false, err
	}
	destination := sql.OpenDB(&snapshotConnector{connection: snapshotConnection})
	destination.SetMaxOpenConns(1)
	destination.SetMaxIdleConns(1)
	if _, err := destination.ExecContext(ctx, "PRAGMA query_only(1)"); err != nil {
		destination.Close()
		return nil, false, err
	}
	return &ReadOnlySnapshot{database: destination, uri: uri}, immutable, nil
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
	})
	return snapshot.err
}
