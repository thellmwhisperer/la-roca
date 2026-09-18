//go:build !windows

package vector

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"
)

func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// armExternalWatchdog kills pid if this process cannot cancel a native call.
// Go timers share the wedged thread pool; an outside sleep does not.
func armExternalWatchdog(d time.Duration, pid int) (func(), error) {
	if d <= 0 || pid <= 0 {
		return func() {}, nil
	}
	seconds := int(d / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	command := exec.Command("/bin/sh", "-c", fmt.Sprintf("sleep %d && kill -KILL %d", seconds, pid))
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := command.Start(); err != nil {
		return nil, err
	}
	return func() {
		if command.Process != nil {
			_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
			_, _ = command.Process.Wait()
		}
	}, nil
}

func replaceFile(source, destination string) error {
	return os.Rename(source, destination)
}
