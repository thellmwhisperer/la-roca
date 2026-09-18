//go:build !windows

package vector

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWorkerStallWatchdogKillsOrSparesAStuckProcess(t *testing.T) {
	for _, tc := range []struct {
		name string
		stop bool
	}{
		{name: "term after deadline"},
		{name: "stop prevents term", stop: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			command := exec.Command("/bin/sleep", "30")
			if err := command.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() {
				_ = command.Process.Kill()
				_, _ = command.Process.Wait()
			}()
			logPath := filepath.Join(t.TempDir(), "worker.log")
			stop, err := armWorkerStallWatchdog(time.Second, command.Process.Pid, logPath)
			if err != nil {
				t.Fatal(err)
			}
			if tc.stop {
				stop()
				time.Sleep(1500 * time.Millisecond)
				if !processAlive(command.Process.Pid) {
					t.Fatal("stopped watchdog still terminated the process")
				}
				return
			}
			defer stop()
			done := make(chan error, 1)
			go func() { done <- command.Wait() }()
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("stuck process exited 0")
				}
			case <-time.After(4 * time.Second):
				t.Fatal("worker stall watchdog did not terminate the stuck process")
			}
			body, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(body), "semantic search stalled") ||
				!strings.Contains(string(body), "indexing worker") {
				t.Fatalf("watchdog log = %q, want a stall diagnostic", body)
			}
		})
	}
}

func TestWatchdogSleepLeavesAReusedPidAlone(t *testing.T) {
	if !RunWatchdogSleepIfRequested([]string{
		"roca-vector", workerWatchdogArg, "1", "1", "not-the-real-identity",
	}) {
		t.Fatal("watchdog reap did not claim the argv")
	}
}

func TestWatchdogSleepRejectsBadArgv(t *testing.T) {
	if RunWatchdogSleepIfRequested([]string{"roca-vector", "status"}) {
		t.Fatal("non-watchdog argv was treated as a reap")
	}
}
