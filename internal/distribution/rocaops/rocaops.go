package rocaops

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/thellmwhisperer/la-roca/internal/distribution/bundledplugin"
	"github.com/thellmwhisperer/la-roca/internal/distribution/plugininstall"
	_ "modernc.org/sqlite"
)

const (
	Name             = "roca-ops"
	DatabaseFilename = "roca-ops.db"
	BundledSource    = "bundled:roca"

	busyTimeout = 15 * time.Second
)

func Ensure(root, binDir, version string) (plugininstall.Result, error) {
	return bundledplugin.Ensure(root, binDir, version, bundleSpec())
}

func applySchema(path string) error {
	db, err := openDatabase(path)
	if err != nil {
		return err
	}
	applied, err := schemaApplied(db)
	if err != nil {
		db.Close()
		return err
	}
	if !applied {
		if _, err := db.Exec(schema); err != nil {
			db.Close()
			return fmt.Errorf("apply bundled %s schema: %w", Name, err)
		}
	}
	if err := db.Close(); err != nil {
		return fmt.Errorf("close bundled %s database: %w", Name, err)
	}
	return os.Chmod(path, 0o600)
}

// openDatabase gives install and version-update work the same lock discipline
// as every other connection to this file. A running service can still own a
// short write lock while the bundled payload is being refreshed.
func openDatabase(path string) (*sql.DB, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve the bundled %s database path: %w", Name, err)
	}
	dsn := url.URL{Scheme: "file", Path: filepath.ToSlash(absolute), RawQuery: url.Values{
		"_pragma": {fmt.Sprintf("busy_timeout(%d)", busyTimeout.Milliseconds())},
	}.Encode()}
	db, err := sql.Open("sqlite", dsn.String())
	if err != nil {
		return nil, fmt.Errorf("open bundled %s database: %w", Name, err)
	}
	return db, nil
}

// schemaApplied answers with a read, so the common case never asks for the write
// lock. It looks for the identifier seed because schema.sql writes it last: what
// carries it carries everything the schema declares before it, and a database
// that lost the seed would hand out identifiers that collide with core's.
func schemaApplied(db *sql.DB) (bool, error) {
	var counters int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master
		WHERE type = 'table' AND name = 'sqlite_sequence'`).Scan(&counters); err != nil {
		return false, fmt.Errorf("read the bundled %s schema: %w", Name, err)
	}
	if counters == 0 {
		return false, nil
	}
	var seeded int
	if err := db.QueryRow(
		"SELECT COUNT(*) FROM sqlite_sequence WHERE name = 'memories'").Scan(&seeded); err != nil {
		return false, fmt.Errorf("read the bundled %s identifier seed: %w", Name, err)
	}
	return seeded == 1, nil
}
