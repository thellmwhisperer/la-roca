//go:build linux

package vector

import "golang.org/x/sys/unix"

func processHighWater() int64 {
	var usage unix.Rusage
	if unix.Getrusage(unix.RUSAGE_SELF, &usage) != nil {
		return 0
	}
	return usage.Maxrss * 1024
}
