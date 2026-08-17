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
	SchemaVersion    = 3
	IndexVersion     = 2
)

func Ensure(root, binDir, version string) (plugininstall.Result, error) {
	return bundledplugin.Ensure(root, binDir, version, BundleSpec())
}

func ApplySchema(path string) error {
	return bundledplugin.ApplySchema(path, Name, schema, SchemaVersion, IndexVersion)
}

func BundleSpec() bundledplugin.Spec {
	return bundledplugin.Spec{Name: Name, DatabaseFilename: DatabaseFilename,
		Source: BundledSource, Manifest: manifest, ApplySchema: ApplySchema}
}
