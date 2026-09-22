package cli

import (
	"encoding/json"
	"io"
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

func TestClaudeHookInstallerCreatesIdempotentlyAndRefusesExistingEdits(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "settings.json")
	binary := filepath.Join(home, "bin", "roca")
	for attempt, wantChanged := range []bool{true, false} {
		outcome, err := installClaudeAuthorshipHook(path, binary)
		if err != nil || outcome.Changed != wantChanged || outcome.Backup != "" {
			t.Fatalf("attempt %d: %+v, err %v", attempt, outcome, err)
		}
	}
	preserved := preserveFile(t, path)
	previous := readSettings(t, path)
	outcome, err := installClaudeAuthorshipHook(path, filepath.Join(home, "opt", "roca"))
	requireConditionalRefusal(t, err)
	if outcome.Changed {
		t.Fatal("refused reinstall reported a change")
	}
	preserved()
	requireExactBackup(t, outcome.Backup, previous)
	outcome, warning, err := uninstallClaudeAuthorshipHook(path)
	requireConditionalRefusal(t, err)
	if outcome.Changed || warning != "" {
		t.Fatalf("refused uninstall = %+v, warning %q", outcome, warning)
	}
	preserved()
	requireExactBackup(t, outcome.Backup, previous)

	operator := filepath.Join(home, "operator.json")
	initial := "{\"permissions\":{\"allow\":[\"Read\"]},\"hooks\":{\"PreToolUse\":[{\"matcher\":\"Write\",\"hooks\":[]}]}}"
	writeFile(t, operator, initial)
	preserved = preserveFile(t, operator)
	outcome, err = installClaudeAuthorshipHook(operator, binary)
	requireConditionalRefusal(t, err)
	preserved()
	requireExactBackup(t, outcome.Backup, initial)
}

func TestHookCommandRegistersTheOwnedClaudeFragment(t *testing.T) {
	home := skillTestHome(t)
	binary := filepath.Join(home, "bin", "roca")
	t.Setenv(EnvExecutable, binary)
	var output strings.Builder
	root := rootCommand(&cliEnv{out: &output, build: Build{Version: "v1.2.3"}})
	root.SetArgs([]string{"hooks", "install", "claude"})
	requireConditionalRefusal(t, root.Execute())
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
	requireConditionalRefusal(t, root.Execute())
	if got := readSettings(t, settings); got != edited {
		t.Fatalf("refused install changed the diverged hook: %s", got)
	}

	root = rootCommand(&cliEnv{out: &output, build: Build{Version: "v1.2.3"}})
	root.SetArgs([]string{"hooks", "install", "claude", "--force"})
	requireConditionalRefusal(t, root.Execute())
	if got := readSettings(t, settings); got != edited {
		t.Fatalf("forced refusal changed SYSTEM: %s", got)
	}
	requireExactBackup(t, settings+".roca.bak.1", edited)

	root = rootCommand(&cliEnv{out: &output, build: Build{Version: "v1.2.3"}})
	root.SetArgs([]string{"hooks", "uninstall", "claude"})
	requireConditionalRefusal(t, root.Execute())
	registry, err = artifact.LoadRegistry(filepath.Join(home, ".roca", "artifacts.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := registry.Find("hook", "claude", settings); !ok {
		t.Fatal("refused hook uninstall removed the artifact registration")
	}
}

func TestHookRefreshRefusalPreservesRegisteredAndOperatorBytes(t *testing.T) {
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
			preserved := preserveFile(t, path)
			outcome, err := refreshClaudeHook(path, newBinary, artifact.Checksum(system), true, false)
			requireConditionalRefusal(t, err)
			if outcome.Changed {
				t.Fatal("refused refresh reported a change")
			}
			preserved()
			requireExactBackup(t, path+".roca.bak", test.previous)
		})
	}
}

