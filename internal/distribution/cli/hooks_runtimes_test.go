package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/distribution/agentcfg"
)

// Every fixture in this file already holds another tool's hook or another
// tool's extension. That is the whole point: an operator's session-start
// surface is shared, and an install that quietly took it over would be worse
// than the omission this feature closes.

func TestCodexInstallKeepsForeignSessionHooks(t *testing.T) {
	home := skillTestHome(t)
	binary := filepath.Join(home, "bin", "roca")
	t.Setenv(EnvExecutable, binary)
	path := filepath.Join(home, ".codex", "hooks.json")
	writeFile(t, path, `{
  "hooks": {
    "SessionStart": [
      {"hooks": [{"command": "tasks-axi", "timeout": 10, "type": "command"}], "matcher": ""},
      {"hooks": [{"command": "bash '/opt/pane-state.sh' session", "timeout": 10, "type": "command"}]}
    ]
  }
}
`)

	var output strings.Builder
	command := sessionHookCommand(binary, agentcfg.RuntimeCodex, sessionRequest{pills: true})
	for range 2 {
		runHookCLI(t, &output, nil, "install", "codex", "--pills")
		entries := nestedHookCommands(t, path, "SessionStart")
		assertCommandCount(t, entries, "tasks-axi", 1)
		assertCommandCount(t, entries, "bash '/opt/pane-state.sh' session", 1)
		assertCommandCount(t, entries, command, 1)
	}

	runHookCLI(t, &output, nil, "uninstall", "codex")
	entries := nestedHookCommands(t, path, "SessionStart")
	assertCommandCount(t, entries, "tasks-axi", 1)
	assertCommandCount(t, entries, "bash '/opt/pane-state.sh' session", 1)
	assertCommandCount(t, entries, command, 0)
}

// A reinstall after the binary moved repoints the entry La Roca owns instead of
// adding a second one beside it.
func TestCodexReinstallRepointsAMovedBinary(t *testing.T) {
	home := skillTestHome(t)
	path := filepath.Join(home, ".codex", "hooks.json")
	var output strings.Builder

	t.Setenv(EnvExecutable, filepath.Join(home, "bin", "roca"))
	runHookCLI(t, &output, nil, "install", "codex")
	moved := filepath.Join(home, "elsewhere", "roca")
	t.Setenv(EnvExecutable, moved)
	runHookCLI(t, &output, nil, "install", "codex")

	entries := nestedHookCommands(t, path, "SessionStart")
	if len(entries) != 1 {
		t.Fatalf("SessionStart entries = %d, want one repointed hook: %v", len(entries), entries)
	}
	assertCommandCount(t, entries,
		sessionHookCommand(moved, agentcfg.RuntimeCodex, sessionRequest{}), 1)
}

func TestCursorInstallKeepsForeignSessionHooksAndSchemaVersion(t *testing.T) {
	home := skillTestHome(t)
	binary := filepath.Join(home, "bin", "roca")
	t.Setenv(EnvExecutable, binary)
	path := filepath.Join(home, ".cursor", "hooks.json")
	foreign := "bash '/opt/pane-state.sh' session"
	writeFile(t, path, `{
  "hooks": {"sessionStart": [{"command": "`+foreign+`"}]},
  "version": 1
}
`)

	var output strings.Builder
	command := sessionHookCommand(binary, agentcfg.RuntimeCursor, sessionRequest{handoff: true})
	for range 2 {
		runHookCLI(t, &output, nil, "install", "cursor", "--handoff")
		entries := flatHookCommands(t, path, "sessionStart")
		assertCommandCount(t, entries, foreign, 1)
		assertCommandCount(t, entries, command, 1)
		if version := hookDocument(t, path)["version"]; version != float64(1) {
			t.Fatalf("cursor schema version = %#v", version)
		}
	}

	runHookCLI(t, &output, nil, "uninstall", "cursor")
	entries := flatHookCommands(t, path, "sessionStart")
	assertCommandCount(t, entries, foreign, 1)
	assertCommandCount(t, entries, command, 0)
}

// A Cursor file this product creates has to declare the schema version, or
// Cursor reads none of the hooks in it, La Roca's included.
func TestCursorInstallCreatesADocumentCursorCanRead(t *testing.T) {
	home := skillTestHome(t)
	t.Setenv(EnvExecutable, filepath.Join(home, "bin", "roca"))
	path := filepath.Join(home, ".cursor", "hooks.json")

	var output strings.Builder
	runHookCLI(t, &output, nil, "install", "cursor")
	if version := hookDocument(t, path)["version"]; version != float64(1) {
		t.Fatalf("a created cursor document declares version %#v", version)
	}
	assertCommandCount(t, flatHookCommands(t, path, "sessionStart"),
		sessionHookCommand(filepath.Join(home, "bin", "roca"),
			agentcfg.RuntimeCursor, sessionRequest{}), 1)
}

