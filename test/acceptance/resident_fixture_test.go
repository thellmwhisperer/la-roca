//go:build acceptance

package acceptance

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/thellmwhisperer/la-roca/pkg/resident"
)

// Keep socket addresses short even when the checkout has a long absolute path.
// CLI children and their resident inherit the acceptance process's directory.
func acceptanceResident() (string, func(), error) {
	dir, socket, err := shortSocket("resident-", filepath.Join("socket", "resident.sock"))
	if err != nil {
		return "", nil, err
	}
	return socket, func() {
		status, err := acceptanceResidentStatus(socket)
		if err == nil && status.PID > 0 {
			if process, err := os.FindProcess(status.PID); err == nil {
				_ = process.Kill()
			}
		}
		_ = os.RemoveAll(dir)
	}, nil
}

func acceptanceResidentStatus(socket string) (resident.Status, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	raw, err := resident.Call(ctx, resident.Options{Socket: socket}, "status", nil)
	var status resident.Status
	if err == nil {
		err = json.Unmarshal(raw, &status)
	}
	return status, err
}

// shortSocket is a fresh private directory and the path of name inside it,
// relative to the suite's directory.
func shortSocket(prefix, name string) (dir, socket string, err error) {
	dir, err = acceptanceTempDir(prefix)
	if err != nil {
		return "", "", err
	}
	cwd, err := os.Getwd()
	if err == nil {
		socket, err = filepath.Rel(cwd, filepath.Join(dir, name))
	}
	if err != nil {
		_ = os.RemoveAll(dir)
		return "", "", err
	}
	return dir, socket, nil
}
