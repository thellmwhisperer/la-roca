package agentcfg_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/distribution/agentcfg"
)

// Each supported runtime has a synthetic configuration in its own format. A
// fixture copied from a real
// machine would carry that machine's vocabulary into a public repository.
//
// Every fixture has content of its own before Roca arrives, because that is the
// contract: installing preserves what was already there.
var fixtures = map[string]string{
	agentcfg.RuntimeCodex: `# The operator's Codex configuration
model = "gpt-5-codex"
approval_policy = "on-request"

[mcp_servers.some-other-server]
command = "other-binary"
args = ["--stdio"]

# A trailing comment nobody should lose
`,
	agentcfg.RuntimeClaude: `{
  "numStartups": 42,
  "mcpServers": {
    "some-other-server": {
      "type": "stdio",
      "command": "other-binary"
    }
  },
  "theme": "dark"
}
`,
	agentcfg.RuntimeClaudeDesktop: `{
  "numStartups": 42,
  "mcpServers": {
    "some-other-server": {
      "type": "stdio",
      "command": "other-binary"
    }
  },
  "theme": "dark"
}
`,
	agentcfg.RuntimeOpencode: `{
  // OpenCode reads JSONC, so this comment has to survive
  "$schema": "https://opencode.ai/config.json",
  "mcp": {
    "some-other-server": {
      "type": "local",
      "command": ["other-binary"],
      "enabled": true
    }
  }
}
`,
	agentcfg.RuntimeHermes: `# Hermes configuration
runtime: hermes
mcp_servers:
  some-other-server:
    command: other-binary
    args:
      - --stdio

logging: verbose
`,
	agentcfg.RuntimePi: `{
  "mcpServers": {
    "some-other-server": {
      "command": "other-binary"
    }
  }
}
`,
	agentcfg.RuntimeZcode: `{
  "theme": "dark",
  "mcp": {
    "servers": {
      "some-other-server": {
        "type": "stdio",
        "command": "other-binary"
      }
    }
  }
}
`,
}

func TestInstallDeclaresTheStdioServerInEveryRuntime(t *testing.T) {
	for _, runtime := range agentcfg.Runtimes() {
		t.Run(runtime, func(t *testing.T) {
			path := fixtureFile(t, runtime)

			outcome, err := agentcfg.Install(runtime, path, "roca")
			if err != nil {
				t.Fatalf("Install: %v", err)
			}
			if !outcome.Changed {
				t.Error("installing over a config with no Roca changed nothing")
			}
			if outcome.Backup == "" {
				t.Error("no backup of the previous file was left")
			}
			if _, err := os.Stat(outcome.Backup); err != nil {
				t.Errorf("the declared backup does not exist: %v", err)
			}

			status, err := agentcfg.Status(runtime, path)
			if err != nil {
				t.Fatalf("Status: %v", err)
			}
			if status.State != agentcfg.StateConfigured {
				t.Errorf("state = %q, want %q", status.State, agentcfg.StateConfigured)
			}
			// The entry launches this binary over stdio, which is the only
			// transport v1 has: there is no port to point at.
			if status.Instance != "roca mcp serve" {
				t.Errorf("instance = %q, does not launch `roca mcp serve`", status.Instance)
			}
			// v1 serves over stdio and has no port, so an entry that named a URL
			// would be pointing the agent at a resident process nobody starts.
			if strings.Contains(read(t, path), "url") {
				t.Error("the entry names a URL: v1 serves over stdio and has no port")
			}
		})
	}
}

// Byte-for-byte preservation, measured the only way that is not a matter of
// opinion: installing and then withdrawing has to give back the exact bytes
// that were there, every comment, every blank line and every ordering.
func TestInstallingAndWithdrawingGivesBackTheExactPreviousBytes(t *testing.T) {
	for _, runtime := range agentcfg.Runtimes() {
		t.Run(runtime, func(t *testing.T) {
			path := fixtureFile(t, runtime)
			before := read(t, path)

			if _, err := agentcfg.Install(runtime, path, "roca"); err != nil {
				t.Fatalf("Install: %v", err)
			}
			if _, err := agentcfg.Uninstall(runtime, path); err != nil {
				t.Fatalf("Uninstall: %v", err)
			}

			if after := read(t, path); after != before {
				t.Errorf("the file did not come back to what it was.\n--- before ---\n%s\n--- after ---\n%s",
					before, after)
			}
		})
	}
}

