package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/artifact"
	"github.com/thellmwhisperer/la-roca/internal/distribution/lifecycle"
)

func TestClaudeHookSignsRocaStoreFromTheTranscriptIdentity(t *testing.T) {
	transcript := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(transcript, []byte(
		`{"message":{"role":"assistant","model":"claude-sonnet-4-6"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name       string
		command    string
		transcript string
		want       []string
	}{
		{"detected model", "roca store --layer handoff --content note", transcript, []string{"--agent claude", "--model 'claude-sonnet-4-6'"}},
		{"unknown model", "roca store --layer handoff --content note", filepath.Join(t.TempDir(), "missing"), []string{"--agent claude", "--model 'unknown'"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input, _ := json.Marshal(map[string]any{
				"hook_event_name": "PreToolUse", "tool_name": "Bash",
				"transcript_path": test.transcript,
				"tool_input":      map[string]any{"command": test.command, "timeout": 5000},
			})
			output, err := runClaudeAuthorshipHook(input)
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range test.want {
				if !strings.Contains(string(output), want) {
					t.Errorf("hook output does not contain %q: %s", want, output)
				}
			}
			if strings.Count(string(output), "--agent") != 1 || strings.Count(string(output), "--model") != 1 {
				t.Errorf("hook duplicated identity flags: %s", output)
			}
		})
	}
	untouched := []string{
		"roca store --agent claude --model opus --layer handoff --content note",
		// A separator inside a quoted value is text: cutting the segment there
		// hid the explicit flags that follow it and duplicated them.
		`roca store --content "a || b" --agent codex --model gpt-5`,
		`echo 'roca store --layer handoff'`,
	}
	for _, command := range untouched {
		if signed := signRocaStoreCommand(command, "sonnet"); signed != command {
			t.Errorf("hook rewrote a command it should have left alone: %s", signed)
		}
	}
	command := `roca store --layer handoff --content '--agent is documentation'`
	if signed := signRocaStoreCommand(command, "sonnet"); !strings.Contains(signed, "--agent claude") {
		t.Errorf("quoted content hid the missing agent flag: %s", signed)
	}
	command = `roca store --content "a || b" --layer handoff && echo done`
	signed := signRocaStoreCommand(command, "sonnet")
	if strings.Count(signed, "--agent") != 1 || strings.Count(signed, "--model") != 1 {
		t.Errorf("hook duplicated identity flags across a quoted separator: %s", signed)
	}
}

func TestClaudeHookInstallerPreservesSettingsAndIsIdempotent(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "settings.json")
	binary := filepath.Join(home, "bin", "roca")
	initial := `{"permissions":{"allow":["Read"]},"hooks":{"PreToolUse":[{"matcher":"Write","hooks":[]}]}}`
	if err := os.WriteFile(path, []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}
	for attempt, wantChanged := range []bool{true, false} {
		outcome, err := installClaudeAuthorshipHook(path, binary)
		if err != nil {
			t.Fatal(err)
		}
		if outcome.Changed != wantChanged {
			t.Errorf("attempt %d changed = %v, want %v", attempt+1, outcome.Changed, wantChanged)
		}
		if attempt == 0 && outcome.Backup == "" {
			t.Error("installer replaced settings without a recovery backup")
		}
	}
	body := readSettings(t, path)
	// The hook runs in Claude's non-interactive shell, where a bare `roca` is
	// whatever PATH happens to hold, so the entry names this binary in full.
	if !strings.Contains(body, `"permissions"`) ||
		strings.Count(body, shellQuote(binary)+" hooks run claude") != 1 {
		t.Errorf("installer lost existing settings or did not name the binary once: %s", body)
	}

	moved := filepath.Join(home, "opt", "roca")
	if _, err := installClaudeAuthorshipHook(path, moved); err != nil {
		t.Fatal(err)
	}
	body = readSettings(t, path)
	if strings.Contains(body, binary) ||
		strings.Count(body, shellQuote(moved)+" hooks run claude") != 1 {
		t.Errorf("reinstall left the hook pointing at a binary that moved: %s", body)
	}

	outcome, warning, err := uninstallClaudeAuthorshipHook(path)
	if err != nil {
		t.Fatal(err)
	}
	if !outcome.Changed || warning != "" {
		t.Errorf("uninstall left the signing hook behind or warned about its own file: %q", warning)
	}
	body = readSettings(t, path)
	if strings.Contains(body, "hooks run claude") || !strings.Contains(body, `"permissions"`) ||
		!strings.Contains(body, `"Write"`) {
		t.Errorf("uninstall did not withdraw exactly its own hook: %s", body)
	}
	if again, _, err := uninstallClaudeAuthorshipHook(path); err != nil || again.Changed {
		t.Errorf("uninstall is not idempotent: changed=%v err=%v", again.Changed, err)
	}

	root := rootCommand(&cliEnv{})
	for _, verb := range []string{"install", "uninstall"} {
		command, _, err := root.Find([]string{"hooks", verb, "claude"})
		if err != nil || command == nil {
			t.Fatalf("roca hooks %s claude is unavailable: %v", verb, err)
		}
	}
}

func TestHookCommandRegistersTheOwnedClaudeFragment(t *testing.T) {
	home := skillTestHome(t)
	binary := filepath.Join(home, "bin", "roca")
	t.Setenv(EnvExecutable, binary)
	var output strings.Builder
	root := rootCommand(&cliEnv{out: &output, build: Build{Version: "v1.2.3"}})
	root.SetArgs([]string{"hooks", "install", "claude"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	settings := filepath.Join(home, ".claude", "settings.json")
	registry, err := artifact.LoadRegistry(filepath.Join(home, ".roca", "artifacts.json"))
	if err != nil {
		t.Fatal(err)
	}
	entry, ok := registry.Find("hook", "claude", settings)
	if !ok || entry.InstalledVersion != "v1.2.3" || entry.SystemSHA256 == "" {
		t.Fatalf("registered hook = %+v, found %v", entry, ok)
	}

	body, err := os.ReadFile(settings)
	if err != nil {
		t.Fatal(err)
	}
	operatorBinary := filepath.Join(home, "operator", "roca")
	edited := strings.Replace(string(body), claudeHookCommand(binary),
		claudeHookCommand(operatorBinary), 1)
	if err := os.WriteFile(settings, []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}
	var warning strings.Builder
	root = rootCommand(&cliEnv{out: &output, errOut: &warning, build: Build{Version: "v1.2.3"}})
	root.SetArgs([]string{"hooks", "install", "claude"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if got := readSettings(t, settings); !strings.Contains(got, operatorBinary) ||
		!strings.Contains(warning.String(), "hooks install claude --force") {
		t.Fatalf("diverged hook was overwritten or not warned: body=%s warning=%q", got, warning.String())
	}

	root = rootCommand(&cliEnv{out: &output, build: Build{Version: "v1.2.3"}})
	root.SetArgs([]string{"hooks", "install", "claude", "--force"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if got := readSettings(t, settings); strings.Contains(got, operatorBinary) || !strings.Contains(got, binary) {
		t.Fatalf("forced hook refresh did not restore SYSTEM: %s", got)
	}

	root = rootCommand(&cliEnv{out: &output, build: Build{Version: "v1.2.3"}})
	root.SetArgs([]string{"hooks", "uninstall", "claude"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	registry, err = artifact.LoadRegistry(filepath.Join(home, ".roca", "artifacts.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := registry.Find("hook", "claude", settings); ok {
		t.Fatal("explicit hook uninstall left the artifact registered")
	}
}

// The refresh rewrites the bytes of the command it registered and no others:
// the operator's numeric spelling, spacing and trailing members survive. The
// reader only ever looks inside PreToolUse, so the edit looks there too, and an
// operator who declared the identical command under another event owns those
// bytes: they are neither rewritten nor a reason to refuse the refresh.
func TestHookRefreshChangesOnlyItsRegisteredCommandBytes(t *testing.T) {
	home := t.TempDir()
	oldBinary := filepath.Join(home, "old", "roca")
	newBinary := filepath.Join(home, "new", "roca")
	oldCommand := encodedJSONString(t, claudeHookCommand(oldBinary))
	entry := `[{"matcher":"Bash","hooks":[{"type":"command","command":` + oldCommand + `}]}]`
	for _, test := range []struct{ name, previous string }{
		{"operator bytes around the entry",
			`{"numeric_spelling":1e3,"hooks":{"PreToolUse":` + entry + `},"tail":"  keep  "}`},
		{"the same command under another event",
			`{"hooks":{"PreToolUse":` + entry + `,"PostToolUse":` + entry + `}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "settings.json")
			if err := os.WriteFile(path, []byte(test.previous), 0o640); err != nil {
				t.Fatal(err)
			}
			system, found, err := claudeHookSystem(path)
			if err != nil || !found {
				t.Fatalf("read installed hook: found=%v err=%v", found, err)
			}
			outcome, err := refreshClaudeHook(path, newBinary, artifact.Checksum(system), true, false)
			if err != nil {
				t.Fatalf("an operator's own bytes blocked the refresh: %v", err)
			}
			want := strings.Replace(test.previous, oldCommand,
				encodedJSONString(t, claudeHookCommand(newBinary)), 1)
			if got := readSettings(t, path); !outcome.Changed || got != want {
				t.Fatalf("refresh did not edit exactly its own registered command:\nwant %s\n got %s", want, got)
			}
		})
	}
}

