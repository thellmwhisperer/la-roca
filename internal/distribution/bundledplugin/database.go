package bundledplugin

import (
	"database/sql"
	"fmt"
	"net/url"
	"path/filepath"
	"time"

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
