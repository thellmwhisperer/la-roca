//go:build !windows

package vector

import (
	"context"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"syscall"

	"github.com/thellmwhisperer/la-roca-vector/internal/engine"
	"github.com/thellmwhisperer/la-roca-vector/internal/llamacpp"
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
	// Writer is the backend policy for an indexing run: which occasion this
	// pass is, plus whatever lever the operator pulled. Readers ignore it.
	Writer        llamacpp.Policy
	ownershipOnce sync.Once
	ownership     chan struct{}
	engine        nativeEngine
	backend       string
	fallback      string
	trapped       atomic.Bool
}

var (
	errNativeTrapped       = fmt.Errorf("semantic search stalled while preparing embeddings")
	restartAfterNativeTrap = restartTrappedWorker
)

func restartTrappedWorker() {
	executable, err := os.Executable()
	if err != nil {
		return
	}
	_ = syscall.Exec(executable, os.Args, os.Environ())
}

type nativeEngine interface {
	Embed(string) ([]float32, int, error)
	Close()
}

func (n *Native) acquireNative(ctx context.Context) error {
	if n.trapped.Load() {
		return errNativeTrapped
	}
	n.ownershipOnce.Do(func() {
		n.ownership = make(chan struct{}, 1)
	})
	select {
	case n.ownership <- struct{}{}:
		if n.trapped.Load() {
			<-n.ownership
			return errNativeTrapped
		}
		if err := ctx.Err(); err != nil {
			<-n.ownership
			return err
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (n *Native) markNativeTrapped() {
	if n.trapped.CompareAndSwap(false, true) {
		restartAfterNativeTrap()
	}
}

func (n *Native) releaseNative() {
	<-n.ownership
}

func (n *Native) nativeContextError(callerCtx context.Context) error {
	if err := callerCtx.Err(); err != nil {
		return err
	}
	n.record(telemetry.Record{Kind: telemetry.KindError, Err: "semantic search stalled"})
	return fmt.Errorf("semantic search stalled while preparing embeddings")
}

func ConfiguredEmbedder(dataDir, stateDir string, events engine.Sink, tel *telemetry.Store,
	readOnly bool, writer llamacpp.Policy) Embedder {
	return &Native{DataDir: dataDir, StateDir: stateDir, Events: events, Telemetry: tel,
		ReadOnly: readOnly, Writer: writer}
}

// writerFallbackReason keeps the engine's own answer when it has one: an
// accelerator that refused to start is a better explanation of a CPU run than
// the policy that asked for it.
func writerFallbackReason(policy, existing string) string {
	if existing != "" {
		return existing
	}
	return policy
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
