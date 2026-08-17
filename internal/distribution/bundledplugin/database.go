package bundledplugin

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/thellmwhisperer/la-roca/internal/distribution/migrationledger"
	_ "modernc.org/sqlite"
)

const busyTimeout = 15 * time.Second

// OpenDatabase gives bundled data plugins one SQLite DSN contract while
// allowing observers to request a read-only connection.
func OpenDatabase(path string, readOnly bool) (*sql.DB, error) {
	mode := ""
	if readOnly {
		mode = "ro"
	}
	return openDatabase(path, mode)
}

func openDatabase(path, mode string) (*sql.DB, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve the bundled plugin database path: %w", err)
	}
	query := url.Values{
		"_pragma": {fmt.Sprintf("busy_timeout(%d)", busyTimeout.Milliseconds())},
	}
	if mode != "" {
		query.Set("mode", mode)
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
