//go:build !darwin && !linux && !windows

package vector

import "fmt"

func processStartUnixNano(int) (int64, error) {
	return 0, fmt.Errorf("process start identity is unavailable")
}
