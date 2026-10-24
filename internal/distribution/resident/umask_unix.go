//go:build !windows

package resident

import "syscall"

func setUmask(mask int) int { return syscall.Umask(mask) }
