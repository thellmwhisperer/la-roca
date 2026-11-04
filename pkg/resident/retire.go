package resident

import (
	"io"
	"net"
	"os"
	"syscall"
	"time"
)

// Retire stops the process listening on a resident socket, whatever build it
// runs, and returns once that process has closed its end. Nothing listening is
// nothing to do. It serves the core and the vector resident alike: both listen
// on a unix socket and both stop on SIGTERM.
//
// ponytail: a resident that outlives SIGTERM for startupWait is left running;
// escalate to SIGKILL if one ever does.
func Retire(socket string) {
	conn, err := net.DialTimeout("unix", socket, 200*time.Millisecond)
	if err != nil {
		return
	}
	defer conn.Close()
	if pid := peerPID(conn.(*net.UnixConn)); pid > 0 {
		process, _ := os.FindProcess(pid)
		_ = process.Signal(syscall.SIGTERM)
		_ = conn.SetReadDeadline(time.Now().Add(startupWait))
		_, _ = io.Copy(io.Discard, conn)
	}
}
