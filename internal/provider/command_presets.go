package provider

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
}

// CommandPresetNames returns shipped command providers in stable display order.
func CommandPresetNames() []string { return []string{NameClaude} }
