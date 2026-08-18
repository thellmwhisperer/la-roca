package provider

import (
	"os"
	"os/exec"
	"path/filepath"
)

// DefaultCodexModel is the model used by the shipped Codex CLI command.
const DefaultCodexModel = "gpt-5.6-luna"

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

// LookPath resolves an executable on PATH, then in the user's well-known
// bins (HOME/.local/bin, HOME/bin). A PATH miss alone is not "not installed":
// cursor-agent on this machine lives at ~/.local/bin and is missed when that
// directory is absent from PATH. Host-global bins stay on PATH; they are not
// searched here so an isolated HOME cannot see the machine's Homebrew tools.
func LookPath(name string) (string, error) {
	path, err := exec.LookPath(name)
	if err == nil {
		return path, nil
	}
	if filepath.Base(name) != name {
		return "", err
	}
	home := os.Getenv("HOME")
	if home == "" {
		if userHome, err := os.UserHomeDir(); err == nil {
			home = userHome
		}
	}
	if home == "" {
		return "", &exec.Error{Name: name, Err: exec.ErrNotFound}
	}
	for _, dir := range []string{
		filepath.Join(home, ".local", "bin"),
		filepath.Join(home, "bin"),
	} {
		path := filepath.Join(dir, name)
		info, err := os.Stat(path)
		if err != nil || info.IsDir() || info.Mode()&0o111 == 0 {
			continue
		}
		return path, nil
	}
	return "", &exec.Error{Name: name, Err: exec.ErrNotFound}
}

// BinaryOnPath reports whether an executable resolves through PATH or a
// well-known bin directory.
func BinaryOnPath(lookPath LookPathFunc, name string) bool {
	if lookPath == nil {
		lookPath = LookPath
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
