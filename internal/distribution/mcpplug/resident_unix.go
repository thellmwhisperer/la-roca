//go:build !windows

package mcpplug

import (
	"io"
	"net"
	"syscall"
	"time"
)

func detachedResidentAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}

func dialUnixTimeout(socket string, timeout time.Duration) (io.ReadWriteCloser, error) {
	return net.DialTimeout("unix", socket, timeout)
}
