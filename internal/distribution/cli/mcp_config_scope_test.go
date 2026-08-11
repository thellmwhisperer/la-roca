package cli

import (
	"strings"
	"testing"
)

// `--config` names ONE runtime's configuration file. Applied to every runtime it
// edited a single file once per runtime, each pass with a different agent's
// rules: one agent's configuration rewritten by another agent's editor.
func TestAConfigPathIsRefusedForMoreThanOneRuntime(t *testing.T) {
	for _, args := range [][]string{
		{"mcp", "uninstall", "--all", "--config", "/tmp/not-read.json"},
		{"mcp", "status", "--config", "/tmp/not-read.json"},
	} {
		err := failingRoot(t, args...)
		if !strings.Contains(err.Error(), "one runtime") {
			t.Errorf("roca %v: the refusal does not say what to do instead: %v", args, err)
		}
	}
}

// Naming the one runtime it belongs to still works.
func TestAConfigPathIsAcceptedForOneNamedRuntime(t *testing.T) {
	out, _ := runRootSplit(t, contractBuild(), nil,
		"mcp", "status", "claude", "--config", "/tmp/not-read.json")
	if !strings.Contains(out, "claude") || !strings.Contains(out, "/tmp/not-read.json") {
		t.Errorf("a single named runtime did not report over the named file:\n%s", out)
	}
}
