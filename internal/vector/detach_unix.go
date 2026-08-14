//go:build !windows

package vector

import "syscall"

var detachedProcessAttributes = &syscall.SysProcAttr{Setsid: true}
