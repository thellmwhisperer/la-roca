package vector

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestQueryIngestDoesNotWaitForeverWhenTheChildNeverReturns(t *testing.T) {
	previous := ingestPageTimeout
	ingestPageTimeout = 40 * time.Millisecond
	t.Cleanup(func() { ingestPageTimeout = previous })
	core := CoreCLI{Executable: "roca", Run: func(ctx context.Context, _ string, _ ...string) ([]byte, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	done := make(chan error, 1)
	go func() {
		_, err := core.queryIngest(context.Background(), "SELECT 1 AS n")
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("queryIngest error = %v, want a page deadline", err)
		}
	case <-time.After(time.Second):
		t.Fatal("queryIngest hung waiting for a child that never returned")
	}
}

func TestTrappedNativeRejectsNewCallersWithoutRestartingOneShotProcess(t *testing.T) {
	restarted := make(chan struct{}, 1)
	previous := restartTrappedWorker
	restartTrappedWorker = func() { restarted <- struct{}{} }
	t.Cleanup(func() { restartTrappedWorker = previous })
	native := &Native{}
	native.markNativeTrapped()
	select {
	case <-restarted:
		t.Fatal("one-shot native trap requested a process restart")
	default:
	}
	began := time.Now()
	err := native.acquireNative(context.Background())
	if elapsed := time.Since(began); elapsed > 100*time.Millisecond {
		t.Fatalf("acquireNative waited %s for a trapped engine", elapsed)
	}
	if !errors.Is(err, errNativeTrapped) {
		t.Fatalf("acquireNative error = %v, want trapped", err)
	}
	if !errors.Is(native.TerminalError(), errNativeTrapped) {
		t.Fatalf("terminal error = %v, want trapped", native.TerminalError())
	}
}

func TestWorkerNativeTrapRequestsProcessRestart(t *testing.T) {
	restarted := make(chan struct{}, 1)
	previous := restartTrappedWorker
	restartTrappedWorker = func() { restarted <- struct{}{} }
	t.Cleanup(func() { restartTrappedWorker = previous })
	native := &Native{}
	EnableWorkerRestartOnNativeTrap(native)
	native.markNativeTrapped()
	select {
	case <-restarted:
	case <-time.After(time.Second):
		t.Fatal("worker native trap did not request a restart")
	}
}
