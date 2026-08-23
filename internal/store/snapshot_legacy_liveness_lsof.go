//go:build darwin || freebsd || openbsd || netbsd || dragonfly || solaris

/**
 * @overview Detects live lease-less legacy snapshots through lsof. ~80 lines, no public symbols.
 *
 *   READING GUIDE
 *   -------------
 *   1. Start at legacySnapshotHasOpenHandles <- runs the platform handle probe
 *   2. snapshotLsofTargetWithin             <- validates reported paths
 *
 *   MAIN FLOW
 *   ---------
 *   legacy snapshot -> recursive lsof probe -> preserve if any handle is inside
 *
 *   PUBLIC API
 *   ----------
 *   None.
 *
 *   INTERNALS
 *   ---------
 *   legacySnapshotHasOpenHandles, snapshotLsofTargetWithin
 *
 * @exports
 * @deps lsof executable
 */
package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func legacySnapshotHasOpenHandles(ctx context.Context, directory string) (bool, error) {
	resolvedDirectory, err := filepath.EvalSymlinks(directory)
	if err != nil {
		return false, fmt.Errorf("resolve legacy snapshot directory: %w", err)
	}
	executable := "/usr/sbin/lsof"
	if _, err := os.Stat(executable); err != nil {
		var lookErr error
		executable, lookErr = exec.LookPath("lsof")
		if lookErr != nil {
			return false, fmt.Errorf("find legacy snapshot handle probe: %w", lookErr)
		}
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command := exec.CommandContext(ctx, executable, "-n", "-P", "-Fn", "+D", resolvedDirectory)
	command.Stdout = &stdout
	command.Stderr = &stderr
	err = command.Run()
	for _, line := range strings.Split(stdout.String(), "\n") {
		if strings.HasPrefix(line, "n") &&
			snapshotLsofTargetWithin(resolvedDirectory, strings.TrimPrefix(line, "n")) {
			return true, nil
		}
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return false, ctxErr
	}
	if err == nil {
		return false, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 && stderr.Len() == 0 {
		return false, nil
	}
	return false, fmt.Errorf("probe legacy snapshot handles: %w: %s", err, strings.TrimSpace(stderr.String()))
}

func snapshotLsofTargetWithin(directory, target string) bool {
	cleanDirectory := filepath.Clean(directory)
	cleanTarget := filepath.Clean(target)
	return cleanTarget == cleanDirectory || strings.HasPrefix(cleanTarget, cleanDirectory+string(os.PathSeparator))
}
