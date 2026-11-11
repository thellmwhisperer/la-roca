//go:build acceptance

package acceptance

import (
	"strings"
	"testing"
)

func TestRealBinaryRejectsInvalidCLIArguments(t *testing.T) {
	binary, err := rocaBinary()
	if err != nil {
		t.Fatal(err)
	}
	home, world := initializedDistributionHome(t, "roca-cli-validation-", binary)
	for _, command := range []string{
		"", "version", "doctor", "init", "migrate", "schema", "schema status", "index",
		"store", "health", "memory", "layers", "pill", "handoff", "handoff latest",
		"mcp", "mcp serve", "skill", "hooks", "update", "uninstall", "plugins", "plugin",
		"remote", "remote list", "remote vector", "_artifacts", "_capabilities", "_database-scope",
		"_vector-reader", "_install-bundled-plugins",
	} {
		t.Run(command, func(t *testing.T) {
			args := append(strings.Fields(command), "stray-argument")
			result := world.runAt(home, binary, args...)
			if result.code == 0 || result.stdout != "" || !strings.Contains(result.stderr, `unknown command "stray-argument"`) {
				t.Fatalf("stray argument: %+v", result)
			}
		})
	}
	for _, command := range []string{"memory", "mcp", "remote", "remote vector"} {
		result := world.runAt(home, binary, strings.Fields(command)...)
		if result.code != 0 || !strings.Contains(result.stdout, "Available Commands:") {
			t.Fatalf("group help for %s: %+v", command, result)
		}
	}
	health := world.runAt(home, binary, "health")
	if health.code != 0 || !strings.HasPrefix(health.stdout, "health: ") {
		t.Fatalf("health without arguments: %+v", health)
	}
	for _, tc := range []struct {
		args    []string
		message string
	}{
		{[]string{"quer"}, "Did you mean this?\n\tquery"},
		{[]string{"qury"}, "Did you mean this?\n\tquery"},
		{[]string{"--json", "quer", "--help"}, "Did you mean this?\n\tquery"},
		{[]string{"stray-argument", "--help"}, `unknown command "stray-argument"`},
		{[]string{"--json", "stray-argument", "--help"}, `unknown command "stray-argument"`},
		{[]string{"exec"}, "requires at least 1 arg(s)"},
		{[]string{"exec", "SELECT 1", "--max-chars", "-1"}, "--max-chars must be zero or greater"},
		{[]string{"query", "orchard", "--top", "-1"}, "--top must be zero or greater"},
		{[]string{"query", "orchard", "--databases", " "}, "empty database name in --databases"},
		{[]string{"handoff", "latest", "--limit", "-1"}, "--limit must be zero or greater"},
		{[]string{"query", "orchard", "--project", " \t"}, "--project must not be empty or whitespace"},
		{[]string{"query", "orchard", "--databases", "ops,,corpus"}, "empty database name in --databases"},
		{[]string{"store", "--layer", "discovery", "--content", "orchard", "stray-argument"}, `unknown command "stray-argument"`},
		{[]string{"store", "--layer", "discovery", "--content", "orchard", "--metadata", "null"}, "metadata"},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			result := world.runAt(home, binary, tc.args...)
			if result.code == 0 || !strings.Contains(result.stderr, tc.message) {
				t.Fatalf("invalid flags: %+v", result)
			}
		})
	}
}
