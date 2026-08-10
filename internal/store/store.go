// Package store is La Roca's store: opening the database, the schema, adoption
// of existing databases, transactions and backup. It is the bottom layer: it
// knows neither the query cascade nor the surfaces.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/thellmwhisperer/la-roca/data"
	sqlite "modernc.org/sqlite"
)

const (
	// busyTimeout bounds lock contention on every connection.
	busyTimeout = 15 * time.Second
	// writeRetries caps the busy wait when acquiring the write lock arrives
	// contended.
	writeRetries = 5
)

// DB is an open La Roca database.
type DB struct {
	sql  *sql.DB
	path string

	once        sync.Once
	readOnly    *sql.DB
	readOnlyErr error
}

// Open opens the database at path, creating the file when it does not exist.
//
// The three concurrency pieces go together and are mandatory: WAL so a reader
// does not block a writer, busy_timeout so a writer waits instead of failing,
// and _txlock=immediate so the transaction takes the write lock as it opens.
// Without the third, a transaction that reads before writing fails to promote
// with SQLITE_BUSY_SNAPSHOT, which the busy handler never retries.
func Open(path string) (*DB, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve the database path %q: %w", path, err)
	}
	dsn := "file:" + abs + "?" + url.Values{
		"_txlock": {"immediate"},
		"_pragma": {
			fmt.Sprintf("busy_timeout(%d)", busyTimeout.Milliseconds()),
			"journal_mode(WAL)",
			"foreign_keys(ON)",
		},
	}.Encode()

	handle, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open the database %q: %w", abs, err)
	}
	if err := handle.Ping(); err != nil {
		handle.Close()
		return nil, fmt.Errorf("open the database %q: %w", abs, whyItCannotOpen(abs, err))
	}
	return &DB{sql: handle, path: abs}, nil
}

// whyItCannotOpen turns the engine's answer into the operator's.
//
// SQLite reports a missing directory and a directory this user cannot write in
// as the same SQLITE_CANTOPEN, and the driver renders that extended code as
// **"out of memory"** — a true sentence about an entirely different machine.
// Somebody who pointed `--db-path` at a directory they do not own goes and looks
// at their RAM instead of fixing the path.
//
// So the two things the engine cannot see are checked here, in the order an
// operator would check them, and only on the path where the open already failed.
func whyItCannotOpen(abs string, err error) error {
	directory := filepath.Dir(abs)
	if _, statErr := os.Stat(directory); os.IsNotExist(statErr) {
		return fmt.Errorf(
			"the directory %s does not exist: create it, or name another database "+
				"with --db-path", directory)
	}
	// Asked and not deduced: the mode bits do not answer it on their own once
	// ACLs, a read-only mount or an immutable flag are in play.
	probe, probeErr := os.CreateTemp(directory, ".roca-open-*")
	if probeErr != nil {
		return fmt.Errorf(
			"nothing can be written in %s: %w. Fix its permissions, or name "+
				"another database with --db-path", directory, probeErr)
	}
	probe.Close()
	os.Remove(probe.Name())
	return err
}

// SQL returns the handle for reads. Writes go through Write.
func (db *DB) SQL() *sql.DB { return db.sql }

// ReadOnly returns a handle over the same database on which the engine itself
// rejects any write. It is the gate's last line: what passes through it runs
// here, where `query_only` is set and a statement that writes fails even if the
// validator had been wrong.
//
// The file is opened read-write at the system level on purpose: a truly
// read-only connection cannot touch WAL's shared index, and a WAL database with
// a reader like that fails to read.
func (db *DB) ReadOnly() (*sql.DB, error) {
	db.once.Do(func() {
		dsn := "file:" + db.path + "?" + url.Values{
			"_pragma": {
				fmt.Sprintf("busy_timeout(%d)", busyTimeout.Milliseconds()),
				"journal_mode(WAL)",
				"query_only(1)",
			},
		}.Encode()
		handle, err := sql.Open("sqlite", dsn)
		if err != nil {
			db.readOnlyErr = fmt.Errorf("open the database read-only: %w", err)
			return
		}
		if err := handle.Ping(); err != nil {
			handle.Close()
			db.readOnlyErr = fmt.Errorf("open the database read-only: %w", err)
			return
		}
		db.readOnly = handle
	})
	return db.readOnly, db.readOnlyErr
}

// Path is the absolute path of the database file.
func (db *DB) Path() string { return db.path }

// Close closes the database.
func (db *DB) Close() error {
	if db.readOnly != nil {
		db.readOnly.Close()
	}
	return db.sql.Close()
}

// Write runs fn inside an immediate transaction, retrying with jitter while the
// write lock is busy. If fn returns an error, the whole transaction is rolled
// back.
func (db *DB) Write(ctx context.Context, fn func(*sql.Tx) error) error {
	var last error
	for attempt := range writeRetries {
		tx, err := db.sql.BeginTx(ctx, nil)
		if err != nil {
			if !isDatabaseBusy(err) {
				return fmt.Errorf("open a write transaction: %w", err)
			}
			last = err
			if err := waitWithJitter(ctx, attempt); err != nil {
				return err
			}
			continue
		}
		if err := fn(tx); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			if !isDatabaseBusy(err) {
				return fmt.Errorf("commit a write transaction: %w", err)
			}
			last = err
			if err := waitWithJitter(ctx, attempt); err != nil {
				return err
			}
			continue
		}
		return nil
	}
	return fmt.Errorf("the database is still busy after %d write attempts: %w",
		writeRetries, last)
}

// ApplySchema creates whichever v1 tables and indexes are missing. It is
// idempotent and touches nothing that already exists: the whole DDL is written
// with IF NOT EXISTS.
func ApplySchema(ctx context.Context, db *DB) error {
	return applySchema(ctx, db, data.Schema, "v1")
}

func applySchema(ctx context.Context, db *DB, schema, label string) error {
	return db.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, schema); err != nil {
			return fmt.Errorf("apply the %s schema: %w", label, err)
		}
		return nil
	})
}

func waitWithJitter(ctx context.Context, attempt int) error {
	base := time.Duration(1<<attempt) * 20 * time.Millisecond
	wait := base + time.Duration(rand.Int64N(int64(base)))
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(wait):
		return nil
	}
}

// isDatabaseBusy tells contention, which is retried, from a real error, which is
// reported with its cause.
func isDatabaseBusy(err error) bool {
	var serr *sqlite.Error
	if !errors.As(err, &serr) {
		return false
	}
	primary := serr.Code() & 0xff
	return primary == 5 || primary == 6 // SQLITE_BUSY, SQLITE_LOCKED
}
