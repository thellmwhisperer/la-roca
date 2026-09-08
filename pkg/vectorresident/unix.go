//go:build !windows

package vectorresident

import (
	"fmt"
	"io"
	"net"
	"os"
	"syscall"
	"time"
)

func detachedResidentAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}

func dialUnixTimeout(socket string, timeout time.Duration) (io.ReadWriteCloser, error) {
	info, err := os.Lstat(socket)
	if err != nil {
		return nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) || info.Mode()&os.ModeSocket == 0 || info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("unsafe semantic search resident socket: %s", socket)
	}
	return net.DialTimeout("unix", socket, timeout)
}

func validateResidentDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("semantic search resident directory must be owned by the current user and private: %s", path)
	}
	return nil
}
