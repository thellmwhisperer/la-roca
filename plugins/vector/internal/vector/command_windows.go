//go:build windows

package vector

import (
	"os/exec"
	"time"
)

func configureCommandCancellation(command *exec.Cmd) {
	command.WaitDelay = 5 * time.Second
}
