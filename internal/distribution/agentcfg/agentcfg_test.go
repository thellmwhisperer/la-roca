package agentcfg_test

import (
	"os"
	"path/filepath"
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
