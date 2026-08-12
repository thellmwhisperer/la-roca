package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/cobra"
	"github.com/thellmwhisperer/la-roca/internal/distribution/agentcfg"
	"github.com/thellmwhisperer/la-roca/internal/distribution/skill"
	"github.com/thellmwhisperer/la-roca/internal/provider/service"
)

var rocaStoreInvocation = regexp.MustCompile(
	`(?:^|(?:&&|\|\||;|\n|\|)[ \t]*)(?:[^ \t;&|\n]*/)?roca[ \t]+store\b`,
)

// claudeHookInvocation recognizes La Roca's own PreToolUse entry whatever binary
// path it was installed with, so a reinstall repoints it and an uninstall finds
// it even after the operator moved the executable.
var claudeHookInvocation = regexp.MustCompile(
	`^(?:'[^']*'|"[^"]*"|\S+)[ \t]+hooks[ \t]+run[ \t]+claude$`,
)

func claudeHookCommand(executable string) string {
	return shellQuote(executable) + " hooks run claude"
}

// skillCommand installs the canonical agent skill that teaches runtimes how to
// use La Roca. Hidden plumbing: bare lists destinations; install writes one
// file per runtime and narrates every path.
func skillCommand(env *cliEnv) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "skill",
		Short: "Install the agent skill that teaches runtimes how to use La Roca",
		Long: "One embedded SKILL.md, copied into each runtime's personal skills\n" +
			"directory. Nothing else is edited.\n\n" +
			"Supported runtimes: " + strings.Join(skill.Runtimes(), ", "),
		Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			return env.listSkillDestinations()
		},
	}
	cmd.AddCommand(skillInstallCommand(env))
	return cmd
}

func skillInstallCommand(env *cliEnv) *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:   "install [runtime]",
		Short: "Write the roca skill into one runtime, or every supported one",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if all == (len(args) == 1) {
				return fmt.Errorf("name one runtime (%s) or ask for --all",
					strings.Join(skill.Runtimes(), ", "))
			}
			runtimes := args
			if all {
				runtimes = skill.Runtimes()
			}
			outcomes := make([]skill.Outcome, 0, len(runtimes))
			for _, runtime := range runtimes {
				path, err := skillFileOf(runtime)
				if err != nil {
					return err
				}
				outcome, err := skill.Install(runtime, path)
				if err != nil {
					return err
				}
				outcomes = append(outcomes, outcome)
			}
			if env.json {
				return env.printJSON(map[string]any{"runtimes": outcomes})
			}
			for _, o := range outcomes {
				verb := "unchanged"
				if o.Changed {
					verb = "wrote"
				}
				env.print("%s: %s %s", o.Runtime, verb, o.Path)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "install into every supported runtime")
	return cmd
}

func (env *cliEnv) listSkillDestinations() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("I do not know where your HOME is")
	}
	type row struct {
		Runtime string `json:"runtime"`
		Path    string `json:"path"`
	}
	rows := make([]row, 0, len(skill.Runtimes()))
	for _, runtime := range skill.Runtimes() {
		path, err := skill.Path(runtime, home, os.Getenv)
		if err != nil {
			return err
		}
		rows = append(rows, row{Runtime: runtime, Path: path})
	}
	if env.json {
		return env.printJSON(map[string]any{"runtimes": rows})
	}
	toonRows := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		toonRows = append(toonRows, map[string]any{"runtime": r.Runtime, "path": r.Path})
	}
	env.print("%s", rowOutput([]string{"runtime", "path"}, toonRows))
	env.print("%s", renderHelp(
		"Run `roca skill install <runtime>` to install one destination",
		"Run `roca skill install --all` to install every destination"))
	return nil
}

func skillFileOf(runtime string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("I do not know where your HOME is")
	}
	return skill.Path(runtime, home, os.Getenv)
}

