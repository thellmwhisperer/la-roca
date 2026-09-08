//go:build acceptance

package acceptance

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestMigrateReadCounter(t *testing.T) {
	root, err := acceptanceRoot()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "cached-payload")
	const size = 2 * 1024 * 1024
	if err := os.WriteFile(path, make([]byte, size), 0600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		args []string
		code int
	}{
		{"read", []string{"/bin/cat", path, path}, 0},
		{"cached-read", []string{"/bin/cat", path, path}, 0},
		{"failure", []string{"/bin/sh", "-c", "echo synthetic-counter-error >&2; exit 7"}, 7},
	} {
		t.Run(test.name, func(t *testing.T) {
			args := append([]string{filepath.Join(root, "testdata", "migrate-cost", "measure-linux.py")}, test.args...)
			cmd := exec.Command("python3", args...)
			raw, err := cmd.Output()
			if cmd.ProcessState == nil || cmd.ProcessState.ExitCode() != test.code {
				t.Fatalf("counter exit: %v: %s", err, raw)
			}
			if test.code != 0 && string(err.(*exec.ExitError).Stderr) != "synthetic-counter-error\n" {
				t.Fatalf("counter hid child diagnostics: %v", err)
			}
			var result migrateCost
			if err := json.Unmarshal(raw, &result); err != nil {
				t.Fatal(err)
			}
			if result.ElapsedNS <= 0 || (test.code == 0 && (result.BytesRead < 2*size || result.BytesRead > 2*size+64*1024)) {
				t.Fatalf("counter lost cached reads or included unrelated work: %+v", result)
			}
		})
	}
}
