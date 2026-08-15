// Package rocaops owns the bundled operational plugin package.
package rocaops

import (
	_ "embed"

	"github.com/thellmwhisperer/la-roca/internal/distribution/bundledplugin"
	"github.com/thellmwhisperer/la-roca/internal/provider/plugin"
)

//go:embed plugin.json
var manifest []byte

//go:embed schema.sql
var schema string

func Manifest(version string) (plugin.Manifest, error) {
	return bundledplugin.Manifest(manifest, version)
}