func TestZcodeUsesTheNestedMCPServersShape(t *testing.T) {
	path := fixtureFile(t, agentcfg.RuntimeZcode)
	if _, err := agentcfg.Install(agentcfg.RuntimeZcode, path, "roca"); err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal([]byte(read(t, path)), &document); err != nil {
		t.Fatal(err)
	}
	mcp, ok := document["mcp"].(map[string]any)
	if !ok {
		t.Fatal("zcode config has no mcp object")
	}
	servers, ok := mcp["servers"].(map[string]any)
	if !ok {
		t.Fatal("zcode config has no nested mcp.servers object")
	}
	roca, ok := servers[agentcfg.ServerName].(map[string]any)
	if !ok || roca["type"] != "stdio" || roca["command"] != "roca" || len(roca) != 3 {
		t.Fatalf("zcode roca server = %#v", servers[agentcfg.ServerName])
	}
	if _, flat := document["servers"]; flat {
		t.Fatal("zcode config wrote a flat servers member")
	}
}

func TestZcodeRejectsDuplicateConfigurationMembers(t *testing.T) {
	mcp := `{"mcp":{"servers":{}},"mcp":{"servers":{}}}`
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(mcp), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := agentcfg.Install(agentcfg.RuntimeZcode, path, "roca"); err == nil {
		t.Fatal("ZCode MCP install accepted duplicate mcp members")
	}
	if got := read(t, path); got != mcp {
		t.Fatalf("duplicate MCP config changed: got %s", got)
	}
	hooks := `{"hooks":{"events":{}},"hooks":{"events":{}}}`
	if _, err := agentcfg.DeclareZcodeSessionStartHook(hooks,
		"roca_session_start_marker", "/home/operator/.zcode/hooks/roca-handoff.sh", 15000); err == nil {
		t.Fatal("ZCode hook install accepted duplicate hooks members")
	}
}

func TestZcodeStatusRejectsInvalidConfigurationMembers(t *testing.T) {
	entry := `{"type":"stdio","command":"roca","args":["mcp","serve"]}`
	for _, test := range []struct {
		document, errorFragment string
	}{
		{`{"mcp":{"servers":{"roca":` + entry + `}},"mcp":{"servers":{}}}`, "duplicate object member"},
		{`{"mcp":{"servers":{"roca":` + entry + `},"servers":{}}}`, "duplicate object member"},
		{`{"mcp":{"servers":{"roca":{"type":"stdio","command":"roca","command":"other","args":["mcp","serve"]}}}}`, "duplicate object member"},
		{`{"mcp":{"servers":{"roca":{}}}}`, "must be a stdio command"},
		{`{"mcp":{"servers":{"roca":{"type":"http","command":"roca","args":["mcp","serve"]}}}}`, "must be a stdio command"},
		{`{"mcp":{"servers":{"roca":{"type":"stdio","command":"","args":["mcp","serve"]}}}}`, "must be a stdio command"},
		{`{"mcp":{"servers":{"roca":{"type":"stdio","command":"roca","args":["serve"]}}}}`, "must be a stdio command"},
		{`{"mcp":{"servers":{"roca":{"type":"stdio","command":"roca","args":["mcp","serve"],"extra":true}}}}`, "must be a stdio command"},
	} {
		path := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(path, []byte(test.document), 0o600); err != nil {
			t.Fatal(err)
		}
		report, err := agentcfg.Status(agentcfg.RuntimeZcode, path)
		if err != nil {
			t.Fatal(err)
		}
		if report.State != agentcfg.StateInvalid || !strings.Contains(report.Error, test.errorFragment) {
			t.Fatalf("invalid status = %#v for %s", report, test.document)
		}
	}
}

