package vector

import (
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"
)

const (
	workerWatchdogArg     = "_watchdog-sleep"
	workerStallHeartbeat  = ".worker-stall-heartbeat"
)

var (
	workerNativeStallTimeout = 10 * time.Minute
	workerStallHeartbeatPath atomic.Value
)

func watchWorkerNativeCall(stateDir string, fn func() error) error {
	heartbeat := ""
	if stateDir != "" {
		heartbeat = filepath.Join(stateDir, workerStallHeartbeat)
		if err := touchWorkerStallHeartbeat(heartbeat); err != nil {
			return fmt.Errorf("arm worker stall watchdog: %w", err)
		}
	}
	stop, err := armWorkerStallWatchdog(workerNativeStallTimeout, os.Getpid(), workerLogPath(stateDir), heartbeat)
	if err != nil {
		return fmt.Errorf("arm worker stall watchdog: %w", err)
	}
	if heartbeat != "" {
		workerStallHeartbeatPath.Store(heartbeat)
	}
	defer func() {
		workerStallHeartbeatPath.Store("")
		stop()
		if heartbeat != "" {
			_ = os.Remove(heartbeat)
		}
	}()
	return fn()
}

func resetWorkerStallWatchdog() {
	path, _ := workerStallHeartbeatPath.Load().(string)
	if path == "" {
		return
	}
	_ = touchWorkerStallHeartbeat(path)
}

func touchWorkerStallHeartbeat(path string) error {
	now := time.Now()
	if err := os.Chtimes(path, now, now); err == nil {
		return nil
	}
	return os.WriteFile(path, []byte(now.UTC().Format(time.RFC3339Nano)+"\n"), 0o600)
}

func workerLogPath(stateDir string) string {
	if stateDir == "" {
		return ""
	}
	return filepath.Join(stateDir, WorkerLogFilename)
}
