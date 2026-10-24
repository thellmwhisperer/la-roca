//go:build windows

package resident

func setUmask(int) int { return 0 }