func TestZcodeHookRoundTripPreservesEmptySessionStartWhitespace(t *testing.T) {
	before := "{\n  \"neighbor\": 9007199254740993,\n  \"hooks\": {\n    \"events\": {\n      \"SessionStart\": [\n        \n      ]\n    }\n  }\n}\n"
	command := "/home/operator/.zcode/hooks/roca-handoff.sh"
	marker := "roca_session_start_marker"
	installed, err := agentcfg.DeclareZcodeSessionStartHook(before, marker, command, 15000)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal([]byte(installed), &document); err != nil {
		t.Fatalf("installed hook config is invalid JSON: %v", err)
	}
	withdrawn, err := agentcfg.RemoveZcodeSessionStartHook(installed, marker)
	if err != nil {
		t.Fatal(err)
	}
	if withdrawn != before {
		t.Fatalf("empty SessionStart bytes changed:\nwant %q\n got %q", before, withdrawn)
	}
}

func TestZcodeEditorsRejectTrailingGarbage(t *testing.T) {
	invalid := `{} trailing`
	if _, err := agentcfg.DeclareZcodeSessionStartHook(invalid, "marker", "/bin/hook", 15000); err == nil {
		t.Fatal("ZCode hook editor accepted trailing garbage")
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(invalid), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := agentcfg.Install(agentcfg.RuntimeZcode, path, "/bin/roca"); err == nil {
		t.Fatal("ZCode MCP editor accepted trailing garbage")
	}
	if got := read(t, path); got != invalid {
		t.Fatalf("invalid config changed: got %q", got)
	}
}

func TestZcodeMCPMatchRequiresExactStdioInvocation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if _, err := agentcfg.Install(agentcfg.RuntimeZcode, path, "/bin/roca"); err != nil {
		t.Fatal(err)
	}
	matched, err := agentcfg.ZcodeMCPMatches(path, "/bin/roca")
	if err != nil || !matched {
		t.Fatalf("installed invocation match = %v, err=%v", matched, err)
	}
	for _, invalid := range []string{
		`{"mcp":{},"mcp":{"servers":{"roca":{"type":"stdio","command":"/bin/roca","args":["mcp","serve"]}}}}`,
		`{"mcp":{"servers":{"roca":{"type":"stdio","type":"stdio","command":"/bin/roca","args":["mcp","serve"]}}}}`,
		`{"mcp":{"servers":{"roca":{"type":"http","command":"/bin/roca","args":["mcp","serve"]}}}}`,
		`{"mcp":{"servers":{"roca":{"type":"stdio","command":"/bin/other","args":["mcp","serve"]}}}}`,
		`{"mcp":{"servers":{"roca":{"type":"stdio","command":"/bin/roca","args":["serve"]}}}}`,
	} {
		if err := os.WriteFile(path, []byte(invalid), 0o600); err != nil {
			t.Fatal(err)
		}
		matched, err := agentcfg.ZcodeMCPMatches(path, "/bin/roca")
		if matched {
			t.Fatalf("invalid invocation match = %v, err=%v: %s", matched, err, invalid)
		}
	}
}

func TestZcodeMCPPreimageUsesComparedInstallSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	concurrent := `{"mcp":{"servers":{}}}`
	var recorded string
	_, err := agentcfg.InstallZcodeMCP(path, "roca", func(preimage string, _ bool) error {
		recorded = preimage
		return os.WriteFile(path, []byte(concurrent), 0o600)
	})
	if err == nil {
		t.Fatal("ZCode MCP install accepted a config changed after its preimage snapshot")
	}
	if recorded != agentcfg.ZcodeMCPPreimageMCPServers {
		t.Fatalf("recorded preimage = %q", recorded)
	}
	if got := read(t, path); got != concurrent {
		t.Fatalf("concurrent config changed: got %s", got)
	}
}

func TestZcodeWithdrawalRemovesOwnedEmptyContainersWithoutServer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"mcp":{"servers":{}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := agentcfg.UninstallZcodeMCP(path, agentcfg.ZcodeMCPPreimageMCPServers); err != nil {
		t.Fatal(err)
	}
	if got := read(t, path); got != `{}` {
		t.Fatalf("owned empty containers remained: %s", got)
	}
}

