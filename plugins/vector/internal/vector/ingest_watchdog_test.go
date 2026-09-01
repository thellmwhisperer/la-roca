package vector

import (
	"context"
	"errors"
	"strings"
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
	restartTrappedWorker = func(string) error {
		restarted <- struct{}{}
		return nil
	}
	t.Cleanup(func() { restartTrappedWorker = previous })
	native := &Native{}
	native.markNativeTrapped("sha256:one-shot")
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
	restartTrappedWorker = func(string) error {
		restarted <- struct{}{}
		return nil
	}
	t.Cleanup(func() { restartTrappedWorker = previous })
	native := &Native{}
	EnableWorkerRestartOnNativeTrap(native)
	native.markNativeTrapped("sha256:worker")
	select {
	case <-restarted:
	case <-time.After(time.Second):
		t.Fatal("worker native trap did not request a restart")
	}
}

func TestWorkerNativeTrapFailsAfterOneRestartOfTheSameElement(t *testing.T) {
	previousExec := execWorkerProcess
	execCalls := 0
	var restartedEnvironment []string
	execFailure := errors.New("exec intercepted")
	execWorkerProcess = func(_ string, _ []string, environment []string) error {
		execCalls++
		restartedEnvironment = environment
		return execFailure
	}
	t.Cleanup(func() { execWorkerProcess = previousExec })
	t.Setenv(nativeTrapElementEnv, "")
	t.Setenv(nativeTrapRestartsEnv, "")
	element := "sha256:repeatable"
	if err := restartTrappedWorkerProcess(element); !errors.Is(err, execFailure) {
		t.Fatalf("first trap restart = %v, want exec attempt", err)
	}
	for _, entry := range restartedEnvironment {
		if key, value, ok := strings.Cut(entry, "="); ok {
			switch key {
			case nativeTrapElementEnv, nativeTrapRestartsEnv:
				t.Setenv(key, value)
			}
		}
	}
	native := &Native{trapAction: restartTrappedWorkerProcess}
	native.markNativeTrapped(element)
	err := native.TerminalError()
	if !errors.Is(err, errNativeTrapped) || !strings.Contains(err.Error(), element) ||
		!strings.Contains(err.Error(), "after 1 worker restart") {
		t.Fatalf("second trap = %v, want named terminal failure", err)
	}
	if execCalls != 1 {
		t.Fatalf("exec calls = %d, want one bounded restart", execCalls)
	}
}
