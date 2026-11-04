//go:build darwin || linux

package resident

import (
	"bufio"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestRetireStopsTheListeningProcess re-executes the test binary as the
// resident, so the process Retire signals is not this one.
func TestRetireStopsTheListeningProcess(t *testing.T) {
	if socket := os.Getenv("RESIDENT_RETIRE_LISTEN"); socket != "" {
		listener, err := net.Listen("unix", socket)
		if err != nil {
			os.Exit(2)
		}
		os.Stdout.WriteString("listening\n")
		for {
			if _, err := listener.Accept(); err != nil {
				os.Exit(3)
			}
		}
	}
	dir, err := os.MkdirTemp("", "retire-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	socket := filepath.Join(dir, "resident.sock")
	Retire(socket) // nothing listening: nothing to do

	child := exec.Command(os.Args[0], "-test.run=^TestRetireStopsTheListeningProcess$")
	child.Env = append(os.Environ(), "RESIDENT_RETIRE_LISTEN="+socket)
	stdout, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = child.Process.Kill() })
	if line, err := bufio.NewReader(stdout).ReadString('\n'); err != nil || line != "listening\n" {
		t.Fatalf("resident did not start: %q %v", line, err)
	}

	Retire(socket)
	if err := child.Wait(); err == nil || child.ProcessState.Sys() == nil || child.ProcessState.ExitCode() != -1 {
		t.Fatalf("resident was not stopped by a signal: %v", err)
	}
	if conn, err := net.Dial("unix", socket); err == nil {
		conn.Close()
		t.Fatal("the socket still accepts connections")
	}
}
