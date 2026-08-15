package rocaops

import (
	"github.com/thellmwhisperer/la-roca/internal/distribution/bundledplugin"
	"github.com/thellmwhisperer/la-roca/internal/distribution/plugininstall"
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

func ApplySchema(path string) error {
	return bundledplugin.ApplySchema(path, Name, schema, SchemaVersion, IndexVersion)
}
