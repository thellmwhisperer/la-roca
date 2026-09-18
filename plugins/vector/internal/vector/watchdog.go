package vector

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const workerWatchdogArg = "_watchdog-sleep"

var workerNativeStallTimeout = 10 * time.Minute

func watchWorkerNativeCall(stateDir string, fn func() error) error {
	stop, err := armWorkerStallWatchdog(workerNativeStallTimeout, os.Getpid(), workerLogPath(stateDir))
	if err != nil {
		return fmt.Errorf("arm worker stall watchdog: %w", err)
	}
	defer stop()
	return fn()
}

func workerLogPath(stateDir string) string {
	if stateDir == "" {
		return ""
	}
	return filepath.Join(stateDir, WorkerLogFilename)
}
