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
	native := &Native{}
	native.markNativeTrapped("sha256:one-shot")
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

func TestWorkerNativeTrapRequestsCancellationBeforeRestart(t *testing.T) {
	canceled := make(chan struct{})
	recovery := NewWorkerTrapRecovery(func() { close(canceled) }, func() {})
	native := &Native{}
	EnableWorkerRestartOnNativeTrap(native, recovery.Handle)
	native.markNativeTrapped("sha256:worker")
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("worker native trap did not cancel in-flight work")
	}
}

func TestWorkerNativeTrapLedgerRemembersNonconsecutiveElements(t *testing.T) {
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
	t.Setenv(nativeTrapDeathsEnv, "")
	applyRestartEnvironment := func() {
		for _, entry := range restartedEnvironment {
			if key, value, ok := strings.Cut(entry, "="); ok && key == nativeTrapDeathsEnv {
				t.Setenv(key, value)
			}
		}
	}
	requestRestart := func(element string) {
		canceled := false
		drained := false
		recovery := NewWorkerTrapRecovery(func() { canceled = true }, func() {
			if !canceled {
				t.Fatal("worker commands drained before cancellation")
			}
			drained = true
		})
		if err := recovery.Handle(element); err != nil {
			t.Fatalf("request restart for %s: %v", element, err)
		}
		if !canceled {
			t.Fatalf("restart for %s did not cancel in-flight work", element)
		}
		if requested, err := recovery.RestartIfRequested(); !requested || !errors.Is(err, execFailure) {
			t.Fatalf("restart for %s = requested %t, error %v", element, requested, err)
		}
		if !drained {
			t.Fatalf("restart for %s did not drain worker commands", element)
		}
		applyRestartEnvironment()
	}
	requestRestart("sha256:A")
	requestRestart("sha256:B")
	canceled := false
	recovery := NewWorkerTrapRecovery(func() { canceled = true }, func() {})
	native := &Native{}
	EnableWorkerRestartOnNativeTrap(native, recovery.Handle)
	native.markNativeTrapped("sha256:A")
	err := native.TerminalError()
	if !errors.Is(err, errNativeTrapped) || !strings.Contains(err.Error(), "sha256:A") ||
		!strings.Contains(err.Error(), "after 1 worker restart") {
		t.Fatalf("repeated nonconsecutive trap = %v, want named terminal failure", err)
	}
	if canceled {
		t.Fatal("exhausted restart budget canceled work for another exec")
	}
	if requested, _ := recovery.RestartIfRequested(); requested {
		t.Fatal("exhausted restart budget retained another restart")
	}
	if execCalls != 2 {
		t.Fatalf("exec calls = %d, want one each for A and B", execCalls)
	}
}
