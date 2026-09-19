//go:build !windows

package resident

import "syscall"

func detachedProcessAttr() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setsid: true} }
