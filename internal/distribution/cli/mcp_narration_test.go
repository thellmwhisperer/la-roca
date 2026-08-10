/*
*
@overview MCP installation narration and executable-path regressions. ~120 lines, no public symbols.

	READING GUIDE
	-------------
	1. Start at TestMCPInstallDeclaresTheRunningExecutable
	2. Then read override normalization and the JSON receipt

	MAIN FLOW
	---------
	running test binary -> mcp install -> runtime config plus narrated receipt

	PUBLIC API
	----------
	None; this file tests CLI behavior.

	INTERNALS
	---------
	Executable path, whitespace override, JSON receipt, and shared install helpers

@exports
@deps os/path/filepath/strings/testing
*/
package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// -- 1/4 CORE · TestMCPInstallDeclaresTheRunningExecutable -- <- START HERE

func TestMCPInstallDeclaresTheRunningExecutable(t *testing.T) {
	t.Setenv(EnvExecutable, "")
	path := filepath.Join(t.TempDir(), "claude.json")
	running := runningExecutable(t)
	out, configured := installMCP(t, path)
	if !strings.Contains(configured, running) {
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

// -/ 1/4

// -- 2/4 CORE · TestMCPInstallIgnoresAWhitespaceOnlyOverride --

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

// -/ 2/4

// -- 3/4 CORE · TestMCPInstallJSONNamesTheDeclaredExecutable --

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

// -/ 3/4

// -- 4/4 HELPERS · runningExecutable, installMCP --

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

// -/ 4/4
