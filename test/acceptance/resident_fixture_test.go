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
	dir, err := acceptanceTempDir("resident-")
	if err != nil {
		return "", nil, err
	}
	cwd, err := os.Getwd()
	if err != nil {
		_ = os.RemoveAll(dir)
		return "", nil, err
	}
	socket, err := filepath.Rel(cwd, filepath.Join(dir, "socket", "resident.sock"))
	if err != nil {
		_ = os.RemoveAll(dir)
		return "", nil, err
	}
	return socket, func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		raw, err := resident.Call(ctx, resident.Options{Socket: socket}, "status", nil)
		var status resident.Status
		if err == nil && json.Unmarshal(raw, &status) == nil && status.PID > 0 {
			if process, err := os.FindProcess(status.PID); err == nil {
				_ = process.Kill()
			}
		}
		_ = os.RemoveAll(dir)
	}, nil
}
