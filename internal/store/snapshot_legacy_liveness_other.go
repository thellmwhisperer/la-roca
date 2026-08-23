//go:build !linux && !windows && !darwin && !freebsd && !openbsd && !netbsd && !dragonfly && !solaris

package store

import "context"

func legacySnapshotHasOpenHandles(context.Context, string) (bool, error) {
	return true, nil
}