// With automatic refresh off, update records and reports outdated installs and
// mutates nothing — including under --force-artifacts. Clearing the divergence
// on that path made an edited hook fragment read as merely outdated and left it
// unnamed, while a zoned file under the same two flags still named itself.
func TestADisabledHookRefreshStillReportsDivergence(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "settings.json")
	previous := `{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":` +
		encodedJSONString(t, claudeHookCommand(filepath.Join(home, "operator", "roca"))) + `}]}]}}`
	if err := os.WriteFile(path, []byte(previous), 0o600); err != nil {
		t.Fatal(err)
	}
	registered := artifact.Checksum(`{"command":"what we installed","type":"command"}`)
	out, err := refreshClaudeHook(path, filepath.Join(home, "bin", "roca"), registered, false, true)
	if err != nil {
		t.Fatal(err)
	}
	if !out.Diverged || out.Changed {
		t.Fatalf("a forced refresh behind the off gate = %+v", out)
	}
	if got := readSettings(t, path); got != previous {
		t.Fatalf("a disabled refresh edited the settings: %s", got)
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
	for _, test := range []struct {
		name, body        string
		sessionUnreadable bool
	}{
		{"settings are not JSON", "{not json", true},
		{"hooks is not an object", `{"hooks":"none"}`, true},
		{"PreToolUse is not an array", `{"hooks":{"PreToolUse":"none"}}`, false},
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
			assertClaudeProductWithdrawalWarnings(t, errOut.String(), path, test.sessionUnreadable)
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

func assertClaudeProductWithdrawalWarnings(t *testing.T, warned, path string, sessionUnreadable bool) {
	t.Helper()
	if !strings.Contains(warned, path) || !strings.Contains(warned, "PreToolUse") {
		t.Fatalf("product withdrawal did not name the unreadable signing hook: %q", warned)
	}
	if sessionUnreadable {
		for _, marker := range []string{"hooks.SessionStart", "hooks run claude-pills", "hooks run claude-handoff"} {
			if !strings.Contains(warned, marker) {
				t.Fatalf("product withdrawal warning does not name %q: %q", marker, warned)
			}
		}
	}
}

func TestProductUninstallReportsRefusedSessionWithdrawalAndUnreadablePreToolUse(t *testing.T) {
	home := skillTestHome(t)
	path := filepath.Join(home, ".claude", "settings.json")
	binary := filepath.Join(home, "O'Brien Tools", "roca")
	settings := map[string]any{"hooks": map[string]any{
		"PreToolUse": "unreadable",
		"SessionStart": []any{
			map[string]any{"hooks": []any{map[string]any{"type": "command", "command": claudePillsHookCommand(binary)}}},
			map[string]any{"hooks": []any{map[string]any{"type": "command", "command": claudeHandoffHookCommand(binary)}}},
		},
	}}
	encoded, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, string(encoded))

	preserved := preserveFile(t, path)
	var out, errOut strings.Builder
	env := &cliEnv{out: &out, errOut: &errOut}
	report := lifecycle.Report{Purged: true, Deleted: []string{}}
	env.withdrawTheIntegrations(&report, false)
	preserved()
	if !strings.Contains(strings.Join(report.Errors, "\n"), "atomic conditional replacement is unsupported") {
		t.Fatalf("missing refusal error: %+v", report)
	}
	groups := readClaudeSessionStartHooks(t, path)
	assertHookCommand(t, groups, "", claudePillsHookCommand(binary), 1)
	assertHookCommand(t, groups, "", claudeHandoffHookCommand(binary), 1)
	if report.Purged {
		t.Fatal("refused integration withdrawal was reported as complete")
	}
}

func TestSessionStartHooksRefuseExistingSettingsAndLeaveWithdrawalANoop(t *testing.T) {
	home, binary, path := claudeHookHomeAt(t, "O'Brien Tools")
	foreign := "/opt/acme pill"
	writeFile(t, path,
		`{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"/opt/acme pill"}]}]}}`)

	session := sessionHookCommand(binary, "claude", sessionRequest{pills: true, handoff: true})
	var output strings.Builder
	for range 2 {
		runRefusedHookCLI(t, path, "install", "claude", "--pills", "--handoff")
		settings := readClaudeHookSettings(t, path)
		assertHookCommand(t, settings.Hooks["SessionStart"], "", foreign, 1)
		assertHookCommand(t, settings.Hooks["SessionStart"], "", session, 0)
		assertHookCommand(t, settings.Hooks["PreToolUse"], "Bash", claudeHookCommand(binary), 0)
	}
	registry, err := artifact.LoadRegistry(filepath.Join(home, ".roca", "artifacts.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, found := registry.Find("hook", "claude", path); found {
		t.Fatal("a refused Claude install registered its signing fragment")
	}

	runHookCLI(t, &output, nil, "uninstall", "claude")
	settings := readClaudeHookSettings(t, path)
	assertHookCommand(t, settings.Hooks["SessionStart"], "", foreign, 1)
	assertHookCommand(t, settings.Hooks["SessionStart"], "", session, 0)
	assertHookCommand(t, settings.Hooks["PreToolUse"], "Bash", claudeHookCommand(binary), 0)

	runHookCLI(t, &output, nil, "uninstall", "claude")
	settings = readClaudeHookSettings(t, path)
	assertHookCommand(t, settings.Hooks["SessionStart"], "", foreign, 1)
}

func TestBareClaudeInstallReportsRefusedSessionHookAfterSigningCreation(t *testing.T) {
	_, binary, path := claudeHookHome(t)

	root := rootCommand(&cliEnv{out: io.Discard, errOut: io.Discard})
	root.SetArgs([]string{"hooks", "install", "claude"})
	requireConditionalRefusal(t, root.Execute())
	settings := readClaudeHookSettings(t, path)
	assertHookCommand(t, settings.Hooks["SessionStart"], "",
		sessionHookCommand(binary, "claude", sessionRequest{}), 0)
	assertHookCommand(t, settings.Hooks["PreToolUse"], "Bash", claudeHookCommand(binary), 1)
}

func TestRefusedInstallPreservesSupersededSessionEntries(t *testing.T) {
	_, binary, path := claudeHookHome(t)
	writeFile(t, path, `{"hooks":{"SessionStart":[`+
		`{"hooks":[{"type":"command","command":`+quoteJSON(claudePillsHookCommand(binary))+`}]},`+
		`{"hooks":[{"type":"command","command":`+quoteJSON(claudeHandoffHookCommand(binary))+`}]}]}}`)

	runRefusedHookCLI(t, path, "install", "claude", "--pills")
	settings := readClaudeHookSettings(t, path)
	assertHookCommand(t, settings.Hooks["SessionStart"], "", claudePillsHookCommand(binary), 1)
	assertHookCommand(t, settings.Hooks["SessionStart"], "", claudeHandoffHookCommand(binary), 1)
	assertHookCommand(t, settings.Hooks["SessionStart"], "",
		sessionHookCommand(binary, "claude", sessionRequest{pills: true}), 0)
}

// claudeHookHome is the fixture every Claude hook test opens with: an isolated
// home, the binary the hook will name, and the settings file it is written in.
func claudeHookHome(t *testing.T) (home, binary, path string) {
	t.Helper()
	return claudeHookHomeAt(t, "bin")
}

func claudeHookHomeAt(t *testing.T, dir string) (home, binary, path string) {
	t.Helper()
	home = skillTestHome(t)
	binary = filepath.Join(home, dir, "roca")
	t.Setenv(EnvExecutable, binary)
	return home, binary, filepath.Join(home, ".claude", "settings.json")
}

func quoteJSON(value string) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

func TestSessionHookInstallLeavesADivergedSigningHookAlone(t *testing.T) {
	home, binary, path := claudeHookHome(t)
	var output strings.Builder
	root := rootCommand(&cliEnv{out: &output, errOut: io.Discard})
	root.SetArgs([]string{"hooks", "install", "claude"})
	requireConditionalRefusal(t, root.Execute())
	operatorCommand := claudeHookCommand(filepath.Join(home, "operator", "roca"))
	body := readSettings(t, path)
	body = strings.Replace(body, claudeHookCommand(binary), operatorCommand, 1)
	writeFile(t, path, body)

	var warning strings.Builder
	preserved := preserveFile(t, path)
	root = rootCommand(&cliEnv{out: &output, errOut: &warning})
	root.SetArgs([]string{"hooks", "install", "claude", "--pills", "--handoff"})
	requireConditionalRefusal(t, root.Execute())
	preserved()
	settings := readClaudeHookSettings(t, path)
	assertHookCommand(t, settings.Hooks["PreToolUse"], "Bash", operatorCommand, 1)
	assertHookCommand(t, settings.Hooks["SessionStart"], "",
		sessionHookCommand(binary, "claude", sessionRequest{pills: true, handoff: true}), 0)
	requireExactBackup(t, path+".roca.bak.1", body)
}

// Claude settings this product cannot parse refuse the install rather than
// leave one of its two hooks written and the other not.
func TestInstallRefusesMalformedClaudeHookEvents(t *testing.T) {
	for _, test := range []struct {
		name, body, malformed string
	}{
		{"PreToolUse", `{"hooks":{"PreToolUse":"operator-owned"}}`, "PreToolUse"},
		{"SessionStart", `{"hooks":{"SessionStart":"operator-owned"}}`, "SessionStart"},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := skillTestHome(t)
			t.Setenv(EnvExecutable, filepath.Join(home, "bin", "roca"))
			path := filepath.Join(home, ".claude", "settings.json")
			writeFile(t, path, test.body)

			root := rootCommand(&cliEnv{out: &strings.Builder{}, errOut: &strings.Builder{},
				build: Build{Version: "v1.2.3"}})
			root.SetArgs([]string{"hooks", "install", "claude", "--pills"})
			err := root.Execute()
			if err == nil {
				t.Fatal("an install edited settings it cannot parse")
			}
			if !strings.Contains(err.Error(), path) {
				t.Fatalf("refusal did not name the settings file: %v", err)
			}
			if got := readClaudeHookValue(t, path, test.malformed); got != "operator-owned" {
				t.Fatalf("a refused install changed %s: %#v", test.malformed, got)
			}
			other := "SessionStart"
			if test.malformed == "SessionStart" {
				other = "PreToolUse"
			}
			if readClaudeHookValue(t, path, other) != nil {
				t.Fatal("a refused install wrote half of its hooks")
			}
			if _, err := os.Stat(filepath.Join(home, ".roca", "artifacts.json")); !os.IsNotExist(err) {
				t.Fatal("a refused install registered a hook")
			}
		})
	}
}

