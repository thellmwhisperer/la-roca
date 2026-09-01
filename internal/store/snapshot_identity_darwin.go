//go:build darwin

package store

import (
	"fmt"
	"os"
	"syscall"
)

func snapshotFileIdentity(_ string, info os.FileInfo) (string, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", fmt.Errorf("unsupported file metadata %T", info.Sys())
	}
	return fmt.Sprintf("%d:%d:%d:%d", stat.Dev, stat.Ino, stat.Ctimespec.Sec, stat.Ctimespec.Nsec), nil
}
