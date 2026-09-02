//go:build !darwin && !linux && !windows

package rocavector

import "fmt"

func processStartIdentity(int) (string, error) {
	return "", fmt.Errorf("process start identity is unavailable")
}
