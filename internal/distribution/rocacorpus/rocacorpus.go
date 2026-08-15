package rocacorpus

import (
	"github.com/thellmwhisperer/la-roca/internal/distribution/bundledplugin"
	"github.com/thellmwhisperer/la-roca/internal/distribution/plugininstall"
	"github.com/thellmwhisperer/la-roca/internal/provider/plugin"
)

const (
	Name             = "roca-corpus"
	DatabaseFilename = "roca-corpus.db"
	// BundledSource is what the installer records for this package, and it is
	// what discovery reads to know the corpus attach alias is the kernel's own.
	BundledSource = plugin.BundledSource
	SchemaVersion = 1
	IndexVersion  = 1
)

func Ensure(root, binDir, version string) (plugininstall.Result, error) {
	return bundledplugin.Ensure(root, binDir, version, bundleSpec())
}

// ApplySchema replays the whole declaration because every statement in it is
// guarded, so a version update over a database that already carries the harvest
// leaves its rows untouched.
func ApplySchema(path string) error {
	return bundledplugin.ApplySchema(path, Name, schema, SchemaVersion, IndexVersion)
}
