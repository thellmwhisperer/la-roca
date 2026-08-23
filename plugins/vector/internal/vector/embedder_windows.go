//go:build windows

package vector

import (
	"os"

	"github.com/thellmwhisperer/la-roca-vector/internal/engine"
	"github.com/thellmwhisperer/la-roca-vector/internal/telemetry"
)

func ConfiguredEmbedder(dataDir, stateDir string, events engine.Sink, tel *telemetry.Store, readOnly bool) Embedder {
	_ = dataDir
	_ = stateDir
	_ = events
	_ = tel
	_ = readOnly
	return Ollama{BaseURL: os.Getenv("OLLAMA_HOST")}
}
