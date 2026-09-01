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

func TestTrappedNativeRejectsNewCallersWithoutWaiting(t *testing.T) {
	restarted := make(chan struct{}, 1)
	previous := restartAfterNativeTrap
	restartAfterNativeTrap = func() {
		select {
		case restarted <- struct{}{}:
		default:
		}
	}
	t.Cleanup(func() { restartAfterNativeTrap = previous })
	native := &Native{}
	native.markNativeTrapped()
	select {
	case <-restarted:
	case <-time.After(time.Second):
		t.Fatal("native trap did not request a restart")
	}
	began := time.Now()
	err := native.acquireNative(context.Background())
	if elapsed := time.Since(began); elapsed > 100*time.Millisecond {
		t.Fatalf("acquireNative waited %s for a trapped engine", elapsed)
	}
	if !errors.Is(err, errNativeTrapped) {
		t.Fatalf("acquireNative error = %v, want trapped", err)
	}
}
