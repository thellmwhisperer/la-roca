package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPackageContainsOnlyExecutableManifestAndChecksums(t *testing.T) {
	directory := t.TempDir()
	binary := filepath.Join(directory, "input")
	if err := os.WriteFile(binary, []byte("synthetic executable"), 0o700); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(directory, "package")
	if err := packagePlugin(binary, out, "1.2.3"); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("package files = %v", entries)
	}
	raw, err := os.ReadFile(filepath.Join(out, "plugin.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Kind     string `json:"kind"`
		StateDir string `json:"state_directory"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Kind != "executable" || manifest.StateDir != "state" {
		t.Fatalf("plugin manifest = %+v", manifest)
	}
	executable := "roca-vector"
	if runtime.GOOS == "windows" {
		executable += ".exe"
	}
	checksums, err := os.ReadFile(filepath.Join(out, "checksums.txt"))
	if err != nil || !strings.Contains(string(checksums), executable) ||
		!strings.Contains(string(checksums), "plugin.json") {
		t.Fatalf("checksums = %q, err=%v", checksums, err)
	}
}
