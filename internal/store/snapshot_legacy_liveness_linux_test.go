//go:build linux

package store

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

// TestSnapshotProcUninspectable pins the classification that keeps the procfs
// probe from aborting when a same-user process refuses to expose its file
// descriptors. Non-dumpable processes (PR_SET_DUMPABLE=0, common for the .NET
// runner processes on CI) make readlink(/proc/<pid>/fd/<n>) fail with EACCES or
// EPERM; they can never be roca snapshot holders, so the probe must skip them
// rather than treat the whole sweep as indeterminate and silently stop reaping.
func TestSnapshotProcUninspectable(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil error", err: nil, want: false},
		{name: "permission denied", err: &os.PathError{Op: "readlink", Path: "/proc/1/fd/3", Err: syscall.EACCES}, want: true},
		{name: "operation not permitted", err: &os.PathError{Op: "readlink", Path: "/proc/1/fd/3", Err: syscall.EPERM}, want: true},
		{name: "raw permission denied", err: syscall.EACCES, want: true},
		{name: "not exist is vanished not uninspectable", err: syscall.ENOENT, want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := snapshotProcUninspectable(test.err); got != test.want {
				t.Fatalf("snapshotProcUninspectable(%v) = %t, want %t", test.err, got, test.want)
			}
		})
	}
}

// TestLegacyOrphanReapedBesideNonDumpableProcess reproduces the CI failure mode:
// a same-user process that refuses descriptor inspection (PR_SET_DUMPABLE=0,
// like the .NET runner processes on GitHub Actions) must not make the legacy
// sweep indeterminate. A dead legacy orphan is still reaped even while such a
// process is running, because the uninspectable process can never be a roca
// snapshot holder.
func TestLegacyOrphanReapedBesideNonDumpableProcess(t *testing.T) {
	tempRoot := isolateSnapshotTemp(t)
	orphan := createLegacyOrphan(t, tempRoot, "beside-nondumpable")
	startNonDumpableHolder(t, tempRoot)

	snapshot, err := OpenReadOnlySnapshot(context.Background(), fixtureDatabase(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := snapshot.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatalf("legacy orphan beside a non-dumpable process was not reaped: %v", err)
	}
}

// startNonDumpableHolder launches the test binary in a mode that sets
// PR_SET_DUMPABLE=0 and then holds an unrelated file open, so every
// readlink(/proc/<pid>/fd/<n>) for that process fails with EACCES. It returns
// once the holder announces readiness.
func startNonDumpableHolder(t *testing.T, tmp string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestNonDumpableHolderProcess$")
	cmd.Env = append(os.Environ(),
		"ROCA_NONDUMPABLE_HOLDER=1",
		"TMPDIR="+tmp,
		"TMP="+tmp,
		"TEMP="+tmp,
	)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = stdin.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		_ = cmd.Wait()
		t.Fatalf("holder stdout: %v", err)
	}
	if strings.TrimSpace(line) != "READY" {
		t.Fatalf("holder announced %q", line)
	}
	return cmd
}

// TestNonDumpableHolderProcess is the re-executed helper: it drops its own
// dumpable flag and blocks with an open file handle until its stdin closes.
func TestNonDumpableHolderProcess(t *testing.T) {
	if os.Getenv("ROCA_NONDUMPABLE_HOLDER") != "1" {
		t.Skip("holder process")
	}
	_, _, errno := syscall.RawSyscall6(syscall.SYS_PRCTL, syscall.PR_SET_DUMPABLE, 0, 0, 0, 0, 0)
	if errno != 0 {
		t.Fatalf("set dumpable: %v", errno)
	}
	dir, err := os.MkdirTemp("", "nondumpable-holder-")
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(filepath.Join(dir, "held.db"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Println("READY")
	os.Stdout.Sync()
	_, _ = io.Copy(io.Discard, os.Stdin)
	_ = file.Close()
	_ = os.RemoveAll(dir)
}
