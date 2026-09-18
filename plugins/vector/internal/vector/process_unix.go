//go:build !windows

package vector

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"time"
)

func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// armWorkerStallWatchdog starts a sibling process that can terminate this
// indexing worker if native work stays mute. The sibling records pid and start
// identity so a reused PID is never signalled, writes a diagnostic, then sends
// SIGTERM. It is not used for queries or residents.
func armWorkerStallWatchdog(d time.Duration, pid int, logPath string) (func(), error) {
	if d <= 0 || pid <= 0 {
		return func() {}, nil
	}
	identity, err := processStartIdentity(pid)
	if err != nil {
		return nil, err
	}
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	seconds := int(d / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	command := exec.Command(self, workerWatchdogArg, strconv.Itoa(seconds), strconv.Itoa(pid), identity, logPath)
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := command.Start(); err != nil {
		return nil, err
	}
	return func() {
		if command.Process != nil {
			_ = syscall.Kill(-command.Process.Pid, syscall.SIGTERM)
			_, _ = command.Process.Wait()
		}
	}, nil
}

func RunWatchdogSleepIfRequested(args []string) bool {
	if len(args) < 5 || args[1] != workerWatchdogArg {
		return false
	}
	seconds, err := strconv.Atoi(args[2])
	if err != nil || seconds < 1 {
		return true
	}
	pid, err := strconv.Atoi(args[3])
	if err != nil || pid <= 0 {
		return true
	}
	identity := args[4]
	logPath := ""
	if len(args) > 5 {
		logPath = args[5]
	}
	time.Sleep(time.Duration(seconds) * time.Second)
	current, err := processStartIdentity(pid)
	if err != nil || current != identity {
		return true
	}
	message := fmt.Sprintf("semantic search stalled: indexing worker pid %d made no progress for %ds", pid, seconds)
	fmt.Fprintln(os.Stderr, message)
	if logPath != "" {
		if file, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
			_, _ = fmt.Fprintln(file, message)
			_ = file.Close()
		}
	}
	_ = syscall.Kill(pid, syscall.SIGTERM)
	return true
}

func replaceFile(source, destination string) error {
	return os.Rename(source, destination)
}
