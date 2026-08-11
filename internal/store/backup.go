package store

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Backup writes a dated copy of the database into dir and verifies it before
// returning it. It is the precondition of any in-place repair: if the copy
// cannot be verified, there is no repair.
func Backup(ctx context.Context, db *DB, dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create the backup directory %q: %w", dir, err)
	}
	name := fmt.Sprintf("%s.%s.backup",
		strings.TrimSuffix(filepath.Base(db.path), ".db"),
		time.Now().UTC().Format("20060102T150405Z"))
	dest := filepath.Join(dir, name+".db")

	if _, err := os.Stat(dest); err == nil {
		return "", fmt.Errorf("a backup already exists at %q", dest)
	}
	if _, err := db.sql.ExecContext(ctx, "VACUUM INTO ?", dest); err != nil {
		return "", fmt.Errorf("copy the database to %q: %w", dest, err)
	}
	if err := verifyBackup(ctx, db.sql, dest); err != nil {
		return "", err
	}
	return dest, nil
}

// CopyDatabase copies the source database to destPath using VACUUM INTO, which
// produces a self-contained database from a live WAL source consistently — the
// WAL is checkpointed into the copy, and the source is not modified. It is the
// adoption-by-copy tool: init copies an existing database into La Roca's home,
// and from that point the copy is the one operated on.
func CopyDatabase(ctx context.Context, srcPath, destPath string) error {
	_, dsn, err := sqliteFileDSN(srcPath, url.Values{"mode": {"ro"}})
	if err != nil {
		return fmt.Errorf("resolve the source database at %q: %w", srcPath, err)
	}
	src, err := sql.Open("sqlite", dsn)
	if err != nil {
		return fmt.Errorf("open the source database at %q: %w", srcPath, err)
	}
	defer src.Close()
	if err := src.PingContext(ctx); err != nil {
		return fmt.Errorf("open the source database at %q: %w", srcPath, err)
	}

	if _, err := os.Stat(destPath); err == nil {
		return fmt.Errorf("%s already exists: refusing to overwrite it", destPath)
	}

	if err := os.MkdirAll(filepath.Dir(destPath), 0o700); err != nil {
		return fmt.Errorf("create the destination directory: %w", err)
	}

	if _, err := src.ExecContext(ctx, "VACUUM INTO ?", destPath); err != nil {
		return fmt.Errorf("copy the database from %q to %q: %w", srcPath, destPath, err)
	}

	if err := verifyBackup(ctx, src, destPath); err != nil {
		return fmt.Errorf("verify the copy at %q: %w", destPath, err)
	}
	return nil
}

// verifyBackup checks that the copy opens, that the engine considers it whole,
// and that the identity tables carry the same rows as the original.
func verifyBackup(ctx context.Context, src *sql.DB, dest string) error {
	copyDB, err := Open(dest)
	if err != nil {
		return fmt.Errorf("verify the backup %q: %w", dest, err)
	}
	defer copyDB.Close()
	var sessionsFTS int
	if err := copyDB.sql.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'sessions_fts'").Scan(&sessionsFTS); err != nil {
		return fmt.Errorf("verify the backup %q: %w", dest, err)
	}
	if sessionsFTS == 1 {
		if _, err := copyDB.sql.ExecContext(ctx,
			"INSERT INTO sessions_fts(sessions_fts) VALUES ('rebuild')"); err != nil {
			return fmt.Errorf("rebuild the session index in %q: %w", dest, err)
		}
	}

	var integrity string
	if err := copyDB.sql.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&integrity); err != nil {
		return fmt.Errorf("verify the backup %q: %w", dest, err)
	}
	if integrity != "ok" {
		return fmt.Errorf("the backup %q is not whole: %s", dest, integrity)
	}

	for _, table := range identityTables {
		expected, err := countRows(ctx, src, table)
		if err != nil {
			return fmt.Errorf("verify the backup %q: %w", dest, err)
		}
		copied, err := countRows(ctx, copyDB.sql, table)
		if err != nil {
			return fmt.Errorf("verify the backup %q: %w", dest, err)
		}
		if expected != copied {
			return fmt.Errorf("the backup %q has %d rows in %s and the original has %d",
				dest, copied, table, expected)
		}
	}
	return nil
}

func countRows(ctx context.Context, db *sql.DB, table string) (int64, error) {
	var n int64
	err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&n)
	return n, err
}
