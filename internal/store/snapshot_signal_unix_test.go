//go:build !windows

/**
 * @overview Verifies Unix signal cleanup for snapshot owners. ~55 lines, no public symbols.
 *
 *   READING GUIDE
 *   -------------
 *   1. Start at TestCatchableSignalsRemoveOpenSnapshots  <- subprocess behavior
 *
 *   MAIN FLOW
 *   ---------
 *   startSnapshotHelper -> send signal -> process cleanup -> assert directory absent
 *
 *   PUBLIC API
 *   ----------
 *   None.
 *
 *   INTERNALS
 *   ---------
 *   TestCatchableSignalsRemoveOpenSnapshots
 *
 * @exports
 * @deps testing; syscall
 */
package store

import (
	"os"
	"syscall"
	"testing"
	"time"
)

// -- 1/1 CORE · TestCatchableSignalsRemoveOpenSnapshots -- <- START HERE

func TestCatchableSignalsRemoveOpenSnapshots(t *testing.T) {
	tests := []struct {
		name   string
		signal os.Signal
	}{
		{name: "SIGHUP", signal: syscall.SIGHUP},
		{name: "SIGQUIT", signal: syscall.SIGQUIT},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := isolateSnapshotTemp(t)
			helper := startSnapshotHelper(t, root, fixtureDatabase(t), "hold-forever")
			if err := helper.cmd.Process.Signal(test.signal); err != nil {
				t.Fatal(err)
			}
			waited := make(chan error, 1)
			go func() { waited <- helper.wait() }()
			select {
			case err := <-waited:
				if err == nil {
					t.Fatal("signaled helper exited successfully")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("signaled helper did not exit")
			}
			if _, err := os.Stat(helper.directory); !os.IsNotExist(err) {
				t.Fatalf("signal left snapshot directory: %v", err)
			}
		})
	}
}

// -/ 1/1
