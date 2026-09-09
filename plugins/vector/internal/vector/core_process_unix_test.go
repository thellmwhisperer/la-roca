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
	for _, mode := range []string{"legacy", "reader", "page-deadline"} {
		t.Run(mode, func(t *testing.T) {
			pidPath := t.TempDir() + "/child.pid"
			script := t.TempDir() + "/reader"
			t.Setenv("D4_CHILD_PID", pidPath)
			if err := os.WriteFile(script, []byte("#!/bin/sh\nsleep 30 & echo $! > \"$D4_CHILD_PID\"; wait\n"), 0700); err != nil {
				t.Fatal(err)
			}
			if mode == "page-deadline" {
				previous := ingestPageTimeout
				ingestPageTimeout = 250 * time.Millisecond
				t.Cleanup(func() { ingestPageTimeout = previous })
			}
			ctx, cancel := context.WithCancel(context.Background())
			ctx, drain := WithWorkerCommandDrain(ctx)
			done := make(chan error, 1)
			go func() {
				var err error
				switch mode {
				case "legacy":
					_, err = runCommand(ctx, script)
				case "reader":
					_, err = (CoreCLI{Executable: script}).query(ctx, "SELECT 1")
				case "page-deadline":
					_, err = (CoreCLI{Executable: script}).queryIngest(ctx, "SELECT 1")
				}
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
			if mode != "page-deadline" {
				cancel()
			}
			drain()
			defer cancel()
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
		})
	}
}
