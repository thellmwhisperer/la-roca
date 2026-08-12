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

const claudeHookCommand = "roca hooks run claude"

var rocaStoreInvocation = regexp.MustCompile(
	`(?:^|(?:&&|\|\||;|\n|\|)[ \t]*)(?:[^ \t;&|\n]*/)?roca[ \t]+store\b`,
)

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
		Short: "Install client-side authorship signing hooks",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	command.AddCommand(hooksInstallCommand(env), hooksRunCommand(env))
	return command
}

func hooksInstallCommand(env *cliEnv) *cobra.Command {
	return &cobra.Command{
		Use:   "install [runtime]",
		Short: "Install the Claude Code roca-store signing hook",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if args[0] != "claude" {
				return fmt.Errorf("unsupported hook runtime %q (want claude)", args[0])
			}
			path, err := claudeSettingsPath()
			if err != nil {
				return err
			}
			outcome, err := installClaudeAuthorshipHook(path)
			if err != nil {
				return err
			}
			if env.json {
				return env.printJSON(map[string]any{"runtimes": []agentcfg.Outcome{outcome}})
			}
			return env.renderOutcome(outcome, "updated")
		},
	}
}

func hooksRunCommand(env *cliEnv) *cobra.Command {
	command := &cobra.Command{
		Use:    "run [runtime]",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if args[0] != "claude" {
				return fmt.Errorf("unsupported hook runtime %q (want claude)", args[0])
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
func installClaudeAuthorshipHook(path string) (agentcfg.Outcome, error) {
	return agentcfg.Edit("claude", path, func(previous string) (string, error) {
		settings := map[string]any{}
		if previous != "" {
			if err := json.Unmarshal([]byte(previous), &settings); err != nil {
				return "", fmt.Errorf("read Claude settings: %w", err)
			}
			if settings == nil {
				return "", fmt.Errorf("Claude settings must be an object")
			}
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
		for _, entry := range entries {
			if isClaudeAuthorshipHook(entry) {
				return previous, nil
			}
		}
		entries = append(entries, map[string]any{
			"matcher": "Bash",
			"hooks":   []any{map[string]any{"type": "command", "command": claudeHookCommand}},
		})
		hooks["PreToolUse"] = entries
		encoded, err := json.MarshalIndent(settings, "", "  ")
		if err != nil {
			return "", fmt.Errorf("encode Claude settings: %w", err)
		}
		return string(append(encoded, '\n')), nil
	}, true)
}

func isClaudeAuthorshipHook(entry any) bool {
	group, ok := entry.(map[string]any)
	if !ok {
		return false
	}
	hooks, _ := group["hooks"].([]any)
	for _, raw := range hooks {
		hook, ok := raw.(map[string]any)
		if ok && hook["type"] == "command" && hook["command"] == claudeHookCommand {
			return true
		}
	}
	return false
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
	end := len(command)
	for _, separator := range []string{"&&", "||", ";", "\n", "|"} {
		if offset := strings.Index(command[location[1]:], separator); offset >= 0 && location[1]+offset < end {
			end = location[1] + offset
		}
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

func hasUnquotedFlag(command, flag string) bool {
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
			if (i == 0 || command[i-1] == ' ' || command[i-1] == '\t') &&
				strings.HasPrefix(command[i:], flag) {
				after := i + len(flag)
				if after == len(command) || command[after] == '=' ||
					command[after] == ' ' || command[after] == '\t' {
					return true
				}
			}
		}
	}
	return false
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
