//go:build !windows

/**
 * @overview Verifies Unix signal cleanup for snapshot owners. ~120 lines, no public symbols.
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
 *   TestCatchableSignalsRemoveOpenSnapshots, TestSignalDuringLeaseRegistrationCleansStaging
 *   TestSignalCleanupHasABoundedFallback, TestSignalCleanupDoesNotWaitForNamespaceLock
 *   signalHelper, assertSnapshotDirAbsent
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
			signalHelper(t, helper, test.signal, "signaled helper did not exit")
			assertSnapshotDirAbsent(t, helper.directory, "signal left snapshot directory")
		})
	}
}

func TestSignalDuringLeaseRegistrationCleansStaging(t *testing.T) {
	root := isolateSnapshotTemp(t)
	helper := startSnapshotHelper(t, root, fixtureDatabase(t), "signal-registration")
	signalHelper(t, helper, syscall.SIGHUP, "registration-race helper did not exit")
	assertSnapshotDirAbsent(t, helper.directory, "signal left staging directory")
}

func TestSignalDuringCopyRemovesSnapshot(t *testing.T) {
	root := isolateSnapshotTemp(t)
	helper := startSnapshotHelper(t, root, fixtureDatabase(t), "signal-during-copy")
	signalHelper(t, helper, syscall.SIGHUP, "signaled helper did not exit")
	assertSnapshotDirAbsent(t, helper.directory, "signal left snapshot directory during copy")
}

func TestSignalCleanupHasABoundedFallback(t *testing.T) {
	root := isolateSnapshotTemp(t)
	helper := startSnapshotHelper(t, root, fixtureDatabase(t), "hold-query")
	started := time.Now()
	signalHelper(t, helper, syscall.SIGHUP, "signal cleanup waited indefinitely for the active query")
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("signal fallback took %v", elapsed)
	}

	next, err := OpenReadOnlySnapshot(t.Context(), fixtureDatabase(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := next.Close(); err != nil {
		t.Fatal(err)
	}
	if err := CloseReadOnlySnapshots(); err != nil {
		t.Fatal(err)
	}
	if dirs := listSnapshotDirs(t, root); len(dirs) != 0 {
		t.Fatalf("next open left snapshot dirs %v", dirs)
	}
}

func TestSignalCleanupDoesNotWaitForNamespaceLock(t *testing.T) {
	root := isolateSnapshotTemp(t)
	helper := startSnapshotHelper(t, root, fixtureDatabase(t), "hold-forever")
	unlock := lockNamespaceLease(t, root)
	signalHelper(t, helper, syscall.SIGHUP, "signal cleanup waited for the namespace lock")
	assertSnapshotDirAbsent(t, helper.directory, "signal cleanup left snapshot while namespace was locked")
	unlock()
}

// -/ 1/1

// -- 2/2 HELPER · Signal delivery and directory assertions --

func signalHelper(t *testing.T, helper *snapshotHelper, signal os.Signal, timeoutMsg string) {
	t.Helper()
	if err := helper.cmd.Process.Signal(signal); err != nil {
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
		t.Fatal(timeoutMsg)
	}
}

func assertSnapshotDirAbsent(t *testing.T, directory, msg string) {
	t.Helper()
	if _, err := os.Stat(directory); !os.IsNotExist(err) {
		t.Fatalf("%s: %v", msg, err)
	}
}

// -/ 2/2
