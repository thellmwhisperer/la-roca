package resident

import (
	"net"

	"golang.org/x/sys/unix"
)

// peerPID is the process at the other end of conn, or 0 when it cannot be read.
func peerPID(conn *net.UnixConn) (pid int) {
	raw, _ := conn.SyscallConn()
	_ = raw.Control(func(fd uintptr) {
		pid, _ = unix.GetsockoptInt(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERPID)
	})
	return pid
}
