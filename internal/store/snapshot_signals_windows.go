//go:build windows

package store

import (
	"os"
	"syscall"

	"golang.org/x/sys/windows"
)

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

func snapshotTerminationSignals() []os.Signal {
	return []os.Signal{os.Interrupt, syscall.SIGTERM}
}

func terminateSnapshotProcess(os.Signal) {
	os.Exit(130)
}
