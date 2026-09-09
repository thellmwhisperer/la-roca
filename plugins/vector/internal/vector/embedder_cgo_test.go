//go:build cgo && !windows

package vector

import (
	"context"
	"errors"
	"slices"
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

type nativeEngineFunc func(string) ([]float32, int, error)

func (f nativeEngineFunc) Embed(text string) ([]float32, int, error) { return f(text) }
func (nativeEngineFunc) Close()                                      {}

func TestNativeEmbedBatchWatchdog(t *testing.T) {
	previous := nativeCallTimeout
	nativeCallTimeout = 200 * time.Millisecond
	t.Cleanup(func() { nativeCallTimeout = previous })
	for _, tc := range []struct {
		name     string
		stall    bool
		deadline time.Duration
		wantErr  error
	}{
		{name: "progressing batch exceeds operation timeout"},
		{name: "later operation stalls", stall: true, wantErr: errNativeTrapped},
		{name: "caller deadline bounds batch", deadline: 80 * time.Millisecond, wantErr: context.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			release := make(chan struct{})
			defer close(release)
			var calls atomic.Int32
			native := &Native{engine: nativeEngineFunc(func(string) ([]float32, int, error) {
				if calls.Add(1) == 2 && tc.stall {
					<-release
				}
				time.Sleep(10 * time.Millisecond)
				return []float32{1, 0}, 1, nil
			})}
			trapped := make(chan string, 1)
			EnableWorkerRestartOnNativeTrap(native, func(element string) error {
				trapped <- element
				return nil
			})
			ctx := context.Background()
			if tc.deadline > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, tc.deadline)
				defer cancel()
			}
			input := slices.Repeat([]string{"later"}, 64)
			input[0] = "first"
			vectors, err := native.Embed(ctx, DefaultModel, input)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Embed error = %v, want %v", err, tc.wantErr)
			}
			if tc.wantErr == nil {
				if len(vectors) != len(input) || calls.Load() != int32(len(input)) {
					t.Fatalf("vectors = %d, calls = %d, want %d", len(vectors), calls.Load(), len(input))
				}
			} else if vectors != nil || calls.Load() >= int32(len(input)) {
				t.Fatalf("interrupted batch returned vectors or finished: vectors = %d, calls = %d", len(vectors), calls.Load())
			}
			if tc.stall {
				if element := <-trapped; element != nativeElementIdentity(input[1]) {
					t.Fatalf("trapped element = %s, want second input", element)
				}
			} else if err := native.TerminalError(); err != nil {
				t.Fatalf("healthy engine marked trapped: %v", err)
			}
		})
	}
}

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
