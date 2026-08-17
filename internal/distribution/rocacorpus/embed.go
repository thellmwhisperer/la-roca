// Package rocacorpus owns the bundled perennial-harvest plugin package.
package rocacorpus

import (
	_ "embed"

	"github.com/thellmwhisperer/la-roca/internal/distribution/bundledplugin"
	"github.com/thellmwhisperer/la-roca/internal/provider/plugin"
)

var (
	//go:embed plugin.json
	manifest []byte
	//go:embed schema.sql
	schema string
)

func BundleSpec() bundledplugin.Spec {
	return bundledplugin.Spec{Name: Name, DatabaseFilename: DatabaseFilename,
		Source: BundledSource, Manifest: manifest, ApplySchema: ApplySchema}
}

func Manifest(version string) (plugin.Manifest, error) {
	return bundledplugin.Manifest(manifest, version)
}
