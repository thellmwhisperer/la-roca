//go:build windows

/**
 * @overview Declares Windows snapshot cleanup signals. ~35 lines, no public symbols.
 *
 *   READING GUIDE
 *   -------------
 *   1. Start at snapshotTerminationSignals  <- supported termination set
 *
 *   MAIN FLOW
 *   ---------
 *   ensureSnapshotExitCleanup -> snapshotTerminationSignals -> signal.Notify
 *
 *   PUBLIC API
 *   ----------
 *   None.
 *
 *   INTERNALS
 *   ---------
 *   snapshotTerminationSignals
 *
 * @exports
 * @deps os; syscall
 */
package store

import (
	"os"
	"syscall"
)

// -- 1/1 CORE · snapshotTerminationSignals -- <- START HERE

func snapshotTerminationSignals() []os.Signal {
	return []os.Signal{os.Interrupt, syscall.SIGTERM}
}

// -/ 1/1
