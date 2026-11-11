//go:build !windows

package rocavector

import (
	"errors"
	"syscall"

	"github.com/thellmwhisperer/la-roca/pkg/processidentity"
)

var processStartIdentity = processidentity.Start

func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
