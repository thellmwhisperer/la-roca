package vector

import (
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"
)

const (
	workerWatchdogArg            = "_watchdog-sleep"
	workerStallHeartbeat         = ".worker-stall-heartbeat"
	workerStallHeartbeatInterval = time.Second
)

var (
	workerNativeStallTimeout = 10 * time.Minute
	workerStallHeartbeatPath atomic.Value
	workerStallLastHeartbeat atomic.Int64
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
		workerStallLastHeartbeat.Store(time.Now().UnixNano())
	}
	defer func() {
		workerStallHeartbeatPath.Store("")
		workerStallLastHeartbeat.Store(0)
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
	now := time.Now()
	last := workerStallLastHeartbeat.Load()
	if last != 0 && now.Sub(time.Unix(0, last)) < workerStallHeartbeatInterval {
		return
	}
	if !workerStallLastHeartbeat.CompareAndSwap(last, now.UnixNano()) {
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
