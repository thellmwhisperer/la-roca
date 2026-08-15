package rocaops

import (
	"context"
	"database/sql"
	"fmt"
	"os"

	"github.com/thellmwhisperer/la-roca/internal/distribution/bundledplugin"
	"github.com/thellmwhisperer/la-roca/internal/distribution/migrationledger"
	"github.com/thellmwhisperer/la-roca/internal/distribution/plugininstall"
	_ "modernc.org/sqlite"
)

const (
	Name             = "roca-ops"
	DatabaseFilename = "roca-ops.db"
	BundledSource    = "bundled:roca"
	SchemaVersion    = 1
	IndexVersion     = 1
)

func Ensure(root, binDir, version string) (plugininstall.Result, error) {
	return bundledplugin.Ensure(root, binDir, version, bundleSpec())
}

func applySchema(path string) error {
	db, err := openDatabase(path)
	if err != nil {
		return err
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return fmt.Errorf("apply bundled %s schema: %w", Name, err)
	}
	if err := migrationledger.Prepare(context.Background(), db, migrationledger.Definition{
		Plugin: Name, SchemaVersion: SchemaVersion, IndexVersion: IndexVersion,
	}); err != nil {
		db.Close()
		return fmt.Errorf("prepare bundled %s migration ledger: %w", Name, err)
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
	db, err := bundledplugin.OpenDatabase(path, false)
	if err != nil {
		return nil, fmt.Errorf("open bundled %s database: %w", Name, err)
	}
	return db, nil
}
