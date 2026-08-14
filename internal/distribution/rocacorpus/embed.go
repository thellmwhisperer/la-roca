// Package rocacorpus owns the bundled perennial-harvest plugin package.
package rocacorpus

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
