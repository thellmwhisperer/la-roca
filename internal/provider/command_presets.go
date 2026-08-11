package provider

import "os/exec"

// CommandPreset is shipped provider configuration for a local CLI. The
// adapter does not branch on these names: adding another built-in is one row,
// and an operator's provider table overrides every executable setting.
type CommandPreset struct {
	Command        []string
	Model          string
	Models         []string
	TimeoutSeconds int
	Action         string
	ResponseFormat string
}

var commandPresets = map[string]CommandPreset{
	NameClaude: {
		Command: []string{
			"claude", "-p", "--output-format", "json", "--model", "{model}",
			"--safe-mode", "--strict-mcp-config", "--mcp-config", `{"mcpServers":{}}`,
			"--tools", "", "--disable-slash-commands", "--no-session-persistence", "--no-chrome",
		},
		Model:          "sonnet",
		Models:         []string{"sonnet", "opus", "haiku"},
		TimeoutSeconds: 120,
		Action:         "install Claude Code or put `claude` on PATH",
		ResponseFormat: binaryResponseJSON,
	},
	NameCodex: {
		Command: []string{
			"codex", "exec", "--model", "{model}",
			"--sandbox", "read-only", "--ephemeral", "--skip-git-repo-check",
			"--ignore-user-config", "--ignore-rules", "--color", "never", "-",
		},
		Model:          DefaultCodexModel,
		Models:         []string{DefaultCodexModel},
		TimeoutSeconds: 120,
		Action:         "install Codex CLI or put `codex` on PATH",
	},
}

// CommandPresetNames returns shipped command providers in stable display order.
func CommandPresetNames() []string { return []string{NameClaude, NameCodex} }

// CommandPresetDefaultModel returns the one model a detected CLI can offer
// without inventing a catalogue the CLI itself does not expose.
func CommandPresetDefaultModel(name string) (string, bool) {
	preset, ok := commandPresets[name]
	return preset.Model, ok
}

// LookPathFunc is the platform-aware executable lookup used by factory-default
// detection. Tests and reconciliation can provide the same observable PATH
// without duplicating detection rules.
type LookPathFunc func(string) (string, error)

// BinaryOnPath reports whether an executable resolves through PATH.
func BinaryOnPath(lookPath LookPathFunc, name string) bool {
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	_, err := lookPath(name)
	return err == nil
}

// DetectedCommandPresets returns shipped local CLI providers whose executable
// is on PATH, in stable factory-preference order.
func DetectedCommandPresets(lookPath LookPathFunc) []string {
	var detected []string
	for _, name := range CommandPresetNames() {
		if BinaryOnPath(lookPath, commandPresets[name].Command[0]) {
			detected = append(detected, name)
		}
	}
	return detected
}

func MissingCommandPresets(detected []string) []string {
	present := make(map[string]bool, len(detected))
	for _, name := range detected {
		present[name] = true
	}
	var missing []string
	for _, name := range CommandPresetNames() {
		if !present[name] {
			missing = append(missing, name)
		}
	}
	return missing
}
