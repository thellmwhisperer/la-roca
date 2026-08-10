// Package data carries inside the binary everything La Roca needs to start with
// no network: the SQL schema, the search index and the layer registry. v1 is
// model-only, so there is no classifier, no training corpus and no language
// pack: the question goes to the model, which answers over the schema below.
package data

import _ "embed"

// Schema is the DDL of the eight v1 tables.
//
//go:embed schema.sql
var Schema string

// SearchSchema is the DDL of the search artefact: the FTS5 lexical index with
// its triggers and its index-state marker. It goes apart from the identity
// schema on purpose, and the why is in that file's own header.
//
//go:embed search.sql
var SearchSchema string

// Layers is the layer registry, declared as data and not as code.
//
//go:embed layers.yaml
var Layers []byte
