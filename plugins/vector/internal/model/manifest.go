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
	ReleaseTag = "models-v1"

	LicenseFileName = "LICENSE-model.txt"
	LicenseSHA256   = "cfc7749b96f63bd31c3c42b5c471bf756814053e847c10f3eb003417bc523d30"
	LicenseBytes    = 11358
	LicenseURL      = "https://www.apache.org/licenses/LICENSE-2.0.txt"

	DocumentPrefix = "search_document: "
	QueryPrefix    = "search_query: "
)

// SourceURL is where the release lane obtains the pinned upstream bytes.
// DownloadURL is the same verified file after La Roca publishes it through its
// existing GitHub release channel. Tests override DownloadURL.
const SourceURL = "https://huggingface.co/nomic-ai/nomic-embed-text-v2-moe-GGUF/resolve/" +
	Revision + "/" + FileName

var DownloadURL = "https://github.com/thellmwhisperer/la-roca/releases/download/" +
	ReleaseTag + "/" + FileName
