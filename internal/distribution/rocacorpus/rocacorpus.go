package rocacorpus

import (
	"fmt"
	"os"

	"github.com/thellmwhisperer/la-roca/internal/distribution/bundledplugin"
	"github.com/thellmwhisperer/la-roca/internal/distribution/plugininstall"
	"github.com/thellmwhisperer/la-roca/internal/provider/plugin"
	_ "modernc.org/sqlite"
)

const (
	Name             = "roca-corpus"
	DatabaseFilename = "roca-corpus.db"
	// BundledSource is what the installer records for this package, and it is
	// what discovery reads to know the corpus attach alias is the kernel's own.
	BundledSource = plugin.BundledSource
)

func Ensure(root, binDir, version string) (plugininstall.Result, error) {
	return bundledplugin.Ensure(root, binDir, version, bundleSpec())
}

// applySchema replays the whole declaration because every statement in it is
// guarded, so a version update over a database that already carries the harvest
// leaves its rows untouched.
func applySchema(path string) error {
	db, err := bundledplugin.OpenDatabase(path, false)
	if err != nil {
		return fmt.Errorf("open bundled %s database: %w", Name, err)
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return fmt.Errorf("apply bundled %s schema: %w", Name, err)
	}
	if err := db.Close(); err != nil {
		return fmt.Errorf("close bundled %s database: %w", Name, err)
	}
	return os.Chmod(path, 0o600)
}
