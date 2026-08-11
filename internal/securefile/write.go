// Package securefile owns the small file primitives used for operator secrets.
package securefile

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// Write replaces path atomically after the new bytes and permissions are durable.
func Write(path string, data []byte, mode, dirMode os.FileMode) (err error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return err
	}
	if err := os.Chmod(dir, dirMode); err != nil {
		return fmt.Errorf("restrict directory permissions: %w", err)
	}
	temporary, err := os.CreateTemp(dir, "."+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	staged := temporary.Name()
	defer func() {
		temporary.Close()
		if err != nil {
			os.Remove(staged)
		}
	}()
	if err = temporary.Chmod(mode); err != nil {
		return err
	}
	if _, err = temporary.Write(data); err != nil {
		return err
	}
	if err = temporary.Sync(); err != nil {
		return err
	}
	if err = temporary.Close(); err != nil {
		return err
	}
	if err = os.Rename(staged, path); err != nil {
		return err
	}
	if err = os.Chmod(path, mode); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		return nil
	}
	directory, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
