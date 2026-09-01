//go:build !windows

package vector

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestRunCommandCancellationKillsAndReapsItsProcessGroup(t *testing.T) {
	pidPath := t.TempDir() + "/child.pid"
	ctx, cancel := context.WithCancel(context.Background())
	ctx, drain := WithWorkerCommandDrain(ctx)
	done := make(chan error, 1)
	go func() {
		_, err := runCommand(ctx, "sh", "-c", `sleep 30 & echo $! > "$1"; wait`, "sh", pidPath)
		done <- err
	}()
	var pid int
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(pidPath)
		if err == nil {
			pid, _ = strconv.Atoi(strings.TrimSpace(string(raw)))
			if pid > 0 {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if pid <= 0 {
		cancel()
		<-done
		t.Fatal("command did not start its child")
	}
	cancel()
	drain()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("canceled command returned no error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("canceled command was not reaped")
	}
	deadline = time.Now().Add(time.Second)
	for processAlive(pid) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if processAlive(pid) {
		t.Fatalf("process-group child %d survived cancellation", pid)
	}
}
