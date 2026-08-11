//go:build darwin || linux

package provider

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestRunLocalCommandCleansProcessGroupAfterLeaderExit(t *testing.T) {
	cmd := exec.CommandContext(context.Background(), "sh", "-c", `sleep 30 & printf '%d' "$$"`)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.WaitDelay = 20 * time.Millisecond
	_ = runLocalCommand(cmd)
	groupID, err := strconv.Atoi(strings.TrimSpace(stdout.String()))
	if err != nil {
		t.Fatalf("process group ID %q: %v", stdout.String(), err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		err = syscall.Kill(-groupID, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("process group %d survived command exit: %v", groupID, err)
}
