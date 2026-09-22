package agentcfg_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/distribution/agentcfg"
	"github.com/thellmwhisperer/la-roca/internal/securefile"
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
			path := filepath.Join(t.TempDir(), "config"+extensionOf(runtime))

			outcome, err := agentcfg.Install(runtime, path, "roca")
			if err != nil {
				t.Fatalf("Install: %v", err)
			}
			if !outcome.Changed {
				t.Error("installing over a config with no Roca changed nothing")
			}
			if outcome.Backup != "" {
				t.Fatal("creation backed up a nonexistent file")
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
			refused := expectRefusedEdit(t, path)
			outcome, err = agentcfg.Uninstall(runtime, path)
			refused(outcome, err)
		})
	}
}

func TestRefusedInstallationAndNoopWithdrawalPreserveTheExactPreviousBytes(t *testing.T) {
	for _, runtime := range agentcfg.Runtimes() {
		t.Run(runtime, func(t *testing.T) {
			path := fixtureFile(t, runtime)
			before := read(t, path)

			refused := expectRefusedEdit(t, path)
			outcome, err := agentcfg.Install(runtime, path, "roca")
			refused(outcome, err)
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

func TestRefusedJSONCWithdrawalPreservesBytesBeforeTheNextMember(t *testing.T) {
	path := filepath.Join(t.TempDir(), "opencode.json")
	const before = "{\n  \"mcp\": {\"roca\": {\"type\": \"local\", \"command\": [\"roca\", \"mcp\", \"serve\"], \"enabled\": true},\n    // This comment belongs to the next member.\n    \"other\": {\"type\": \"local\", \"command\": [\"other\"]}\n  }\n}\n"
	if err := os.WriteFile(path, []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}
	refused := expectRefusedEdit(t, path)
	outcome, err := agentcfg.Uninstall(agentcfg.RuntimeOpencode, path)
	refused(outcome, err)
}

func TestCodexRefusesEditsToQuotedRocaTableHeaders(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	const before = "model = \"synthetic\"\n\n[mcp_servers.\"roca\"]\ncommand = \"old-roca\"\nargs = [\"old\"]\n\n[mcp_servers.other]\ncommand = \"other\"\n"
	if err := os.WriteFile(path, []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, operation := range []func() (agentcfg.Outcome, error){
		func() (agentcfg.Outcome, error) { return agentcfg.Install(agentcfg.RuntimeCodex, path, "new-roca") },
		func() (agentcfg.Outcome, error) { return agentcfg.Uninstall(agentcfg.RuntimeCodex, path) },
	} {
		refused := expectRefusedEdit(t, path)
		outcome, err := operation()
		refused(outcome, err)
	}
}

func TestHermesRefusesInstallationIntoExistingEmptyServerMappings(t *testing.T) {
	cases := []struct {
		name   string
		before string
	}{
		{
			name:   "null block mapping",
			before: "# Hermes configuration\nruntime: hermes\nmcp_servers:\nlogging: verbose\n",
		},
		{
			name:   "empty flow mapping",
			before: "# Hermes configuration\nruntime: hermes\nmcp_servers: {}\nlogging: verbose\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(tc.before), 0o600); err != nil {
				t.Fatal(err)
			}
			refused := expectRefusedEdit(t, path)
			outcome, err := agentcfg.Install(agentcfg.RuntimeHermes, path, "roca")
			refused(outcome, err)
			status, err := agentcfg.Status(agentcfg.RuntimeHermes, path)
			if err != nil || status.State != agentcfg.StateNotConfigured {
				t.Fatalf("refused install status = %+v, err %v", status, err)
			}
		})
	}
}

func TestTheNeighboursSurviveARefusedInstallation(t *testing.T) {
	for _, runtime := range agentcfg.Runtimes() {
		t.Run(runtime, func(t *testing.T) {
			path := fixtureFile(t, runtime)
			before := read(t, path)

			refused := expectRefusedEdit(t, path)
			outcome, err := agentcfg.Install(runtime, path, "roca")
			refused(outcome, err)
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
			path := filepath.Join(t.TempDir(), "config"+extensionOf(runtime))
			if _, err := agentcfg.Install(runtime, path, "roca"); err != nil {
				t.Fatalf("first Install: %v", err)
			}
			afterFirst := read(t, path)
			original, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}

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
			current, err := os.Stat(path)
			if err != nil || !os.SameFile(original, current) || original.Mode() != current.Mode() {
				t.Fatalf("idempotent install changed file identity or permissions: %v", err)
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
	path := filepath.Join(t.TempDir(), "config.json")

	if _, err := agentcfg.Install(agentcfg.RuntimeClaude, path,
		"/opt/roca/bin/roca"); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if !strings.Contains(read(t, path), "/opt/roca/bin/roca") {
		t.Error("the config does not name the executable that was asked for")
	}
}

func TestRefusedInstallationLeavesAbsentServerKeysAbsent(t *testing.T) {
	verify := func(runtime string) {
		t.Helper()
		path := filepath.Join(t.TempDir(), "config"+extensionOf(runtime))
		before := "{}\n"
		if err := os.WriteFile(path, []byte(before), 0o600); err != nil {
			t.Fatal(err)
		}
		refused := expectRefusedEdit(t, path)
		outcome, err := agentcfg.Install(runtime, path, "roca")
		refused(outcome, err)
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
	verify(agentcfg.RuntimeZcode)
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
func TestAKeyThatMerelyStartsLikeTheServersKeyReachesPublication(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	// `mcp_servers_legacy` is somebody else's key, written inline, beside a
	// perfectly ordinary tables form of the real one.
	const before = "mcp_servers_legacy = { old = true }\n\n[mcp_servers.other]\ncommand = \"x\"\n"
	if err := os.WriteFile(path, []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}

	refused := expectRefusedEdit(t, path)
	outcome, err := agentcfg.Install(agentcfg.RuntimeCodex, path, "roca")
	refused(outcome, err)
}

func TestTheOperatorsPermissionsSurviveARefusedEdit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[mcp_servers.other]\ncommand = \"x\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}

	refused := expectRefusedEdit(t, path)
	outcome, err := agentcfg.Install(agentcfg.RuntimeCodex, path, "roca")
	refused(outcome, err)

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

// Capture the live file before an edit, then verify the public refusal contract.
func expectRefusedEdit(t *testing.T, path string) func(agentcfg.Outcome, error) {
	t.Helper()
	before := read(t, path)
	original, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return func(outcome agentcfg.Outcome, err error) {
		t.Helper()
		if !errors.Is(err, securefile.ErrConditionalReplaceUnsupported) || outcome.Changed {
			t.Fatalf("edit = %+v, err %v; want safe refusal", outcome, err)
		}
		current, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if read(t, path) != before || !os.SameFile(original, current) || original.Mode() != current.Mode() {
			t.Fatal("refused edit changed live bytes, identity, or permissions")
		}
		if outcome.Backup == "" || read(t, outcome.Backup) != before {
			t.Fatalf("refused edit lost its exact backup: %+v", outcome)
		}
		backup, err := os.Stat(outcome.Backup)
		if err != nil || backup.Mode().Perm() != 0o600 {
			t.Fatalf("backup permissions: %v, err %v", backup, err)
		}
	}
}
