// Package rocacorpus owns the bundled perennial-harvest plugin package.
package rocacorpus

import (
	"bytes"
	_ "embed"
	"strings"

	"github.com/thellmwhisperer/la-roca/internal/distribution/bundledplugin"
	"github.com/thellmwhisperer/la-roca/internal/provider/plugin"
)

var (
	//go:embed plugin.json
	manifest []byte
	//go:embed schema.sql
	schema string
)

func bundleSpec() bundledplugin.Spec {
	return bundledplugin.Spec{Name: Name, DatabaseFilename: DatabaseFilename,
		Source: BundledSource, Manifest: manifest, ApplySchema: applySchema}
}

func Manifest(version string) (plugin.Manifest, error) {
	declaration, err := plugin.DecodeManifest(bytes.NewReader(manifest))
	if err != nil {
		return plugin.Manifest{}, err
	}
	if strings.TrimSpace(version) != "" {
		declaration.Version = version
	}
	if err := declaration.Valid(); err != nil {
		return plugin.Manifest{}, err
	}
	return declaration, nil
}
