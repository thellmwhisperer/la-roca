package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
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

	outcome, err := uninstallClaudeAuthorshipHook(path)
	if err != nil {
		t.Fatal(err)
	}
	if !outcome.Changed {
		t.Error("uninstall left the signing hook behind")
	}
	body = readSettings(t, path)
	if strings.Contains(body, "hooks run claude") || !strings.Contains(body, `"permissions"`) ||
		!strings.Contains(body, `"Write"`) {
		t.Errorf("uninstall did not withdraw exactly its own hook: %s", body)
	}
	if again, err := uninstallClaudeAuthorshipHook(path); err != nil || again.Changed {
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
