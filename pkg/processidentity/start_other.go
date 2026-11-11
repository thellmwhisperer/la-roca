//go:build !darwin && !linux && !windows

package processidentity

import "fmt"

func Start(int) (string, error) {
	return "", fmt.Errorf("process start identity is unavailable")
}
