package securefile

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCreatePreservingParentModeFallsBackWithoutReplacing(t *testing.T) {
	originalLinkFile := linkFile
	linkFile = func(_, _ string) error {
		return errors.ErrUnsupported
	}
	t.Cleanup(func() {
		linkFile = originalLinkFile
	})

	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	want := []byte("[features]\nplugins = true\n")
	if err := CreatePreservingParentMode(path, want, 0o600, 0o700); err != nil {
		t.Fatalf("create with hard-link fallback: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read created file: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("created content = %q, want %q", got, want)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat created file: %v", err)
	}
	if gotMode := info.Mode().Perm(); gotMode != 0o600 {
		t.Fatalf("created mode = %o, want 600", gotMode)
	}

	err = CreatePreservingParentMode(path, []byte("replacement"), 0o600, 0o700)
	if err == nil || !strings.Contains(err.Error(), "existing file was preserved") {
		t.Fatalf("second create error = %v, want preserved-file collision", err)
	}
	got, err = os.ReadFile(path)
	if err != nil {
		t.Fatalf("read preserved file: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("preserved content = %q, want %q", got, want)
	}
}