func encodedJSONString(t *testing.T, value string) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

// Settings La Roca did not write are two different questions. Installing into
// them is refused, because an installer that cannot read a file cannot edit it
// safely. Withdrawing from them is not: an operator removing this product must
// never be held hostage by a file the product never owned, so the withdrawal
// changes nothing, succeeds, and names what to take out by hand.
func TestMalformedClaudeSettingsRefuseInstallAndNeverBlockWithdrawal(t *testing.T) {
	for _, test := range []struct{ name, body string }{
		{"settings are not JSON", "{not json"},
		{"hooks is not an object", `{"hooks":"none"}`},
		{"PreToolUse is not an array", `{"hooks":{"PreToolUse":"none"}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := skillTestHome(t)
			path := filepath.Join(home, ".claude", "settings.json")
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(test.body), 0o600); err != nil {
				t.Fatal(err)
			}

			if _, err := installClaudeAuthorshipHook(path, filepath.Join(home, "bin", "roca")); err == nil {
				t.Fatal("install edited settings it could not read")
			}

			var out, errOut strings.Builder
			env := &cliEnv{out: &out, errOut: &errOut}
			root := rootCommand(env)
			root.SetArgs([]string{"hooks", "uninstall", "claude"})
			if err := root.Execute(); err != nil {
				t.Fatalf("withdrawal refused to run over foreign settings: %v", err)
			}
			assertClaudeWithdrawalWarning(t, errOut.String(), path)

			errOut.Reset()
			report := lifecycle.Report{Purged: true, Deleted: []string{}}
			env.withdrawTheIntegrations(&report, false)
			assertClaudeWithdrawalWarning(t, errOut.String(), path)
			for _, failure := range report.Errors {
				if strings.Contains(failure, "signing hook") {
					t.Errorf("foreign settings blocked the uninstall: %s", failure)
				}
			}

			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(body) != test.body {
				t.Errorf("withdrawal rewrote settings it could not read: %s", body)
			}
		})
	}
}

func assertClaudeWithdrawalWarning(t *testing.T, warned, path string) {
	t.Helper()
	if count := strings.Count(warned, "warning:"); count != 1 {
		t.Fatalf("want exactly one warning line, got %d: %q", count, warned)
	}
	if !strings.Contains(warned, path) || !strings.Contains(warned, "hooks run claude") ||
		!strings.Contains(warned, "PreToolUse") {
		t.Fatalf("the warning does not name the file and the entry to remove: %q", warned)
	}
}

func readSettings(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var settings map[string]any
	if err := json.Unmarshal(body, &settings); err != nil {
		t.Fatalf("settings are no longer JSON: %v", err)
	}
	return string(body)
}
