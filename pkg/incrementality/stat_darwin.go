package incrementality

import (
	"fmt"
	"os"
	"syscall"
)

func fileChangeIdentity(info os.FileInfo) string {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return ""
	}
	return fmt.Sprintf("%d:%d:%d:%d", stat.Dev, stat.Ino, stat.Ctimespec.Sec, stat.Ctimespec.Nsec)
}
