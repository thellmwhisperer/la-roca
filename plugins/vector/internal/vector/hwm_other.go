//go:build !darwin && !linux && !windows

package vector

func processHighWater() int64 { return 0 }
