//go:build !windows

package vector

import (
	"os/exec"
	"testing"
	"time"
)

func TestExternalWatchdogKillsOrSparesAStuckProcess(t *testing.T) {
	for _, tc := range []struct {
		name string
		stop bool
	}{
		{name: "kill after deadline"},
		{name: "stop prevents kill", stop: true},
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
			stop, err := armExternalWatchdog(time.Second, command.Process.Pid)
			if err != nil {
				t.Fatal(err)
			}
			if tc.stop {
				stop()
				time.Sleep(1500 * time.Millisecond)
				if !processAlive(command.Process.Pid) {
					t.Fatal("stopped watchdog still killed the process")
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
			case <-time.After(3 * time.Second):
				t.Fatal("external watchdog did not kill the stuck process")
			}
		})
	}
}
