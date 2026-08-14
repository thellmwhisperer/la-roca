// Package rocaops owns the bundled operational plugin package.
package rocaops

import (
	_ "embed"

	"github.com/thellmwhisperer/la-roca/internal/distribution/bundledplugin"
)

var (
	//go:embed semantic.yaml
	semantic []byte
	//go:embed schema.sql
	schema string
)

func bundleSpec() bundledplugin.Spec {
	return bundledplugin.Spec{Name: Name, DatabaseFilename: DatabaseFilename,
		Source: BundledSource, Semantic: semantic, ApplySchema: applySchema}
}
