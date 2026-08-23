//go:build windows

/**
 * @overview Defines Windows snapshot process boundaries. ~80 lines, no public symbols.
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
 * @deps os; syscall; golang.org/x/sys/windows
 */
package store

import (
	"os"
	"syscall"

	"golang.org/x/sys/windows"
)

// -- 1/2 CORE · Windows identity and namespace ownership -- <- START HERE

func snapshotUserIdentity() (string, error) {
	current, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return "", err
	}
	return current.User.Sid.String(), nil
}

func snapshotNamespaceOwned(path string, _ os.FileInfo) bool {
	descriptor, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION)
	if err != nil || descriptor == nil {
		return false
	}
	owner, _, err := descriptor.Owner()
	if err != nil || owner == nil {
		return false
	}
	current, err := windows.GetCurrentProcessToken().GetTokenUser()
	return err == nil && current.User.Sid != nil && owner.Equals(current.User.Sid)
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
