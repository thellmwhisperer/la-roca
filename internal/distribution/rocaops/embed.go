// Package rocaops owns the bundled operational plugin package.
package rocaops

import _ "embed"

var (
	//go:embed semantic.yaml
	semantic []byte
	//go:embed schema.sql
	schema string
)
