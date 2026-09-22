//go:build !windows

package securefile

import "os"

// lockDirectory gives conditional publication one stable lock without
// materializing a lock file beside every operator-owned artifact.
func lockDirectory(path string) (func() error, error) {
	directory, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	return lockUnixFile(directory)
}
