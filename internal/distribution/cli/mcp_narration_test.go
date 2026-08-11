package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestMCPInstallDeclaresTheRunningExecutable(t *testing.T) {
	t.Setenv(EnvExecutable, "")
	path := filepath.Join(t.TempDir(), "claude.json")
	running := runningExecutable(t)
	out, configured := installMCP(t, path)
	// The configuration is JSON, so the path is compared in its encoded form: a
	// path carrying a backslash or a quote is escaped on the way in, and a raw
	// comparison would fail for a reason that is not the product's.
	if !strings.Contains(configured, strconv.Quote(running)) {
		t.Fatalf("configuration does not name the running executable %q:\n%s",
			running, configured)
	}
	for _, want := range []string{
		"wrote MCP server \"roca\"",
		path,
		"command: " + running + " mcp serve",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("receipt does not carry %q:\n%s", want, out)
		}
	}
}

func TestMCPInstallIgnoresAWhitespaceOnlyOverride(t *testing.T) {
	t.Setenv(EnvExecutable, "   ")
	path := filepath.Join(t.TempDir(), "claude.json")
	running := runningExecutable(t)
	_, configured := installMCP(t, path)
	if !strings.Contains(configured, running) {
		t.Fatalf("whitespace override displaced the running executable %q:\n%s",
			running, configured)
	}
}

func TestMCPInstallJSONNamesTheDeclaredExecutable(t *testing.T) {
	t.Setenv(EnvExecutable, "")
	path := filepath.Join(t.TempDir(), "claude.json")
	running := runningExecutable(t)

	out := runRoot(t, Build{Version: "test"}, "--json", "mcp", "install", "claude",
		"--config", path)
	var receipt struct {
		Executable string `json:"executable"`
	}
	if err := json.Unmarshal([]byte(out), &receipt); err != nil {
		t.Fatalf("decode receipt: %v\n%s", err, out)
	}
	if receipt.Executable != running {
		t.Fatalf("receipt executable = %q, want %q", receipt.Executable, running)
	}
}

func runningExecutable(t *testing.T) string {
	t.Helper()
	running, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	running, err = filepath.Abs(running)
	if err != nil {
		t.Fatal(err)
	}
	return running
}

func installMCP(t *testing.T, path string) (string, string) {
	t.Helper()
	out, err := runRootErr(t, Build{Version: "test"}, nil,
		"mcp", "install", "claude", "--config", path)
	if err != nil {
		t.Fatalf("mcp install: %v\n%s", err, out)
	}
	configured, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return out, string(configured)
}
