//go:build cgo && !windows

package vector

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestNativeEmbedTrapRestartsAndFreesFutureCallers(t *testing.T) {
	previousTimeout := nativeCallTimeout
	nativeCallTimeout = 40 * time.Millisecond
	t.Cleanup(func() { nativeCallTimeout = previousTimeout })
	restarted := make(chan struct{}, 1)
	restart := func(string) error {
		select {
		case restarted <- struct{}{}:
		default:
		}
		return nil
	}
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
	EnableWorkerRestartOnNativeTrap(native, restart)
	first := make(chan error, 1)
	go func() {
		_, err := native.Embed(context.Background(), DefaultModel, []string{"trapped"})
		first <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("native call did not start")
	}
	select {
	case err := <-first:
		if err == nil || !strings.Contains(err.Error(), "stalled") {
			t.Fatalf("trapped embed error = %v, want a stall", err)
		}
	case <-time.After(time.Second):
		t.Fatal("caller was not released after the native trap")
	}
	select {
	case <-restarted:
	case <-time.After(time.Second):
		t.Fatal("native trap did not restart the worker")
	}
	nativeCallTimeout = 2 * time.Second
	began := time.Now()
	_, err := native.Embed(context.Background(), DefaultModel, []string{"again"})
	if elapsed := time.Since(began); elapsed > 200*time.Millisecond {
		t.Fatalf("future caller waited %s for a trapped engine", elapsed)
	}
	if err == nil || !strings.Contains(err.Error(), "stalled") {
		t.Fatalf("future caller error = %v, want a stall", err)
	}
}
