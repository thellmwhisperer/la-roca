//go:build !windows

package rocavector_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/distribution/rocavector"
	"golang.org/x/sys/unix"
)

func TestBundledVectorRefusesActiveStateLocks(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		lockPath  func(string) string
		wantError string
	}{
		{name: "relocation", wantError: "vector state is active",
			lockPath: rocavector.RelocationLockPath},
		{name: "index", wantError: "vector index is active", lockPath: func(root string) string {
			return filepath.Join(root, rocavector.LegacyName, rocavector.StateDir, "vector.db.index.lock")
		}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			root, bin := filepath.Join(t.TempDir(), "plugins"), filepath.Join(t.TempDir(), "bin")
			plantLegacyVector(t, root, bin, "v1", []byte("vector one"))
			path := testCase.lockPath(root)
			file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
				t.Fatal(err)
			}
			defer unix.Flock(int(file.Fd()), unix.LOCK_UN)

			_, err = rocavector.EnsureWithPayload(root, bin, "v2", []byte("vector two"))
			if err == nil || !strings.Contains(err.Error(), testCase.wantError) {
				t.Fatalf("ensure error = %v, want %q", err, testCase.wantError)
			}
			assertDirExists(t, filepath.Join(root, rocavector.LegacyName), true)
			assertDirExists(t, filepath.Join(root, rocavector.Name), false)
		})
	}
}
