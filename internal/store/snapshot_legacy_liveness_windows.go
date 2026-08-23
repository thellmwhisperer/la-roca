//go:build windows

/**
 * @overview Detects live lease-less legacy snapshots through Windows sharing checks. ~75 lines, no public symbols.
 *
 *   READING GUIDE
 *   -------------
 *   1. Start at legacySnapshotHasOpenHandles <- probes each legacy artifact
 *
 *   MAIN FLOW
 *   ---------
 *   legacy snapshot -> exclusive file opens -> preserve on sharing violation
 *
 *   PUBLIC API
 *   ----------
 *   None.
 *
 *   INTERNALS
 *   ---------
 *   legacySnapshotHasOpenHandles
 *
 * @exports
 * @deps golang.org/x/sys/windows
 */
package store

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"

	"golang.org/x/sys/windows"
)

func legacySnapshotHasOpenHandles(ctx context.Context, directory string) (bool, error) {
	live := false
	err := filepath.WalkDir(directory, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		name, err := windows.UTF16PtrFromString(path)
		if err != nil {
			return err
		}
		handle, err := windows.CreateFile(name, windows.GENERIC_READ, 0, nil,
			windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
		if err == nil {
			return windows.CloseHandle(handle)
		}
		if errors.Is(err, windows.ERROR_SHARING_VIOLATION) || errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			live = true
			return fs.SkipAll
		}
		return fmt.Errorf("probe legacy snapshot file %q: %w", path, err)
	})
	return live, err
}
