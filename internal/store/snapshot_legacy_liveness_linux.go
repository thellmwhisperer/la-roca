//go:build linux

/**
 * @overview Detects live lease-less legacy snapshots through Linux procfs. ~100 lines, no public symbols.
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
 *   legacySnapshotHasOpenHandles, snapshotProcfsVisibility, snapshotTargetWithin
 *   snapshotProcVanished
 *
 * @exports
 * @deps Linux procfs
 */
package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

func legacySnapshotHasOpenHandles(ctx context.Context, directory string) (bool, error) {
	if err := snapshotProcfsVisibility(); err != nil {
		return false, err
	}
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
		if snapshotProcVanished(err) {
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
		if snapshotProcVanished(err) {
			continue
		}
		if err != nil {
			return false, fmt.Errorf("inspect process %s handles: %w", process.Name(), err)
		}
		for _, fd := range fds {
			target, err := os.Readlink(filepath.Join(processRoot, "fd", fd.Name()))
			if snapshotProcVanished(err) {
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

func snapshotProcfsVisibility() error {
	if os.Getenv("container") != "" {
		return fmt.Errorf("procfs visibility is container-scoped")
	}
	for _, marker := range []string{"/.dockerenv", "/run/.containerenv"} {
		if _, err := os.Stat(marker); err == nil {
			return fmt.Errorf("procfs visibility is container-scoped")
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("inspect container marker: %w", err)
		}
	}
	status, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return fmt.Errorf("inspect process namespace: %w", err)
	}
	for _, line := range strings.Split(string(status), "\n") {
		fields := strings.Fields(line)
		if len(fields) > 2 && fields[0] == "NSpid:" {
			return fmt.Errorf("procfs visibility is PID-namespace scoped")
		}
	}
	cgroup, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return fmt.Errorf("inspect process cgroup: %w", err)
	}
	lower := strings.ToLower(string(cgroup))
	for _, marker := range []string{"docker", "containerd", "kubepods", "libpod", "lxc"} {
		if strings.Contains(lower, marker) {
			return fmt.Errorf("procfs visibility is container-scoped")
		}
	}
	return nil
}

func snapshotTargetWithin(directory, target string) bool {
	cleanDirectory := filepath.Clean(directory)
	cleanTarget := filepath.Clean(target)
	return cleanTarget == cleanDirectory || strings.HasPrefix(cleanTarget, cleanDirectory+string(os.PathSeparator))
}

// snapshotProcVanished reports whether a procfs read failed because the process
// or file descriptor being inspected no longer exists. ENOENT and ESRCH both
// mean the process is gone and can no longer hold a snapshot open, so the scan
// may skip it instead of aborting the whole probe.
func snapshotProcVanished(err error) bool {
	return os.IsNotExist(err) || errors.Is(err, syscall.ESRCH)
}
