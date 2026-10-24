package securefile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReplacePreservesEditLandingBeforePublication(t *testing.T) {
	for name, edit := range map[string]func(t *testing.T, path string){
		"in place": func(t *testing.T, path string) {
			if err := os.WriteFile(path, []byte("concurrent"), 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"renamed over": func(t *testing.T, path string) {
			other := filepath.Join(filepath.Dir(path), "other")
			if err := os.WriteFile(other, []byte("concurrent"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(other, path); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			path := replaceFixture(t, func(real func(string, string) error, staged, target string) error {
				edit(t, target) // after Replace checked previous, before the exchange
				return real(staged, target)
			})

			err := Replace(path, []byte("replacement"), []byte("previous"))
			if err == nil || !strings.Contains(err.Error(), "changed while it was being edited") {
				t.Fatalf("Replace error = %v, want concurrent-change refusal", err)
			}
			assertFileContentAndMode(t, path, []byte("concurrent"), 0o600)
			assertOnlyLiveFile(t, path)
		})
	}
}

func TestReplaceReturnsThePublishedIdentity(t *testing.T) {
	var staged os.FileInfo
	path := replaceFixture(t, func(real func(string, string) error, from, target string) error {
		var err error
		if staged, err = os.Lstat(from); err != nil {
			t.Fatal(err)
		}
		if err := real(from, target); err != nil {
			return err
		}
		// A later writer replaces our publication before Replace returns.
		later := filepath.Join(filepath.Dir(target), "later")
		if err := os.WriteFile(later, []byte("later"), 0o600); err != nil {
			t.Fatal(err)
		}
		return os.Rename(later, target)
	})

	published, err := replace(path, []byte("replacement"), []byte("previous"), nil, 0o600)
	if err != nil {
		t.Fatalf("replace: %v", err)
	}
	live, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(published, staged) || os.SameFile(published, live) {
		t.Fatal("published identity is not the inode this call exchanged in")
	}
}

func TestReplaceLeavesACompleteFileAtEveryCrashPoint(t *testing.T) {
	exchanged := false
	path := replaceFixture(t, func(real func(string, string) error, staged, target string) error {
		exchanged = true
		assertFileContentAndMode(t, target, []byte("previous"), 0o600) // crash before
		assertFileContentAndMode(t, staged, []byte("replacement"), 0o600)
		if err := real(staged, target); err != nil {
			return err
		}
		assertFileContentAndMode(t, target, []byte("replacement"), 0o600) // crash after
		if strings.HasPrefix(filepath.Base(staged), ".") {
			t.Fatalf("displaced file is hidden at %s", staged)
		}
		assertFileContentAndMode(t, staged, []byte("previous"), 0o600)
		return nil
	})

	if err := Replace(path, []byte("replacement"), []byte("previous")); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	if !exchanged {
		t.Fatal("Replace published without the atomic exchange")
	}
	assertFileContentAndMode(t, path, []byte("replacement"), 0o600)
	assertOnlyLiveFile(t, path)
}

func TestReplaceRefusesWithoutAtomicExchange(t *testing.T) {
	path := replaceFixture(t, func(_ func(string, string) error, _, _ string) error {
		return errAtomicExchangeUnsupported
	})

	err := Replace(path, []byte("replacement"), []byte("previous"))
	if err == nil || !strings.Contains(err.Error(), "atomic exchange publication is unsupported") {
		t.Fatalf("Replace error = %v, want unsupported-exchange refusal", err)
	}
	assertFileContentAndMode(t, path, []byte("previous"), 0o600)
	assertOnlyLiveFile(t, path)
}

// replaceFixture writes "previous" to a fresh path and routes only the first
// exchange through hook, so a swap-back runs the real primitive.
func replaceFixture(t *testing.T, hook func(real func(string, string) error, staged, target string) error) string {
	t.Helper()
	real := exchangeFile
	t.Cleanup(func() { exchangeFile = real })
	exchangeFile = func(staged, target string) error {
		exchangeFile = real
		return hook(real, staged, target)
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte("previous"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func assertOnlyLiveFile(t *testing.T, path string) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != filepath.Base(path) {
		t.Fatalf("directory holds %v, want only %s", entries, filepath.Base(path))
	}
}
