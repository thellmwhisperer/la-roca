//go:build !darwin && !linux && !windows

package store

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
)

func snapshotFileIdentity(path string, _ os.FileInfo) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", digest.Sum(nil)), nil
}
