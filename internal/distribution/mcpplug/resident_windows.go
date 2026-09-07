//go:build windows

package mcpplug

import (
	"io"
	"net"
	"syscall"
	"time"
)

func detachedResidentAttr() *syscall.SysProcAttr {
	const detachedProcess = 0x00000008
	const createNewProcessGroup = 0x00000200
	return &syscall.SysProcAttr{CreationFlags: detachedProcess | createNewProcessGroup}
}

func dialUnixTimeout(socket string, timeout time.Duration) (io.ReadWriteCloser, error) {
	return net.DialTimeout("unix", socket, timeout)
}
