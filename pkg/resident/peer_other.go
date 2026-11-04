//go:build !darwin && !linux

package resident

import "net"

// ponytail: no peer credentials here, so Retire leaves the resident running;
// read the peer process where the platform exposes it if residents ship there.
func peerPID(*net.UnixConn) int { return 0 }