// A document this product cannot read never blocks a withdrawal, and never
// loses a byte of what the operator put there.
func TestJSONHookWithdrawalLeavesAnUnreadableDocumentAlone(t *testing.T) {
	for _, runtime := range []string{agentcfg.RuntimeCodex, agentcfg.RuntimeCursor} {
		t.Run(runtime, func(t *testing.T) {
			skillTestHome(t)
			path, err := hookArtifactPath(runtime)
			if err != nil {
				t.Fatal(err)
			}
			body := `{"hooks": "operator-owned"}`
			writeFile(t, path, body)

			var output, warning strings.Builder
			runHookCLI(t, &output, &warning, "uninstall", runtime)
			if !strings.Contains(warning.String(), path) ||
				!strings.Contains(warning.String(), "hooks run session --runtime "+runtime) {
				t.Fatalf("the warning does not name the file and the entry: %q", warning.String())
			}
			if got := string(mustRead(t, path)); got != body {
				t.Fatalf("withdrawal rewrote a document it could not read: %s", got)
			}
		})
	}
}

func TestScriptInstallLeavesForeignExtensionsAlone(t *testing.T) {
	for _, test := range []struct {
		runtime, neighbour string
	}{
		{agentcfg.RuntimePi, filepath.Join(".pi", "agent", "extensions", "pane-state.ts")},
		{agentcfg.RuntimeOpencode, filepath.Join(".config", "opencode", "plugins", "pane-state.js")},
	} {
		t.Run(test.runtime, func(t *testing.T) {
			home := skillTestHome(t)
			binary := filepath.Join(home, "bin", "roca")
			t.Setenv(EnvExecutable, binary)
			neighbour := filepath.Join(home, test.neighbour)
			foreign := "// installed by another tool\nexport default function () {}\n"
			writeFile(t, neighbour, foreign)
			path, err := hookArtifactPath(test.runtime)
			if err != nil {
				t.Fatal(err)
			}

			var output strings.Builder
			for range 2 {
				runHookCLI(t, &output, nil, "install", test.runtime, "--pills", "--handoff")
				script := string(mustRead(t, path))
				if !strings.Contains(script, rocaScriptMarker) {
					t.Fatalf("the installed script carries no ownership line: %s", script)
				}
				for _, want := range []string{
					`"--runtime"`, `"` + test.runtime + `"`, `"--pills"`, `"--handoff"`, binary,
				} {
					if !strings.Contains(script, want) {
						t.Fatalf("the installed script does not carry %s: %s", want, script)
					}
				}
				if got := string(mustRead(t, neighbour)); got != foreign {
					t.Fatalf("install rewrote a neighbouring extension: %s", got)
				}
			}

			runHookCLI(t, &output, nil, "uninstall", test.runtime)
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("the installed script survived uninstall: %v", err)
			}
			if got := string(mustRead(t, neighbour)); got != foreign {
				t.Fatalf("uninstall removed a neighbouring extension: %s", got)
			}
		})
	}
}

// The pi extension and the OpenCode plugin have to be the file each harness
// loads: one default-exported factory, one hook name the harness calls.
func TestScriptInstallWritesTheShapeEachHarnessLoads(t *testing.T) {
	home := skillTestHome(t)
	t.Setenv(EnvExecutable, filepath.Join(home, "bin", "roca"))
	var output strings.Builder

	runHookCLI(t, &output, nil, "install", "pi")
	pi := string(mustRead(t, mustHookPath(t, agentcfg.RuntimePi)))
	for _, want := range []string{
		"export default function (pi)", `pi.on("session_start"`, `pi.on("before_agent_start"`,
	} {
		if !strings.Contains(pi, want) {
			t.Fatalf("the pi extension does not carry %q: %s", want, pi)
		}
	}

	runHookCLI(t, &output, nil, "install", "opencode")
	opencode := string(mustRead(t, mustHookPath(t, agentcfg.RuntimeOpencode)))
	for _, want := range []string{
		"export const RocaSessionContextPlugin", `"experimental.chat.system.transform"`,
		"output.system.push(body)",
	} {
		if !strings.Contains(opencode, want) {
			t.Fatalf("the OpenCode plugin does not carry %q: %s", want, opencode)
		}
	}
}

// The whole script is the SYSTEM fragment, so an operator's edits inside it are
// left alone and named until `--force` says to replace them.
func TestScriptInstallKeepsAnEditedScriptUntilForced(t *testing.T) {
	home := skillTestHome(t)
	t.Setenv(EnvExecutable, filepath.Join(home, "bin", "roca"))
	path := mustHookPath(t, agentcfg.RuntimePi)

	var output, warning strings.Builder
	runHookCLI(t, &output, nil, "install", "pi")
	edited := string(mustRead(t, path)) + "\n// operator note\n"
	writeFile(t, path, edited)

	runHookCLI(t, &output, &warning, "install", "pi", "--pills")
	if got := string(mustRead(t, path)); got != edited {
		t.Fatalf("an edited script was replaced without consent: %s", got)
	}
	if !strings.Contains(warning.String(), "--force") {
		t.Fatalf("the warning does not say how to replace it: %q", warning.String())
	}

	runHookCLI(t, &output, nil, "install", "pi", "--pills", "--force")
	if got := string(mustRead(t, path)); got == edited {
		t.Fatal("--force did not replace the edited script")
	}
}

