//go:build !windows

package cli

import (
	"fmt"
	"os"
	"syscall"
)

func zcodeUnixRootIdentity(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", fmt.Errorf("read ZCode root identity for %s", path)
	}
	return fmt.Sprintf("%x:%x", uint64(stat.Dev), uint64(stat.Ino)), nil
}
