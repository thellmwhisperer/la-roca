//go:build darwin || linux

package provider

import (
	"os"
	"os/exec"
	"syscall"
)

func runLocalCommand(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return killLocalProcessGroup(cmd)
	}
	defer func() { _ = killLocalProcessGroup(cmd) }()
	return cmd.Run()
}

func killLocalProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return os.ErrProcessDone
	}
	return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
