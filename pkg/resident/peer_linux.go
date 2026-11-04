package resident

import (
	"net"

	"golang.org/x/sys/unix"
)

// peerPID is the process at the other end of conn, or 0 when it cannot be read.
func peerPID(conn *net.UnixConn) (pid int) {
	raw, _ := conn.SyscallConn()
	_ = raw.Control(func(fd uintptr) {
		if cred, err := unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED); err == nil {
			pid = int(cred.Pid)
		}
	})
	return pid
}
