//go:build cgo && !windows

package vector

import (
	"context"
	"fmt"
	"runtime"
	"time"

	"github.com/thellmwhisperer/la-roca-vector/internal/engine"
	"github.com/thellmwhisperer/la-roca-vector/internal/llamacpp"
	"github.com/thellmwhisperer/la-roca-vector/internal/telemetry"
)

var (
	nativeCallTimeout   = 10 * time.Minute
	nativeOpenPreferred = llamacpp.OpenPreferred
)

func (n *Native) Embed(ctx context.Context, requestedModel string, input []string) ([][]float32, error) {
	if requestedModel != DefaultModel {
		return nil, fmt.Errorf("embedding model %q is not supported by this engine", requestedModel)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	ctx, cancel := boundContext(ctx, nativeCallTimeout)
	defer cancel()
	type reply struct {
		vectors [][]float32
		err     error
	}
	if err := n.acquireNative(ctx); err != nil {
		n.record(telemetry.Record{Kind: telemetry.KindError, Err: "semantic search stalled"})
		return nil, fmt.Errorf("semantic search stalled while preparing embeddings")
	}
	done := make(chan reply, 1)
	go func() {
		defer n.releaseNative()
		vectors, err := n.embedLocked(ctx, input)
		done <- reply{vectors: vectors, err: err}
	}()
	select {
	case result := <-done:
		return result.vectors, result.err
	case <-ctx.Done():
		n.record(telemetry.Record{Kind: telemetry.KindError, Err: "semantic search stalled"})
		return nil, fmt.Errorf("semantic search stalled while preparing embeddings")
	}
}

func (n *Native) embedLocked(ctx context.Context, input []string) ([][]float32, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if n.engine == nil {
		if err := n.open(ctx); err != nil {
			return nil, err
		}
	}
	started := time.Now()
	result := make([][]float32, len(input))
	for i, text := range input {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		vector, _, err := n.engine.Embed(text)
		if err != nil {
			n.record(telemetry.Record{Kind: telemetry.KindError, Backend: n.backend, Fallback: n.fallback, Err: "embed failed"})
			return nil, fmt.Errorf("embed: %w", err)
		}
		result[i] = vector
	}
	elapsed := time.Since(started)
	kind := telemetry.KindEmbed
	if len(input) > 1 {
		kind = telemetry.KindBatch
	}
	throughput := 0.0
	if elapsed > 0 {
		throughput = float64(len(input)) / elapsed.Seconds()
	}
	n.record(telemetry.Record{
		Kind: kind, Operation: telemetry.Operation(ctx), Backend: n.backend, Fallback: n.fallback,
		DurationMS: elapsed.Milliseconds(), BatchSize: len(input),
		Throughput: throughput, MemoryHWM: memoryHighWater(),
	})
	return result, nil
}

func (n *Native) open(ctx context.Context) error {
	path, err := n.modelPath(ctx)
	if err != nil {
		n.record(telemetry.Record{Kind: telemetry.KindError, Err: "the embedding model is not downloaded"})
		return err
	}
	n.emit(engine.Progress("prewarm", "semantic search: preparing", 0, 1, 0))
	started := time.Now()
	policy := llamacpp.ReadPolicy()
	if !n.ReadOnly {
		policy = n.Writer
	}
	loaded, err := openPreferredWithContext(ctx, path, runtime.NumCPU(), policy)
	if err != nil {
		n.record(telemetry.Record{Kind: telemetry.KindError, Err: "the embedding model failed to load"})
		if ctx.Err() != nil {
			return fmt.Errorf("semantic search stalled while preparing embeddings")
		}
		return fmt.Errorf("the embedding model failed to load")
	}
	if !n.ReadOnly {
		loaded.FallbackReason = writerFallbackReason(policy.Reason(), loaded.FallbackReason)
	}
	n.engine = loaded
	n.backend = loaded.Backend
	n.fallback = loaded.FallbackReason
	n.record(telemetry.Record{
		Kind: telemetry.KindLoad, Backend: n.backend, Fallback: n.fallback,
		DurationMS: time.Since(started).Milliseconds(), MemoryHWM: memoryHighWater(),
	})
	return nil
}

func openPreferredWithContext(ctx context.Context, path string, threads int, policy llamacpp.Policy) (*llamacpp.Engine, error) {
	loaded, err := nativeOpenPreferred(path, threads, policy)
	if ctxErr := ctx.Err(); ctxErr != nil {
		if loaded != nil {
			loaded.Close()
		}
		return nil, ctxErr
	}
	return loaded, err
}

func (n *Native) Accelerated() bool {
	_ = n.acquireNative(context.Background())
	defer n.releaseNative()
	return n.backend == llamacpp.BackendMetal
}

func (n *Native) Close() {
	if n == nil {
		return
	}
	_ = n.acquireNative(context.Background())
	defer n.releaseNative()
	if n.engine != nil {
		n.engine.Close()
		n.engine = nil
	}
}

func (n *Native) Prewarm(ctx context.Context) error {
	started := time.Now()
	if _, err := n.Embed(telemetry.WithOperation(ctx, telemetry.OperationPrewarm), DefaultModel, []string{QueryPrefix + "warmup"}); err != nil {
		return err
	}
	n.record(telemetry.Record{
		Kind: telemetry.KindPrewarm, Backend: n.backend, Fallback: n.fallback,
		DurationMS: time.Since(started).Milliseconds(),
	})
	n.emit(engine.Result("prewarm", "semantic search: ready"))
	return nil
}
