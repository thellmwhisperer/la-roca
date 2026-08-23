//go:build linux

package store

import (
	"os"
	"syscall"
	"testing"
)

// TestSnapshotProcVanished pins the classification that keeps the procfs probe
// from aborting when a process it was scanning exits mid-scan. On Linux the fd
// directory of a dying process can fail with ESRCH ("no such process") instead
// of ENOENT; both mean the process is gone and cannot hold a snapshot open, so
// the probe must skip it rather than report the whole probe as undetermined.
func TestSnapshotProcVanished(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil error", err: nil, want: false},
		{name: "not exist sentinel", err: os.ErrNotExist, want: true},
		{name: "path enoent", err: &os.PathError{Op: "open", Path: "/proc/1/fd", Err: syscall.ENOENT}, want: true},
		{name: "path no such process", err: &os.PathError{Op: "open", Path: "/proc/1/fd", Err: syscall.ESRCH}, want: true},
		{name: "raw no such process", err: syscall.ESRCH, want: true},
		{name: "permission denied", err: &os.PathError{Op: "open", Path: "/proc/1/fd", Err: syscall.EACCES}, want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := snapshotProcVanished(test.err); got != test.want {
				t.Fatalf("snapshotProcVanished(%v) = %t, want %t", test.err, got, test.want)
			}
		})
	}
}
