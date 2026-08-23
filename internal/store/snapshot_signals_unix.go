//go:build !windows

/**
 * @overview Defines Unix snapshot process boundaries. ~65 lines, no public symbols.
 *
 *   READING GUIDE
 *   -------------
 *   1. Start at snapshotTerminationSignals  <- complete catchable termination set
 *   2. snapshotUserIdentity                 <- scopes temp files to one user
 *   3. terminateSnapshotProcess             <- restores signal exit semantics
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
 * @deps os; strconv; syscall
 */
package store

import (
	"os"
	"strconv"
	"syscall"
)

// -- 1/2 CORE · Unix identity and namespace ownership -- <- START HERE

func snapshotUserIdentity() (string, error) {
	return strconv.Itoa(os.Geteuid()), nil
}

func snapshotNamespaceOwned(_ string, info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Geteuid())
}

func snapshotNamespacePermissionsValid(info os.FileInfo) bool {
	return info.Mode().Perm() == 0o700
}

// -/ 1/2

// -- 2/2 CORE · Unix signal termination --

func snapshotTerminationSignals() []os.Signal {
	return []os.Signal{os.Interrupt, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT}
}

func terminateSnapshotProcess(sig os.Signal) {
	if process, err := os.FindProcess(os.Getpid()); err == nil {
		_ = process.Signal(sig)
	}
}

// -/ 2/2