func TestUninstallReportsEveryOwnedMarkerOnce(t *testing.T) {
	home := skillTestHome(t)
	path := filepath.Join(home, ".claude", "settings.json")
	writeFile(t, path, `{"hooks":{"SessionStart":"operator-owned"}}`)

	var output, warning strings.Builder
	runHookCLI(t, &output, &warning, "uninstall", "claude")
	for _, marker := range []string{
		"hooks run claude-pills", "hooks run claude-handoff",
		"hooks run session --runtime claude",
	} {
		if !strings.Contains(warning.String(), marker) {
			t.Fatalf("uninstall warning omitted %q: %q", marker, warning.String())
		}
	}
	if lines := strings.Count(strings.TrimSpace(warning.String()), "\n"); lines != 0 {
		t.Fatalf("one unreadable file produced more than one warning: %q", warning.String())
	}
	if got := readClaudeHookValue(t, path, "SessionStart"); got != "operator-owned" {
		t.Fatalf("uninstall changed unreadable SessionStart settings: %#v", got)
	}
}

func runHookCLI(t *testing.T, output, warnings *strings.Builder, args ...string) {
	t.Helper()
	var errOut io.Writer = io.Discard
	if warnings != nil {
		errOut = warnings
	}
	root := rootCommand(&cliEnv{
		out: output, errOut: errOut, build: Build{Version: "v1.2.3"},
	})
	root.SetArgs(append([]string{"hooks"}, args...))
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
}

