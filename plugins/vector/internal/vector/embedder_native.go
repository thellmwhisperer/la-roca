//go:build !windows

package vector

import (
	"context"
	"fmt"
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
	ReadOnly  bool
	mu        sync.Mutex
	engine    nativeEngine
	backend   string
	fallback  string
}

type nativeEngine interface {
	Embed(string) ([]float32, int, error)
	Close()
}

func ConfiguredEmbedder(dataDir, stateDir string, events engine.Sink, tel *telemetry.Store, readOnly bool) Embedder {
	return &Native{DataDir: dataDir, StateDir: stateDir, Events: events, Telemetry: tel, ReadOnly: readOnly}
}

func (n *Native) Pull(ctx context.Context, requestedModel string) error {
	return n.pull(ctx, requestedModel)
}

func (n *Native) pull(ctx context.Context, requestedModel string) error {
	if requestedModel != DefaultModel {
		return fmt.Errorf("embedding model %q is not supported by this engine", requestedModel)
	}
	_, err := n.modelPath(ctx)
	return err
}

func (n *Native) modelPath(ctx context.Context) (string, error) {
	if n.ReadOnly {
		return model.Existing(n.DataDir, model.DefaultManifest())
	}
	return model.Ensure(ctx, n.DataDir, model.DefaultManifest(), n.Events)
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
	return processHighWater()
}