func TestZcodeWithdrawalRestoresMCPContainerPreimage(t *testing.T) {
	for _, before := range []string{
		`{}`,
		`{"numeric_spelling":9007199254740993,"mcp":{}}`,
		`{"mcp":{"servers":{}}}`,
	} {
		t.Run(before, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(path, []byte(before), 0o600); err != nil {
				t.Fatal(err)
			}
			preimage, err := agentcfg.ZcodeMCPPreimage(before)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := agentcfg.Install(agentcfg.RuntimeZcode, path, "roca"); err != nil {
				t.Fatal(err)
			}
			if _, err := agentcfg.UninstallZcodeMCP(path, preimage); err != nil {
				t.Fatal(err)
			}
			if after := read(t, path); after != before {
				t.Fatalf("MCP container preimage changed:\nwant %s\n got %s", before, after)
			}
		})
	}
}

func TestZcodeHookWithdrawalRestoresContainerPreimage(t *testing.T) {
	for _, before := range []string{
		`{}`,
		`{"hooks":{}}`,
		`{"hooks":{"events":{}}}`,
	} {
		t.Run(before, func(t *testing.T) {
			installed, err := agentcfg.DeclareZcodeSessionStartHook(before,
				"roca_session_start_marker", "/home/operator/.zcode/hooks/roca-handoff.sh", 15000)
			if err != nil {
				t.Fatal(err)
			}
			withdrawn, err := agentcfg.RemoveZcodeSessionStartHook(installed, "roca_session_start_marker")
			if err != nil {
				t.Fatal(err)
			}
			if withdrawn != before {
				t.Fatalf("hook container preimage changed:\nwant %s\n got %s", before, withdrawn)
			}
		})
	}
}

func TestJSONCWithdrawalPreservesBytesBeforeTheNextMember(t *testing.T) {
	path := filepath.Join(t.TempDir(), "opencode.json")
	const before = "{\n  \"mcp\": {\"roca\": {\"type\": \"local\", \"command\": [\"roca\", \"mcp\", \"serve\"], \"enabled\": true},\n    // This comment belongs to the next member.\n    \"other\": {\"type\": \"local\", \"command\": [\"other\"]}\n  }\n}\n"
	const want = "{\n  \"mcp\": {\n    // This comment belongs to the next member.\n    \"other\": {\"type\": \"local\", \"command\": [\"other\"]}\n  }\n}\n"
	if err := os.WriteFile(path, []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := agentcfg.Uninstall(agentcfg.RuntimeOpencode, path); err != nil {
		t.Fatal(err)
	}
	if got := read(t, path); got != want {
		t.Fatalf("uninstalled bytes:\n--- want ---\n%s--- got ---\n%s", want, got)
	}
}

func TestCodexEditsQuotedRocaTableHeaders(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	const before = "model = \"synthetic\"\n\n[mcp_servers.\"roca\"]\ncommand = \"old-roca\"\nargs = [\"old\"]\n\n[mcp_servers.other]\ncommand = \"other\"\n"
	if err := os.WriteFile(path, []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := agentcfg.Install(agentcfg.RuntimeCodex, path, "new-roca"); err != nil {
		t.Fatal(err)
	}
	installed := read(t, path)
	if strings.Count(installed, "roca\"]") != 1 || !strings.Contains(installed, "command = \"new-roca\"") {
		t.Fatalf("quoted table was not replaced in place:\n%s", installed)
	}
	if _, err := agentcfg.Uninstall(agentcfg.RuntimeCodex, path); err != nil {
		t.Fatal(err)
	}
	want := "model = \"synthetic\"\n\n[mcp_servers.other]\ncommand = \"other\"\n"
	if got := read(t, path); got != want {
		t.Fatalf("uninstalled bytes:\n--- want ---\n%s--- got ---\n%s", want, got)
	}
}

func TestHermesInstallsIntoEmptyServerMappings(t *testing.T) {
	cases := []struct {
		name   string
		before string
		after  string
	}{
		{
			name:   "null block mapping",
			before: "# Hermes configuration\nruntime: hermes\nmcp_servers:\nlogging: verbose\n",
			after: "# Hermes configuration\nruntime: hermes\nmcp_servers:\n  roca:\n" +
				"    command: roca\n    args:\n      - mcp\n      - serve\nlogging: verbose\n",
		},
		{
			name:   "empty flow mapping",
			before: "# Hermes configuration\nruntime: hermes\nmcp_servers: {}\nlogging: verbose\n",
			after: "# Hermes configuration\nruntime: hermes\nmcp_servers: " +
				"{roca: {command: roca, args: [mcp, serve]}}\nlogging: verbose\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(tc.before), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := agentcfg.Install(agentcfg.RuntimeHermes, path, "roca"); err != nil {
				t.Fatalf("Install: %v", err)
			}
			if got := read(t, path); got != tc.after {
				t.Errorf("installed bytes:\n--- want ---\n%s--- got ---\n%s", tc.after, got)
			}
			status, err := agentcfg.Status(agentcfg.RuntimeHermes, path)
			if err != nil {
				t.Fatalf("Status: %v", err)
			}
			if status.State != agentcfg.StateConfigured {
				t.Errorf("state = %q, want %q", status.State, agentcfg.StateConfigured)
			}
			second, err := agentcfg.Install(agentcfg.RuntimeHermes, path, "roca")
			if err != nil {
				t.Fatalf("second Install: %v", err)
			}
			if second.Changed || read(t, path) != tc.after {
				t.Error("the second installation moved bytes")
			}
			if _, err := agentcfg.Uninstall(agentcfg.RuntimeHermes, path); err != nil {
				t.Fatalf("Uninstall: %v", err)
			}
			if got := read(t, path); got != tc.before {
				t.Errorf("uninstalled bytes:\n--- want ---\n%s--- got ---\n%s", tc.before, got)
			}
		})
	}
}