type claudeHookSettingsModel struct {
	Hooks map[string][]claudeHookGroup `json:"hooks"`
}

type claudeHookGroup struct {
	Matcher string              `json:"matcher"`
	Hooks   []claudeCommandHook `json:"hooks"`
}

type claudeCommandHook struct {
	Type    string `json:"type"`
	Command string `json:"command"`
}

func readClaudeSessionStartHooks(t *testing.T, path string) []claudeHookGroup {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var settings struct {
		Hooks struct {
			SessionStart []claudeHookGroup `json:"SessionStart"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(body, &settings); err != nil {
		t.Fatalf("settings are no longer valid Claude SessionStart settings: %v", err)
	}
	return settings.Hooks.SessionStart
}

func readClaudeHookValue(t *testing.T, path, event string) any {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var settings struct {
		Hooks map[string]any `json:"hooks"`
	}
	if err := json.Unmarshal(body, &settings); err != nil {
		t.Fatalf("settings are no longer valid Claude hook settings: %v", err)
	}
	return settings.Hooks[event]
}

func readClaudeHookSettings(t *testing.T, path string) claudeHookSettingsModel {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var settings claudeHookSettingsModel
	if err := json.Unmarshal(body, &settings); err != nil {
		t.Fatalf("settings are no longer valid Claude hook settings: %v", err)
	}
	return settings
}

func assertHookCommand(t *testing.T, groups []claudeHookGroup, matcher, command string, want int) {
	t.Helper()
	got := 0
	for _, group := range groups {
		if group.Matcher != matcher {
			continue
		}
		for _, hook := range group.Hooks {
			if hook.Type == "command" && hook.Command == command {
				got++
			}
		}
	}
	if got != want {
		t.Fatalf("hook command %q with matcher %q occurs %d times, want %d: %+v", command, matcher, got, want, groups)
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
