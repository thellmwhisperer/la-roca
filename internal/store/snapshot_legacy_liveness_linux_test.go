//go:build linux

package store

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

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

func TestLegacyOrphanReapedBesideNonDumpableProcess(t *testing.T) {
	tempRoot := isolateSnapshotTemp(t)
	orphan := createLegacyOrphan(t, tempRoot, "beside-nondumpable")
	startNonDumpableHolder(t, tempRoot)
	openAndCloseSnapshot(t)
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatalf("legacy orphan beside a non-dumpable process was not reaped: %v", err)
	}
}

func startNonDumpableHolder(t *testing.T, tmp string) *exec.Cmd {
	t.Helper()
	cmd, stdout, stdin := startIsolatedTestCommand(t, tmp, "^TestNonDumpableHolderProcess$",
		"ROCA_NONDUMPABLE_HOLDER=1",
	)
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
