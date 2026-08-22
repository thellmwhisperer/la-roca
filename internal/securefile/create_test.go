package securefile

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCreatePreservingParentModePublishesCompleteFile(t *testing.T) {
	dir, path, realRename := preservingParentModeFixture(t)
	want := []byte("[features]\nplugins = true\n")
	renameNoReplaceFile = func(staged, target string) error {
		if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("target visible before publication: %v", err)
		}
		got, err := os.ReadFile(staged)
		if err != nil {
			t.Fatalf("read staged file: %v", err)
		}
		if string(got) != string(want) {
			t.Fatalf("staged content = %q, want %q", got, want)
		}
		return realRename(staged, target)
	}

	if err := CreatePreservingParentMode(path, want, 0o600, 0o700); err != nil {
		t.Fatalf("create: %v", err)
	}
	assertFileContentAndMode(t, path, want, 0o600)
	assertMode(t, dir, 0o750)
}

func TestCreatePreservingParentModePreservesConcurrentTarget(t *testing.T) {
	dir, path, realRename := preservingParentModeFixture(t)
	existing := []byte("operator configuration")
	renameNoReplaceFile = func(staged, target string) error {
		if err := os.WriteFile(target, existing, 0o640); err != nil {
			t.Fatalf("create concurrent target: %v", err)
		}
		return realRename(staged, target)
	}

	err := CreatePreservingParentMode(path, []byte("replacement"), 0o600, 0o700)
	if err == nil || !strings.Contains(err.Error(), "existing file was preserved") {
		t.Fatalf("create error = %v, want preserved-file collision", err)
	}
	assertFileContentAndMode(t, path, existing, 0o640)
	assertMode(t, dir, 0o750)
}

func TestCreatePreservingParentModeFailsWithoutAtomicPrimitive(t *testing.T) {
	realRename := renameNoReplaceFile
	renameNoReplaceFile = func(_, _ string) error {
		return errAtomicNoReplaceUnsupported
	}
	t.Cleanup(func() {
		renameNoReplaceFile = realRename
	})

	path := filepath.Join(t.TempDir(), "config.toml")
	err := CreatePreservingParentMode(path, []byte("complete"), 0o600, 0o700)
	if err == nil || !strings.Contains(err.Error(), "atomic no-replace publication is unsupported") {
		t.Fatalf("create error = %v, want unsupported-publication error", err)
	}
	if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("target exists after unsupported publication: %v", statErr)
	}
}

func preservingParentModeFixture(t *testing.T) (string, string, func(string, string) error) {
	t.Helper()
	realRename := renameNoReplaceFile
	t.Cleanup(func() {
		renameNoReplaceFile = realRename
	})
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o750); err != nil {
		t.Fatalf("set parent mode: %v", err)
	}
	return dir, filepath.Join(dir, "config.toml"), realRename
}

func assertFileContentAndMode(t *testing.T, path string, want []byte, mode os.FileMode) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(got) != string(want) {
		t.Fatalf("content = %q, want %q", got, want)
	}
	assertMode(t, path, mode)
}

func assertMode(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if runtime.GOOS == "windows" {
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != mode {
		t.Fatalf("mode = %o, want %o", got, mode)
	}
}
