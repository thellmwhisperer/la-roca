package bundledplugin

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/thellmwhisperer/la-roca/internal/distribution/migrationledger"
	_ "modernc.org/sqlite"
)

const busyTimeout = 15 * time.Second

// OpenDatabase gives bundled data plugins one SQLite DSN contract. A read-only
// observer opens a closed WAL database as immutable so observation creates no
// sidecars; a live WAL keeps ordinary SQLite locking so its frames stay visible.
func OpenDatabase(path string, readOnly bool, timeout ...time.Duration) (*sql.DB, error) {
	mode := ""
	immutable := false
	if readOnly {
		mode = "ro"
		var err error
		immutable, err = closedWALDatabase(path)
		if err != nil {
			return nil, err
		}
	}
	configuredTimeout := busyTimeout
	if len(timeout) > 0 && timeout[0] > 0 {
		configuredTimeout = timeout[0]
	}
	return openDatabase(path, mode, configuredTimeout, immutable)
}

func closedWALDatabase(path string) (bool, error) {
	if _, err := os.Lstat(path + "-wal"); err == nil {
		return false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("inspect the bundled plugin database WAL: %w", err)
	}
	file, err := os.Open(path)
	if err != nil {
		return false, fmt.Errorf("inspect the bundled plugin database header: %w", err)
	}
	defer file.Close()
	header := make([]byte, 20)
	if _, err := io.ReadFull(file, header); err != nil {
		return false, nil
	}
	return bytes.Equal(header[:16], []byte("SQLite format 3\x00")) &&
		header[18] == 2 && header[19] == 2, nil
}

func openDatabase(path, mode string, configuredTimeout time.Duration, immutable bool) (*sql.DB, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve the bundled plugin database path: %w", err)
	}
	query := url.Values{
		"_pragma": {fmt.Sprintf("busy_timeout(%d)", configuredTimeout.Milliseconds())},
	}
	if mode != "" {
		query.Set("mode", mode)
	}
	if immutable {
		query.Set("immutable", "1")
	}
	dsn := url.URL{Scheme: "file", Path: filepath.ToSlash(absolute), RawQuery: query.Encode()}
	db, err := sql.Open("sqlite", dsn.String())
	if err != nil {
		return nil, fmt.Errorf("open the bundled plugin database: %w", err)
	}
	return db, nil
}

// TableColumns returns the declared column names of one table as a set. The
// table name comes from a bundled schema constant, never operator input, and
// the table-valued pragma form takes a bound parameter, so the name is never
// interpolated into SQL. Both a connection and a transaction satisfy the
// querier shape, which keeps schema-shape probes in one place.
func TableColumns(ctx context.Context, querier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, table string) (map[string]bool, error) {
	rows, err := querier.QueryContext(ctx, `SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns := make(map[string]bool)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		columns[name] = true
	}
	return columns, rows.Err()
}

// ApplySchema upgrades one bundled plugin's owned database in place, then
// prepares its plugin-local DATA SPLIT ledger. Every declaration it executes is
// additive and idempotent; source rows remain the caller's custody throughout.
func ApplySchema(path, pluginName, declaration string, schemaVersion, indexVersion int) error {
	db, err := OpenDatabase(path, false)
	if err != nil {
		return fmt.Errorf("open bundled %s database: %w", pluginName, err)
	}
	if _, err := db.Exec(declaration); err != nil {
		db.Close()
		return fmt.Errorf("apply bundled %s schema: %w", pluginName, err)
	}
	if err := migrationledger.Prepare(context.Background(), db, migrationledger.Definition{
		Plugin: pluginName, SchemaVersion: schemaVersion, IndexVersion: indexVersion,
	}); err != nil {
		db.Close()
		return fmt.Errorf("prepare bundled %s migration ledger: %w", pluginName, err)
	}
	if err := db.Close(); err != nil {
		return fmt.Errorf("close bundled %s database: %w", pluginName, err)
	}
	return os.Chmod(path, 0o600)
}
