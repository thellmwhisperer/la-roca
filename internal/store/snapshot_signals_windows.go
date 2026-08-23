//go:build windows

/**
 * @overview Defines Windows snapshot process boundaries. ~65 lines, no public symbols.
 *
 *   READING GUIDE
 *   -------------
 *   1. Start at snapshotTerminationSignals  <- supported termination set
 *   2. snapshotUserIdentity                 <- scopes temp files to one user
 *   3. terminateSnapshotProcess             <- exits after cleanup
 *
 *   MAIN FLOW
 *   ---------
 *   OpenReadOnlySnapshot -> snapshotUserIdentity -> private namespace
 *   ensureSnapshotExitCleanup -> cleanup -> terminateSnapshotProcess
 *
 *   PUBLIC API
 *   ----------
 *   None.
 *
 *   INTERNALS
 *   ---------
 *   snapshotTerminationSignals, snapshotUserIdentity, snapshotNamespaceOwned
 *   snapshotNamespacePermissionsValid, terminateSnapshotProcess
 *
 * @exports
 * @deps os; os/user; syscall
 */
package store

import (
	"os"
	"os/user"
	"syscall"
)

// -- 1/2 CORE · Windows identity and namespace ownership -- <- START HERE

func snapshotUserIdentity() (string, error) {
	current, err := user.Current()
	if err != nil {
		return "", err
	}
	return current.Uid, nil
}

func snapshotNamespaceOwned(os.FileInfo) bool {
	return true
}

func snapshotNamespacePermissionsValid(os.FileInfo) bool {
	return true
}

// -/ 1/2

// -- 2/2 CORE · Windows signal termination --

func snapshotTerminationSignals() []os.Signal {
	return []os.Signal{os.Interrupt, syscall.SIGTERM}
}

func terminateSnapshotProcess(os.Signal) {
	os.Exit(130)
}

// -/ 2/2
