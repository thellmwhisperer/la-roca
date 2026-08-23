//go:build linux

/**
 * @overview Detects live lease-less legacy snapshots through Linux procfs. ~85 lines, no public symbols.
 *
 *   READING GUIDE
 *   -------------
 *   1. Start at legacySnapshotHasOpenHandles <- scans this user's processes
 *   2. snapshotTargetWithin                <- recognizes snapshot file handles
 *
 *   MAIN FLOW
 *   ---------
 *   legacy snapshot -> same-user /proc fds -> preserve if any target is inside
 *
 *   PUBLIC API
 *   ----------
 *   None.
 *
 *   INTERNALS
 *   ---------
 *   legacySnapshotHasOpenHandles, snapshotTargetWithin
 *
 * @exports
 * @deps Linux procfs
 */
package store

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

func legacySnapshotHasOpenHandles(ctx context.Context, directory string) (bool, error) {
	resolvedDirectory, err := filepath.EvalSymlinks(directory)
	if err != nil {
		return false, fmt.Errorf("resolve legacy snapshot directory: %w", err)
	}
	directory = resolvedDirectory
	processes, err := os.ReadDir("/proc")
	if err != nil {
		return false, fmt.Errorf("inspect procfs: %w", err)
	}
	owner := uint32(os.Geteuid())
	for _, process := range processes {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if !process.IsDir() {
			continue
		}
		if _, err := strconv.Atoi(process.Name()); err != nil {
			continue
		}
		processRoot := filepath.Join("/proc", process.Name())
		info, err := os.Stat(processRoot)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return false, fmt.Errorf("inspect process %s: %w", process.Name(), err)
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != owner {
			continue
		}
		fds, err := os.ReadDir(filepath.Join(processRoot, "fd"))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return false, fmt.Errorf("inspect process %s handles: %w", process.Name(), err)
		}
		for _, fd := range fds {
			target, err := os.Readlink(filepath.Join(processRoot, "fd", fd.Name()))
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				return false, fmt.Errorf("inspect process %s handle %s: %w", process.Name(), fd.Name(), err)
			}
			if snapshotTargetWithin(directory, strings.TrimSuffix(target, " (deleted)")) {
				return true, nil
			}
		}
	}
	return false, nil
}

func snapshotTargetWithin(directory, target string) bool {
	cleanDirectory := filepath.Clean(directory)
	cleanTarget := filepath.Clean(target)
	return cleanTarget == cleanDirectory || strings.HasPrefix(cleanTarget, cleanDirectory+string(os.PathSeparator))
}
