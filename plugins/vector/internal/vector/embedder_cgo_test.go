//go:build cgo && !windows

package vector

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thellmwhisperer/la-roca-vector/internal/llamacpp"
)

type blockingEngine struct {
	inFlight atomic.Int32
	overlap  atomic.Bool
	started  chan struct{}
	block    chan struct{}
}

func (e *blockingEngine) Embed(string) ([]float32, int, error) {
	n := e.inFlight.Add(1)
	if n > 1 {
		e.overlap.Store(true)
	}
	if e.started != nil {
		select {
		case <-e.started:
		default:
			close(e.started)
		}
	}
	if e.block != nil {
		<-e.block
	}
	e.inFlight.Add(-1)
	return []float32{1, 0, 0, 0, 0, 0, 0, 0}, 1, nil
}

func (e *blockingEngine) Close() {}

func TestNativeEmbedFailsInsteadOfHanging(t *testing.T) {
	previous := nativeCallTimeout
	nativeCallTimeout = 50 * time.Millisecond
	t.Cleanup(func() { nativeCallTimeout = previous })
	started := make(chan struct{})
	block := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-block:
		default:
			close(block)
		}
	})
	native := &Native{engine: &blockingEngine{started: started, block: block}}
	errCh := make(chan error, 1)
	go func() {
		_, err := native.Embed(context.Background(), DefaultModel, []string{"hello"})
		errCh <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("engine was not called")
	}
	select {
	case err := <-errCh:
		if err == nil || !strings.Contains(err.Error(), "stalled") {
			t.Fatalf("Embed error = %v, want a stall", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Embed hung after the engine stopped returning")
	}
}

func TestOpenPreferredRetainsOwnershipUntilLateOpenCompletes(t *testing.T) {
	previous := nativeOpenPreferred
	started := make(chan struct{})
	release := make(chan struct{})
	nativeOpenPreferred = func(string, int, llamacpp.Policy) (*llamacpp.Engine, error) {
		close(started)
		<-release
		return &llamacpp.Engine{}, nil
	}
	t.Cleanup(func() {
		nativeOpenPreferred = previous
		select {
		case <-release:
		default:
			close(release)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := openPreferredWithContext(ctx, "model", 1, llamacpp.ReadPolicy())
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("native open did not start")
	}
	<-ctx.Done()
	select {
	case err := <-done:
		t.Fatalf("native open ownership ended before the underlying open completed: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("late native open error = %v, want deadline exceeded", err)
		}
	case <-time.After(time.Second):
		t.Fatal("native open ownership was not released after completion")
	}
}

func TestNativeEmbedPreservesWaitingCallerDeadline(t *testing.T) {
	started := make(chan struct{})
	block := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-block:
		default:
			close(block)
		}
	})
	native := &Native{engine: &blockingEngine{started: started, block: block}}
	active := make(chan error, 1)
	go func() {
		_, err := native.Embed(context.Background(), DefaultModel, []string{"active"})
		active <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("active native call did not start")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err := native.Embed(ctx, DefaultModel, []string{"waiting"})
	if !errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "stalled") {
		t.Fatalf("waiting caller error = %v, want its deadline", err)
	}
	close(block)
	select {
	case err := <-active:
		if err != nil {
			t.Fatalf("active native call: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("active native call did not finish")
	}
}

func TestNativeEmbedPreservesActiveCallerCancellation(t *testing.T) {
	started := make(chan struct{})
	block := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-block:
		default:
			close(block)
		}
	})
	native := &Native{engine: &blockingEngine{started: started, block: block}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := native.Embed(ctx, DefaultModel, []string{"active"})
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("native call did not start")
	}
	cancel()
	close(block)
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "stalled") {
			t.Fatalf("active caller error = %v, want cancellation", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled native caller did not return")
	}
}

func TestNativeEmbedDoesNotOverlapEngineCalls(t *testing.T) {
	engine := &blockingEngine{block: make(chan struct{})}
	native := &Native{engine: engine}
	var wg sync.WaitGroup
	wg.Add(2)
	for range 2 {
		go func() {
			defer wg.Done()
			_, _ = native.Embed(context.Background(), DefaultModel, []string{"hello"})
		}()
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if engine.inFlight.Load() >= 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	if got := engine.inFlight.Load(); got != 1 {
		close(engine.block)
		wg.Wait()
		t.Fatalf("in-flight engine calls = %d, want 1", got)
	}
	close(engine.block)
	wg.Wait()
	if engine.overlap.Load() {
		t.Fatal("engine.Embed overlapped across goroutines")
	}
}
