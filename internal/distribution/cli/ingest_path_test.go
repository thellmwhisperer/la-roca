package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIngestAcceptsAtMostOneExportDirectory(t *testing.T) {
	command := ingestCommand(&cliEnv{})
	if err := command.Args(command, nil); err != nil {
		t.Fatalf("plain nightly ingest: %v", err)
	}
	export := t.TempDir()
	if err := command.Args(command, []string{export}); err != nil {
		t.Fatalf("one-shot export ingest: %v", err)
	}
	if err := command.Args(command, []string{export, export}); err == nil {
		t.Fatal("two export paths were accepted")
	}
}

func TestIngestRefusesAnUnavailableExportPathBeforeOpeningTheDatabase(t *testing.T) {
	command := ingestCommand(&cliEnv{})
	file := filepath.Join(t.TempDir(), "export.zip")
	if err := os.WriteFile(file, []byte("synthetic"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(t.TempDir(), "missing"), file} {
		err := command.Args(command, []string{path})
		if err == nil || !strings.Contains(err.Error(), path) {
			t.Errorf("path %q: error = %v", path, err)
		}
	}
}