// A file at La Roca's own path that La Roca did not write belongs to someone
// else. It is refused, not replaced and not backed up over.
func TestScriptInstallRefusesAFileItDidNotWrite(t *testing.T) {
	home := skillTestHome(t)
	t.Setenv(EnvExecutable, filepath.Join(home, "bin", "roca"))
	path := mustHookPath(t, agentcfg.RuntimeOpencode)
	foreign := "export default { id: \"someone.else\" };\n"
	writeFile(t, path, foreign)

	root := rootCommand(&cliEnv{out: &strings.Builder{}, errOut: &strings.Builder{},
		build: Build{Version: "v1.2.3"}})
	root.SetArgs([]string{"hooks", "install", "opencode", "--force"})
	if err := root.Execute(); err == nil {
		t.Fatal("install replaced a file La Roca did not write")
	}
	if got := string(mustRead(t, path)); got != foreign {
		t.Fatalf("a refused install rewrote the file: %s", got)
	}
}

// One fragment, six harnesses, six envelopes. The body is what every session
// gets; the wrapper around it is the only per-harness difference.
func TestSessionHookStdoutSpeaksEachHarnessOwnEnvelope(t *testing.T) {
	body := "context"
	for runtime, want := range map[string]string{
		agentcfg.RuntimeClaude:   `{"hookSpecificOutput":{"additionalContext":"context","hookEventName":"SessionStart"}}`,
		agentcfg.RuntimeCodex:    `{"hookSpecificOutput":{"additionalContext":"context","hookEventName":"SessionStart"}}`,
		agentcfg.RuntimeCursor:   `{"additional_context":"context"}`,
		agentcfg.RuntimeZcode:    `{"additionalContext":"context"}`,
		agentcfg.RuntimePi:       "context",
		agentcfg.RuntimeOpencode: "context",
	} {
		if got := strings.TrimSpace(sessionHookStdout(runtime, body)); got != want {
			t.Errorf("%s envelope = %s, want %s", runtime, got, want)
		}
	}
	for _, runtime := range []string{
		agentcfg.RuntimeClaude, agentcfg.RuntimeCodex,
		agentcfg.RuntimeCursor, agentcfg.RuntimeZcode,
	} {
		if got := sessionHookStdout(runtime, ""); got != "{}\n" {
			t.Errorf("%s empty envelope = %q, want valid empty JSON", runtime, got)
		}
	}
}

// The craft is not optional. A hook that injected only pills would leave a
// harness guessing how to search, which is the omission this feature closes.
func TestEverySessionHookCarriesTheFixedFragment(t *testing.T) {
	home := skillTestHome(t)
	t.Setenv("ROCA_DB_PATH", filepath.Join(home, "missing", "roca.db"))
	env := &cliEnv{out: &strings.Builder{}, errOut: &strings.Builder{}}
	body := sessionHookBody(t.Context(), env, sessionRequest{pills: true, handoff: true})
	for _, want := range []string{
		"roca vector query", "roca exec", "roca-semantica", "last resort",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("the session fragment does not carry %q: %s", want, body)
		}
	}
}

func mustHookPath(t *testing.T, runtime string) string {
	t.Helper()
	path, err := hookArtifactPath(runtime)
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func hookDocument(t *testing.T, path string) map[string]any {
	t.Helper()
	var document map[string]any
	if err := json.Unmarshal(mustRead(t, path), &document); err != nil {
		t.Fatalf("%s is no longer JSON: %v", path, err)
	}
	return document
}

func nestedHookCommands(t *testing.T, path, event string) []string {
	t.Helper()
	var commands []string
	for _, entry := range hookEvent(t, path, event) {
		group, _ := entry.(map[string]any)
		hooks, _ := group["hooks"].([]any)
		for _, raw := range hooks {
			hook, _ := raw.(map[string]any)
			commands = append(commands, stringMember(hook, "command"))
		}
	}
	return commands
}

func flatHookCommands(t *testing.T, path, event string) []string {
	t.Helper()
	var commands []string
	for _, entry := range hookEvent(t, path, event) {
		object, _ := entry.(map[string]any)
		commands = append(commands, stringMember(object, "command"))
	}
	return commands
}

func hookEvent(t *testing.T, path, event string) []any {
	t.Helper()
	hooks, _ := hookDocument(t, path)["hooks"].(map[string]any)
	entries, _ := hooks[event].([]any)
	return entries
}

func assertCommandCount(t *testing.T, commands []string, want string, times int) {
	t.Helper()
	seen := 0
	for _, command := range commands {
		if command == want {
			seen++
		}
	}
	if seen != times {
		t.Fatalf("command %q occurs %d times, want %d: %v", want, seen, times, commands)
	}
}
