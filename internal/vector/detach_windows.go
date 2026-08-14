//go:build windows

package vector

import "syscall"

var detachedProcessAttributes = &syscall.SysProcAttr{CreationFlags: 0x00000008 | 0x00000200}