// The other half of the same question: while Roca is installed, everything that
// was not Roca is still there, in the same order.
func TestTheNeighboursSurviveTheInstallation(t *testing.T) {
	for _, runtime := range agentcfg.Runtimes() {
		t.Run(runtime, func(t *testing.T) {
			path := fixtureFile(t, runtime)
			before := read(t, path)

			if _, err := agentcfg.Install(runtime, path, "roca"); err != nil {
				t.Fatalf("Install: %v", err)
			}
			after := read(t, path)

			for _, line := range strings.Split(before, "\n") {
				if strings.TrimSpace(line) == "" {
					continue
				}
				if !strings.Contains(after, line) {
					t.Errorf("the line %q was lost", line)
				}
			}
			if !strings.Contains(after, "some-other-server") {
				t.Error("the neighbouring server disappeared")
			}
		})
	}
}

// Installing twice writes nothing the second time. It matters because the
// operator's real flow reinstalls on top, and a second backup on every run
// turns their config directory into a graveyard.
func TestInstallingTwiceIsIdempotentAndWritesNothingTheSecondTime(t *testing.T) {
	for _, runtime := range agentcfg.Runtimes() {
		t.Run(runtime, func(t *testing.T) {
			path := fixtureFile(t, runtime)
			if _, err := agentcfg.Install(runtime, path, "roca"); err != nil {
				t.Fatalf("first Install: %v", err)
			}
			afterFirst := read(t, path)

			second, err := agentcfg.Install(runtime, path, "roca")
			if err != nil {
				t.Fatalf("second Install: %v", err)
			}
			if second.Changed {
				t.Error("the second installation changed the file")
			}
			if second.Backup != "" {
				t.Error("the second installation left a backup of nothing")
			}
			if read(t, path) != afterFirst {
				t.Error("the second installation moved bytes")
			}
		})
	}
}

// A config file that is not there yet is created with only Roca in it: an agent
// installed on a machine where the runtime has never run is still a valid
// installation.
func TestInstallingCreatesTheFileWhenTheRuntimeHasNeverRun(t *testing.T) {
	for _, runtime := range agentcfg.Runtimes() {
		t.Run(runtime, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "nested", "config"+extensionOf(runtime))

			outcome, err := agentcfg.Install(runtime, path, "roca")
			if err != nil {
				t.Fatalf("Install: %v", err)
			}
			if !outcome.Changed {
				t.Fatal("nothing was created")
			}
			if outcome.Backup != "" {
				t.Error("a backup of a file that did not exist")
			}
			status, err := agentcfg.Status(runtime, path)
			if err != nil {
				t.Fatalf("Status: %v", err)
			}
			if status.State != agentcfg.StateConfigured {
				t.Errorf("state = %q, want %q", status.State, agentcfg.StateConfigured)
			}
		})
	}
}

