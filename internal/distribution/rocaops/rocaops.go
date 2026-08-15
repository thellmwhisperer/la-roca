package rocaops

import (
	"github.com/thellmwhisperer/la-roca/internal/distribution/bundledplugin"
	"github.com/thellmwhisperer/la-roca/internal/distribution/plugininstall"
	"github.com/thellmwhisperer/la-roca/internal/provider/plugin"
)

const (
	Name             = "roca-ops"
	DatabaseFilename = "roca-ops.db"
	BundledSource    = plugin.BundledSource
	SchemaVersion    = 2
	IndexVersion     = 2
)

func Ensure(root, binDir, version string) (plugininstall.Result, error) {
	spec := bundledplugin.Spec{Name: Name, DatabaseFilename: DatabaseFilename,
		Source: BundledSource, Manifest: manifest, ApplySchema: ApplySchema}
	return bundledplugin.Ensure(root, binDir, version, spec)
}

func ApplySchema(path string) error {
	return bundledplugin.ApplySchema(path, Name, schema, SchemaVersion, IndexVersion)
}
