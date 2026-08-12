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
	command := "roca store --agent claude --model opus --layer handoff --content note"
	if signed := signRocaStoreCommand(command, "sonnet"); signed != command {
		t.Errorf("hook changed explicit identity flags: %s", signed)
	}
	if signed := signRocaStoreCommand(`echo 'roca store --layer handoff'`, "sonnet"); signed != `echo 'roca store --layer handoff'` {
		t.Errorf("hook changed text that is not an invocation: %s", signed)
	}
	command = `roca store --layer handoff --content '--agent is documentation'`
	if signed := signRocaStoreCommand(command, "sonnet"); !strings.Contains(signed, "--agent claude") {
		t.Errorf("quoted content hid the missing agent flag: %s", signed)
	}
}

func TestClaudeHookInstallerPreservesSettingsAndIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	initial := `{"permissions":{"allow":["Read"]},"hooks":{"PreToolUse":[{"matcher":"Write","hooks":[]}]}}`
	if err := os.WriteFile(path, []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}
	for attempt, wantChanged := range []bool{true, false} {
		outcome, err := installClaudeAuthorshipHook(path)
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
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var settings map[string]any
	if err := json.Unmarshal(body, &settings); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"permissions"`) || strings.Count(string(body), "roca hooks run claude") != 1 {
		t.Errorf("installer lost existing settings or duplicated its hook: %s", body)
	}

	root := rootCommand(&cliEnv{})
	command, _, err := root.Find([]string{"hooks", "install", "claude"})
	if err != nil || command == nil {
		t.Fatalf("roca hooks install claude is unavailable: %v", err)
	}
}