// Withdrawing from a config that never had Roca is a no-op, not a failure: an
// uninstall runs over whatever state it finds.
func TestWithdrawingWhatWasNeverInstalledChangesNothing(t *testing.T) {
	for _, runtime := range agentcfg.Runtimes() {
		t.Run(runtime, func(t *testing.T) {
			path := fixtureFile(t, runtime)
			before := read(t, path)

			outcome, err := agentcfg.Uninstall(runtime, path)
			if err != nil {
				t.Fatalf("Uninstall: %v", err)
			}
			if outcome.Changed {
				t.Error("withdrawing what was not there changed the file")
			}
			if read(t, path) != before {
				t.Error("withdrawing what was not there moved bytes")
			}
		})
	}
}

// And a config file that is not there is not created by an uninstall: what is
// removed is Roca's entry, never the operator's file.
func TestWithdrawingDoesNotCreateAConfigThatIsNotThere(t *testing.T) {
	for _, runtime := range agentcfg.Runtimes() {
		t.Run(runtime, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config"+extensionOf(runtime))

			if _, err := agentcfg.Uninstall(runtime, path); err != nil {
				t.Fatalf("Uninstall: %v", err)
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Error("the uninstall created a config file that was not there")
			}
		})
	}
}

func TestFreshEditDoesNotReplaceConcurrentlyCreatedConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	operator := `{"hooks":{"enabled":true}}`
	outcome, err := agentcfg.Edit(agentcfg.RuntimeZcode, path, func(previous string) (string, error) {
		if previous != "" {
			t.Fatalf("fresh edit previous bytes = %q", previous)
		}
		if err := os.WriteFile(path, []byte(operator), 0o640); err != nil {
			t.Fatal(err)
		}
		return `{"mcp":{"servers":{"roca":{"type":"stdio","command":"roca","args":["mcp","serve"]}}}}`, nil
	}, true)
	if err == nil {
		t.Fatal("fresh edit replaced a concurrently created config")
	}
	if outcome.Changed {
		t.Fatal("failed fresh edit reported a change")
	}
	if got := read(t, path); got != operator {
		t.Fatalf("concurrently created config changed: got %s", got)
	}
}

// A config Roca cannot parse is a config Roca must not edit. It is reported
// with the file, the reason and no write at all.
func TestABrokenConfigIsReportedAndNotEdited(t *testing.T) {
	for _, runtime := range agentcfg.Runtimes() {
		t.Run(runtime, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config"+extensionOf(runtime))
			broken := "{[ this is not valid in any of the five formats ::: }"
			if err := os.WriteFile(path, []byte(broken), 0o600); err != nil {
				t.Fatalf("write: %v", err)
			}

			if _, err := agentcfg.Install(runtime, path, "roca"); err == nil {
				t.Fatal("a config that cannot be parsed was edited anyway")
			} else if !strings.Contains(err.Error(), path) {
				t.Errorf("the error %q does not name the file", err)
			}
			if read(t, path) != broken {
				t.Error("a config that cannot be parsed was written to")
			}
			status, err := agentcfg.Status(runtime, path)
			if err != nil {
				t.Fatalf("Status: %v", err)
			}
			if status.State != agentcfg.StateInvalid {
				t.Errorf("state = %q, want %q", status.State, agentcfg.StateInvalid)
			}
		})
	}
}

func TestStatusReportsAConfigThatIsNotThereWithoutCreatingIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")

	status, err := agentcfg.Status(agentcfg.RuntimeCodex, path)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.State != agentcfg.StateMissing {
		t.Errorf("state = %q, want %q", status.State, agentcfg.StateMissing)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("asking about the state created the file")
	}
}

