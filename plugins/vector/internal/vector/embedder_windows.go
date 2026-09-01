//go:build windows

package vector

import (
	"context"
	"os"

	"github.com/thellmwhisperer/la-roca-vector/internal/engine"
	"github.com/thellmwhisperer/la-roca-vector/internal/llamacpp"
	"github.com/thellmwhisperer/la-roca-vector/internal/telemetry"
)

// Windows embeds through Ollama, which owns its own backend choice, so the
// writer policy has nowhere to land here.
type WorkerTrapRecovery struct{}

func NewWorkerTrapRecovery(context.CancelFunc, func()) *WorkerTrapRecovery {
	return &WorkerTrapRecovery{}
}

func (*WorkerTrapRecovery) Handle(string) error { return nil }

func (*WorkerTrapRecovery) RestartIfRequested() (bool, error) { return false, nil }

func EnableWorkerRestartOnNativeTrap(Embedder, func(string) error) {}

func ConfiguredEmbedder(dataDir, stateDir string, events engine.Sink, tel *telemetry.Store,
	readOnly bool, writer llamacpp.Policy) Embedder {
	_ = dataDir
	_ = stateDir
	_ = events
	_ = tel
	_ = readOnly
	_ = writer
	return Ollama{BaseURL: os.Getenv("OLLAMA_HOST")}
}