func hooksCommand(env *cliEnv) *cobra.Command {
	command := &cobra.Command{
		Use:   "hooks",
		Short: "Install and withdraw client-side authorship signing hooks",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	command.AddCommand(
		hooksInstallCommand(env), hooksUninstallCommand(env), hooksRunCommand(env),
	)
	return command
}

// hooksEditCommand is the shape both `hooks install` and `hooks uninstall`
// have: one supported runtime, one settings file, one rendered outcome.
func hooksEditCommand(env *cliEnv, use, short, verb string,
	edit func(path string) (agentcfg.Outcome, error)) *cobra.Command {
	return &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if err := supportedHookRuntime(args[0]); err != nil {
				return err
			}
			path, err := claudeSettingsPath()
			if err != nil {
				return err
			}
			outcome, err := edit(path)
			if err != nil {
				return err
			}
			return env.renderOutcome(outcome, verb)
		},
	}
}

func hooksInstallCommand(env *cliEnv) *cobra.Command {
	var executable string
	cmd := hooksEditCommand(env, "install [runtime]",
		"Install the Claude Code roca-store signing hook", "updated",
		func(path string) (agentcfg.Outcome, error) {
			return installClaudeAuthorshipHook(path, executable)
		})
	cmd.Flags().StringVar(&executable, "executable", "",
		"the binary the hook launches (default: this executable; override with "+EnvExecutable+")")
	return cmd
}

func hooksUninstallCommand(env *cliEnv) *cobra.Command {
	return hooksEditCommand(env, "uninstall [runtime]",
		"Withdraw the Claude Code roca-store signing hook, leaving the rest of the settings as they were",
		"withdrawn", uninstallClaudeAuthorshipHook)
}

func supportedHookRuntime(name string) error {
	if name != "claude" {
		return fmt.Errorf("unsupported hook runtime %q (want claude)", name)
	}
	return nil
}

func hooksRunCommand(env *cliEnv) *cobra.Command {
	command := &cobra.Command{
		Use:    "run [runtime]",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := supportedHookRuntime(args[0]); err != nil {
				return err
			}
			input, err := io.ReadAll(cmd.InOrStdin())
			if err != nil {
				return fmt.Errorf("read Claude hook input: %w", err)
			}
			output, err := runClaudeAuthorshipHook(input)
			if err != nil {
				return err
			}
			if len(output) > 0 {
				fmt.Fprintln(env.out, string(output))
			}
			return nil
		},
	}
	return command
}

func claudeSettingsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("I do not know where your HOME is")
	}
	root := os.Getenv("CLAUDE_CONFIG_DIR")
	if root == "" {
		root = filepath.Join(home, ".claude")
	}
	return filepath.Join(root, "settings.json"), nil
}

// The hook is intentionally one hard-coded Claude artifact. Versioned hook and
// skill lifecycle belongs to issue #58; this feature does not grow that registry.
//
// The entry launches the absolute path of this executable, the way `roca mcp
// install` declares the server: Claude runs a PreToolUse hook in a
// non-interactive shell, where a bare `roca` is whatever PATH happens to hold.
func installClaudeAuthorshipHook(path, executable string) (agentcfg.Outcome, error) {
	declared := chosenExecutable(executable)
	if !filepath.IsAbs(declared) {
		return agentcfg.Outcome{Runtime: "claude", Path: path},
			fmt.Errorf("resolve the running executable %q to an absolute path", declared)
	}
	command := claudeHookCommand(declared)
	return agentcfg.Edit("claude", path, func(previous string) (string, error) {
		settings, err := claudeSettings(previous)
		if err != nil {
			return "", err
		}
		hooks, ok := settings["hooks"].(map[string]any)
		if settings["hooks"] != nil && !ok {
			return "", fmt.Errorf("Claude settings hooks must be an object")
		}
		if hooks == nil {
			hooks = map[string]any{}
			settings["hooks"] = hooks
		}
		entries, ok := hooks["PreToolUse"].([]any)
		if hooks["PreToolUse"] != nil && !ok {
			return "", fmt.Errorf("Claude settings hooks.PreToolUse must be an array")
		}
		found, repointed := adoptClaudeAuthorshipHook(entries, command)
		if found && !repointed {
			return previous, nil
		}
		if !found {
			entries = append(entries, map[string]any{
				"matcher": "Bash",
				"hooks":   []any{map[string]any{"type": "command", "command": command}},
			})
		}
		hooks["PreToolUse"] = entries
		return encodeClaudeSettings(settings)
	}, true)
}

