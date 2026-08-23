//go:build !windows

package vector

import (
	"context"
	"sync"

	"github.com/thellmwhisperer/la-roca-vector/internal/engine"
	"github.com/thellmwhisperer/la-roca-vector/internal/model"
	"github.com/thellmwhisperer/la-roca-vector/internal/telemetry"
)

// Native is the unix embeddings engine: one downloaded model file, no daemon.
type Native struct {
	DataDir   string
	StateDir  string
	Events    engine.Sink
	Telemetry *telemetry.Store
	once      sync.Once
	engine    nativeEngine
	backend   string
	fallback  string
	err       error
}

type nativeEngine interface {
	Embed(string) ([]float32, int, error)
	Close()
}

func ConfiguredEmbedder(dataDir, stateDir string, events engine.Sink, tel *telemetry.Store) Embedder {
	return &Native{DataDir: dataDir, StateDir: stateDir, Events: events, Telemetry: tel}
}

func (n *Native) Pull(ctx context.Context, _ string) error {
	_, err := model.Ensure(ctx, n.DataDir, model.DefaultManifest(), n.Events)
	return err
}

func (n *Native) record(record telemetry.Record) {
	if n.Telemetry == nil {
		return
	}
	_ = n.Telemetry.Record(context.Background(), record)
}

func (n *Native) emit(event engine.Event) {
	if n.Events != nil {
		n.Events(event)
	}
}

func memoryHighWater() int64 {
	var info runtimeMem
	readMem(&info)
	return info
}

type runtimeMem = int64