func TestAnUnknownRuntimeNamesTheOnesThatExist(t *testing.T) {
	_, err := agentcfg.Install("emacs", filepath.Join(t.TempDir(), "x"), "roca")
	if err == nil {
		t.Fatal("an unknown runtime was accepted")
	}
	for _, runtime := range agentcfg.Runtimes() {
		if !strings.Contains(err.Error(), runtime) {
			t.Errorf("the error %q does not name the supported runtime %q", err, runtime)
		}
	}
}

// The executable written into the config is whatever the caller asks for. A
// bare `roca` keeps a config portable between machines; an absolute path is
// written only when somebody asks for one.
func TestTheExecutableWrittenIntoTheConfigIsTheOneAsked(t *testing.T) {
	path := fixtureFile(t, agentcfg.RuntimeClaude)

	if _, err := agentcfg.Install(agentcfg.RuntimeClaude, path,
		"/opt/roca/bin/roca"); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if !strings.Contains(read(t, path), "/opt/roca/bin/roca") {
		t.Error("the config does not name the executable that was asked for")
	}
}

// When a config file carries no servers key at all, install creates it and
// uninstall has to remove it entirely, not leave an empty object behind.
func TestWithdrawingFromAConfigWhoseServersKeyWasCreatedByInstallRestoresTheExactBytes(t *testing.T) {
	verify := func(runtime string) {
		t.Helper()
		path := filepath.Join(t.TempDir(), "config"+extensionOf(runtime))
		before := "{}\n"
		if err := os.WriteFile(path, []byte(before), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := agentcfg.Install(runtime, path, "roca"); err != nil {
			t.Fatal(err)
		}
		if _, err := agentcfg.Uninstall(runtime, path); err != nil {
			t.Fatal(err)
		}
		after := read(t, path)
		if after != before {
			t.Errorf("expected %q, got %q", before, after)
		}
	}
	verify(agentcfg.RuntimeClaude)
	verify(agentcfg.RuntimeOpencode)
	verify(agentcfg.RuntimePi)
}

// Where each runtime keeps its config comes from the home and the environment,
// in that order of increasing precedence, exactly as the rest of the product
// resolves its paths.
func TestTheConfigPathComesFromTheHomeAndTheEnvironment(t *testing.T) {
	home := t.TempDir()
	cases := []struct {
		runtime string
		env     map[string]string
		want    string
	}{
		{agentcfg.RuntimeCodex, nil, filepath.Join(home, ".codex", "config.toml")},
		{agentcfg.RuntimeCodex, map[string]string{"CODEX_HOME": "/elsewhere"},
			"/elsewhere/config.toml"},
		{agentcfg.RuntimeClaude, nil, filepath.Join(home, ".claude.json")},
		{agentcfg.RuntimeOpencode, nil,
			filepath.Join(home, ".config", "opencode", "opencode.json")},
		{agentcfg.RuntimeHermes, nil, filepath.Join(home, ".hermes", "config.yaml")},
		{agentcfg.RuntimePi, nil, filepath.Join(home, ".pi", "agent", "mcp.json")},
		{agentcfg.RuntimeZcode, nil, filepath.Join(home, ".zcode", "cli", "config.json")},
	}
	for _, tc := range cases {
		t.Run(tc.runtime, func(t *testing.T) {
			got, err := agentcfg.ConfigPath(tc.runtime, home, lookup(tc.env))
			if err != nil {
				t.Fatalf("ConfigPath: %v", err)
			}
			if got != tc.want {
				t.Errorf("path = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestClaudeDesktopConfigPathResolvesSupportedPlatforms(t *testing.T) {
	home := t.TempDir()
	appData := filepath.Join(home, "AppData", "Roaming")
	cases := []struct {
		goos string
		env  map[string]string
		want string
	}{
		{"darwin", nil, filepath.Join(home, "Library", "Application Support", "Claude", "claude_desktop_config.json")},
		{"windows", map[string]string{"APPDATA": appData}, filepath.Join(appData, "Claude", "claude_desktop_config.json")},
	}
	for _, test := range cases {
		got, err := agentcfg.ConfigPathForOS(agentcfg.RuntimeClaudeDesktop, home, test.goos,
			lookup(test.env))
		if err != nil {
			t.Fatal(err)
		}
		if got != test.want {
			t.Fatalf("%s path = %q, want %q", test.goos, got, test.want)
		}
	}
	if _, err := agentcfg.ConfigPathForOS(agentcfg.RuntimeClaudeDesktop, home, "linux", lookup(nil)); err == nil {
		t.Fatal("explicit Claude Desktop selection on Linux did not explain its unsupported platform")
	}
}

func TestRuntimeCatalogueOmitsClaudeDesktopOnUnsupportedPlatforms(t *testing.T) {
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		return
	}
	for _, name := range agentcfg.Runtimes() {
		if name == agentcfg.RuntimeClaudeDesktop {
			t.Fatalf("Claude Desktop appeared in the %s runtime catalogue", runtime.GOOS)
		}
	}
}

func TestZcodeConfigPathTreatsZcodeHomeAsTheRuntimeRoot(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, "elsewhere")
	got, err := agentcfg.ConfigPathForOS(agentcfg.RuntimeZcode, home, "darwin",
		lookup(map[string]string{"ZCODE_HOME": root}))
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "cli", "config.json")
	if got != want {
		t.Fatalf("path = %q, want %q", got, want)
	}
}

// --- the harness ---

func fixtureFile(t *testing.T, runtime string) string {
	t.Helper()
	content, ok := fixtures[runtime]
	if !ok {
		t.Fatalf("there is no fixture for the runtime %q", runtime)
	}
	path := filepath.Join(t.TempDir(), "config"+extensionOf(runtime))
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write the fixture: %v", err)
	}
	return path
}

func extensionOf(runtime string) string {
	switch runtime {
	case agentcfg.RuntimeCodex:
		return ".toml"
	case agentcfg.RuntimeHermes:
		return ".yaml"
	default:
		return ".json"
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(content)
}

func lookup(env map[string]string) func(string) string {
	return func(key string) string { return env[key] }
}

// The inline-table refusal matched the servers key as a PREFIX, so a document
// carrying an unrelated key that merely starts the same way was refused with a
// complaint about a table it does not have.
func TestAKeyThatMerelyStartsLikeTheServersKeyIsNotRefused(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	// `mcp_servers_legacy` is somebody else's key, written inline, beside a
	// perfectly ordinary tables form of the real one.
	const before = "mcp_servers_legacy = { old = true }\n\n[mcp_servers.other]\ncommand = \"x\"\n"
	if err := os.WriteFile(path, []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := agentcfg.Install(agentcfg.RuntimeCodex, path, "roca"); err != nil {
		t.Fatalf("an unrelated inline key was refused: %v", err)
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(after), "mcp_servers_legacy = { old = true }") {
		t.Errorf("the neighbour key did not survive:\n%s", after)
	}
	if !strings.Contains(string(after), "[mcp_servers.roca]") {
		t.Errorf("the declaration did not land:\n%s", after)
	}
}

// write promises "the previous file's permissions are kept: this file is the
// operator's". os.WriteFile only applies its mode when it CREATES the file, and
// the staged file already exists at 0600 from os.CreateTemp, so the mode was
// computed and then thrown away: an operator's 0644 config came back 0600.
func TestTheOperatorsPermissionsSurviveAnEdit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[mcp_servers.other]\ncommand = \"x\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := agentcfg.Install(agentcfg.RuntimeCodex, path, "roca"); err != nil {
		t.Fatalf("install: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o644 {
		t.Errorf("mode = %o, want 644: the operator's permissions were not kept", got)
	}
}

func TestBackupNameStatErrorsAreReturned(t *testing.T) {
	path := filepath.Join(t.TempDir(), strings.Repeat("x", 250))
	if err := os.WriteFile(path, []byte("model = \"synthetic\"\n"), 0o600); err != nil {
		t.Skipf("filesystem does not support the fixture name: %v", err)
	}
	if _, err := agentcfg.Install(agentcfg.RuntimeCodex, path, "roca"); err == nil || !strings.Contains(err.Error(), "inspect backup") {
		t.Fatalf("Install error = %v, want backup inspection failure", err)
	}
}