// uninstallClaudeAuthorshipHook takes the PreToolUse entry back out and leaves
// every other setting, and every hook that is not La Roca's, exactly as it was.
// A settings file that is not there is not created and not an error.
func uninstallClaudeAuthorshipHook(path string) (agentcfg.Outcome, error) {
	return agentcfg.Edit("claude", path, func(previous string) (string, error) {
		settings, err := claudeSettings(previous)
		if err != nil {
			return "", err
		}
		hooks, ok := settings["hooks"].(map[string]any)
		if !ok {
			return previous, nil
		}
		entries, ok := hooks["PreToolUse"].([]any)
		if !ok {
			return previous, nil
		}
		remaining, withdrawn := withoutClaudeAuthorshipHook(entries)
		if !withdrawn {
			return previous, nil
		}
		if len(remaining) == 0 {
			delete(hooks, "PreToolUse")
		} else {
			hooks["PreToolUse"] = remaining
		}
		if len(hooks) == 0 {
			delete(settings, "hooks")
		}
		return encodeClaudeSettings(settings)
	}, false)
}

func claudeSettings(previous string) (map[string]any, error) {
	settings := map[string]any{}
	if strings.TrimSpace(previous) == "" {
		return settings, nil
	}
	if err := json.Unmarshal([]byte(previous), &settings); err != nil {
		return nil, fmt.Errorf("read Claude settings: %w", err)
	}
	if settings == nil {
		return nil, fmt.Errorf("Claude settings must be an object")
	}
	return settings, nil
}

func encodeClaudeSettings(settings map[string]any) (string, error) {
	encoded, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode Claude settings: %w", err)
	}
	return string(append(encoded, '\n')), nil
}

// adoptClaudeAuthorshipHook repoints an entry this product already installed at
// the currently resolved binary, so reinstalling after a move heals the command
// instead of leaving a second one beside it.
func adoptClaudeAuthorshipHook(entries []any, command string) (found, repointed bool) {
	for _, entry := range entries {
		for _, hook := range commandHooksOf(entry) {
			if !claudeHookInvocation.MatchString(commandOf(hook)) {
				continue
			}
			found = true
			if commandOf(hook) != command {
				hook["command"] = command
				repointed = true
			}
		}
	}
	return found, repointed
}

func withoutClaudeAuthorshipHook(entries []any) ([]any, bool) {
	remaining := make([]any, 0, len(entries))
	withdrawn := false
	for _, entry := range entries {
		group, ok := entry.(map[string]any)
		hooks, isList := group["hooks"].([]any)
		if !ok || !isList {
			remaining = append(remaining, entry)
			continue
		}
		kept := make([]any, 0, len(hooks))
		ours := false
		for _, raw := range hooks {
			hook, isHook := raw.(map[string]any)
			if isHook && hook["type"] == "command" &&
				claudeHookInvocation.MatchString(commandOf(hook)) {
				ours, withdrawn = true, true
				continue
			}
			kept = append(kept, raw)
		}
		if ours && len(kept) == 0 {
			continue
		}
		group["hooks"] = kept
		remaining = append(remaining, group)
	}
	return remaining, withdrawn
}

func commandHooksOf(entry any) []map[string]any {
	group, ok := entry.(map[string]any)
	if !ok {
		return nil
	}
	hooks, _ := group["hooks"].([]any)
	commands := make([]map[string]any, 0, len(hooks))
	for _, raw := range hooks {
		if hook, ok := raw.(map[string]any); ok && hook["type"] == "command" {
			commands = append(commands, hook)
		}
	}
	return commands
}

