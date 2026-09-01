//go:build !windows

package store

import (
	"os"
	"strconv"
	"syscall"
)

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

func snapshotTerminationSignals() []os.Signal {
	return []os.Signal{os.Interrupt, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT}
}

func terminateSnapshotProcess(sig os.Signal) {
	if process, err := os.FindProcess(os.Getpid()); err == nil {
		_ = process.Signal(sig)
	}
}
