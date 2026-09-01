//go:build darwin || linux

package store

import (
	"fmt"
	"os"
)

func snapshotFileIdentity(_ string, info os.FileInfo) (string, error) {
	stat, ok := info.Sys().(*snapshotStat)
	if !ok {
		return "", fmt.Errorf("unsupported file metadata %T", info.Sys())
	}
	changedSec, changedNSec := snapshotChangeTime(stat)
	return fmt.Sprintf("%d:%d:%d:%d", stat.Dev, stat.Ino, changedSec, changedNSec), nil
}
