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
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve the bundled plugin database path: %w", err)
	}
	query := url.Values{
		"_pragma": {fmt.Sprintf("busy_timeout(%d)", busyTimeout.Milliseconds())},
	}
	if readOnly {
		query.Set("mode", "ro")
	}
	dsn := url.URL{Scheme: "file", Path: filepath.ToSlash(absolute), RawQuery: query.Encode()}
	db, err := sql.Open("sqlite", dsn.String())
	if err != nil {
		return nil, fmt.Errorf("open the bundled plugin database: %w", err)
	}
	return db, nil
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
