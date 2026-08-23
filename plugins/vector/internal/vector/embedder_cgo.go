//go:build cgo && !windows

package vector

import (
	"context"
	"fmt"
	"runtime"
	"time"

	"github.com/thellmwhisperer/la-roca-vector/internal/engine"
	"github.com/thellmwhisperer/la-roca-vector/internal/llamacpp"
	"github.com/thellmwhisperer/la-roca-vector/internal/model"
	"github.com/thellmwhisperer/la-roca-vector/internal/telemetry"
)

func (n *Native) Embed(ctx context.Context, requestedModel string, input []string) ([][]float32, error) {
	if requestedModel != DefaultModel {
		return nil, fmt.Errorf("embedding model %q is not supported by this engine", requestedModel)
	}
	n.once.Do(func() { n.err = n.open(ctx) })
	if n.err != nil {
		return nil, n.err
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
		Kind: kind, Backend: n.backend, Fallback: n.fallback,
		DurationMS: elapsed.Milliseconds(), BatchSize: len(input),
		Throughput: throughput, MemoryHWM: memoryHighWater(),
	})
	return result, nil
}

func (n *Native) open(ctx context.Context) error {
	path, err := model.Ensure(ctx, n.DataDir, model.DefaultManifest(), n.Events)
	if err != nil {
		n.record(telemetry.Record{Kind: telemetry.KindError, Err: "the embedding model is not downloaded"})
		return err
	}
	n.emit(engine.Progress("prewarm", "semantic search: preparing", 0, 1, 0))
	started := time.Now()
	engine, err := llamacpp.OpenPreferred(path, runtime.NumCPU())
	if err != nil {
		n.record(telemetry.Record{Kind: telemetry.KindError, Err: "the embedding model failed to load"})
		return fmt.Errorf("the embedding model failed to load")
	}
	n.engine = engine
	n.backend = engine.Backend
	n.fallback = engine.FallbackReason
	n.record(telemetry.Record{
		Kind: telemetry.KindLoad, Backend: n.backend, Fallback: n.fallback,
		DurationMS: time.Since(started).Milliseconds(), MemoryHWM: memoryHighWater(),
	})
	return nil
}

func (n *Native) Close() {
	if n != nil && n.engine != nil {
		n.engine.Close()
		n.engine = nil
	}
}

func (n *Native) Prewarm(ctx context.Context) error {
	started := time.Now()
	if _, err := n.Embed(ctx, DefaultModel, []string{QueryPrefix + "warmup"}); err != nil {
		return err
	}
	n.record(telemetry.Record{
		Kind: telemetry.KindPrewarm, Backend: n.backend, Fallback: n.fallback,
		DurationMS: time.Since(started).Milliseconds(),
	})
	n.emit(engine.Result("prewarm", "semantic search: ready"))
	return nil
}
