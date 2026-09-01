//go:build linux

package store

import "syscall"

type snapshotStat = syscall.Stat_t

func snapshotChangeTime(stat *snapshotStat) (int64, int64) {
	return stat.Ctim.Sec, stat.Ctim.Nsec
}
