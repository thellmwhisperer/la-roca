// Package model owns the one embeddings file La Roca downloads: a pinned
// content hash under the selected data directory. The user downloads exactly
// this file, once.
package model

const (
	ID         = "nomic-embed-text-v2-moe"
	FileName   = "nomic-embed-text-v2-moe.f16.gguf"
	SHA256     = "a5db3381f2e514d3490a3a31fe70eb1a65e95016c85c6c2c23223b810806594f"
	Bytes      = 957680480
	Dimensions = 768
	Context    = 512
	Runtime    = "llama.cpp@b21e4de74567f5eef213765c9476a843c2e43f0d"
	Revision   = "main"

	DocumentPrefix = "search_document: "
	QueryPrefix    = "search_query: "
)

// DownloadURL is the pinned source of the embeddings file. Tests override it.
var DownloadURL = "https://huggingface.co/nomic-ai/nomic-embed-text-v2-moe-GGUF/resolve/" +
	Revision + "/" + FileName