func commandOf(hook map[string]any) string {
	command, _ := hook["command"].(string)
	return command
}

func runClaudeAuthorshipHook(input []byte) ([]byte, error) {
	var event struct {
		HookEventName string         `json:"hook_event_name"`
		ToolName      string         `json:"tool_name"`
		Transcript    string         `json:"transcript_path"`
		ToolInput     map[string]any `json:"tool_input"`
	}
	if err := json.Unmarshal(input, &event); err != nil {
		return nil, fmt.Errorf("decode Claude hook input: %w", err)
	}
	command, _ := event.ToolInput["command"].(string)
	if event.HookEventName != "PreToolUse" || event.ToolName != "Bash" ||
		!rocaStoreInvocation.MatchString(command) {
		return nil, nil
	}
	model := claudeTranscriptModel(event.Transcript)
	if model == "" {
		model = service.UnknownAuthor
	}
	signed := signRocaStoreCommand(command, model)
	if signed == command {
		return nil, nil
	}
	event.ToolInput["command"] = signed
	return json.Marshal(map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName": "PreToolUse", "updatedInput": event.ToolInput,
		},
	})
}

func signRocaStoreCommand(command, model string) string {
	location := rocaStoreInvocation.FindStringIndex(command)
	if location == nil {
		return command
	}
	// The segment ends at the first separator the shell would honour, found by a
	// scan that starts at the beginning of the line: a `||` inside a quoted
	// value is text, and cutting the segment there hid the flags behind it.
	end := scanUnquoted(command, func(index int) bool {
		return index >= location[1] && shellSeparatorAt(command[index:])
	})
	if end < 0 {
		end = len(command)
	}
	segment := command[location[0]:end]
	flags := ""
	if !hasUnquotedFlag(segment, "--agent") {
		flags += " --agent claude"
	}
	if !hasUnquotedFlag(segment, "--model") {
		flags += " --model " + shellQuote(model)
	}
	return command[:location[1]] + flags + command[location[1]:]
}

func shellSeparatorAt(rest string) bool {
	for _, separator := range []string{"&&", "||", ";", "\n", "|"} {
		if strings.HasPrefix(rest, separator) {
			return true
		}
	}
	return false
}

func hasUnquotedFlag(command, flag string) bool {
	return scanUnquoted(command, func(index int) bool {
		if index > 0 && command[index-1] != ' ' && command[index-1] != '\t' {
			return false
		}
		if !strings.HasPrefix(command[index:], flag) {
			return false
		}
		after := index + len(flag)
		return after == len(command) || command[after] == '=' ||
			command[after] == ' ' || command[after] == '\t'
	}) >= 0
}

// scanUnquoted walks the command the way a shell reads it and returns the first
// offset outside quoting that `found` accepts, or -1. One scan owns the quoting
// rules for every question asked about a command line.
func scanUnquoted(command string, found func(index int) bool) int {
	var quote byte
	for i := 0; i < len(command); i++ {
		current := command[i]
		if quote != 0 {
			if current == quote {
				quote = 0
			} else if quote == '"' && current == '\\' {
				i++
			}
			continue
		}
		switch current {
		case '\'', '"':
			quote = current
		case '\\':
			i++
		default:
			if found(i) {
				return i
			}
		}
	}
	return -1
}

func claudeTranscriptModel(path string) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()
	reader := bufio.NewReader(file)
	var model string
	for {
		line, readErr := reader.ReadBytes('\n')
		var record struct {
			Message struct {
				Model string `json:"model"`
			} `json:"message"`
		}
		if json.Unmarshal(line, &record) == nil && strings.TrimSpace(record.Message.Model) != "" {
			model = strings.TrimSpace(record.Message.Model)
		}
		if readErr != nil {
			break
		}
	}
	return model
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}
