package hooks

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/thellmwhisperer/la-roca/internal/agentcfg"
)

// The Claude Code lifecycle adapter: its settings file and its protocol.
//
// One runtime only, which is the decision on A-1, option (b): the
// laboratory supports `claude` and nothing else, and shipping four more
// adapters nobody has ever run would be inventing scope. Every other runtime
// lands as another file next to this one.
//
// The settings file belongs to the user. Roca owns exactly the entries whose
// command runs `roca hook`, so a hook somebody wrote themselves survives both an
// install and an uninstall, and every byte outside the `hooks` member is left
// where it was found.

const (
	RuntimeClaude = "claude"

	// EnvSettingsDir is what moves Claude Code's settings directory.
	EnvSettingsDir  = "CLAUDE_CONFIG_DIR"
	settingsDirName = ".claude"
	settingsFile    = "settings.json"
	hooksKey        = "hooks"
)

// The lifecycle events v1 declares.
//
// `Stop` is deliberately not among them, and the reason is worth writing down:
// it fires on every turn, and in the laboratory it exists to keep the live
// session hot through the incremental ingest. In v1 the incremental engine is
// `roca ingest`, so a `Stop` hook would be a subprocess on every single turn of
// every session with nothing to do. It comes back the day it has a referent.
const (
	EventSessionStart = "SessionStart"
	EventPreCompact   = "PreCompact"
	EventSessionEnd   = "SessionEnd"
)

// SupportedEvents are the events declared, in the order they are written.
var SupportedEvents = []string{EventSessionStart, EventPreCompact, EventSessionEnd}

// The states a settings file can be in, from Roca's point of view.
const (
	StateConfigured    = "configured"
	StateNotConfigured = "not-configured"
	StateMissing       = "missing"
	StateInvalid       = "invalid"
)

// ownedCommand is what Roca answers for: a command that runs the hook
// transport, whatever absolute path, wrapper or extension the binary was
// installed under. Recognizing it by shape and not by exact string is what lets
// an uninstall clean up after an install that used `--executable`.
//
// The optional quote is the closing one `shellQuote` writes around a path with
// a space in it. Without it the withdrawal stops recognizing exactly the entries
// this version installs, and an entry nobody recognizes stays in the operator's
// settings for ever, pointing at a binary the purge deleted.
var ownedCommand = regexp.MustCompile(`(?:^|[\s/\\'"])roca(?:\.\w+)?['"]?\s+hook\s+\S+`)

// Report is what one runtime has declared.
type Report struct {
	Runtime string   `json:"runtime"`
	Path    string   `json:"path"`
	State   string   `json:"state"`
	Events  []string `json:"events,omitempty"`
	Error   string   `json:"error,omitempty"`
}

// Runtimes are the runtimes with a lifecycle adapter. One, for now.
func Runtimes() []string { return []string{RuntimeClaude} }

// SettingsPath is where Claude Code keeps its settings, which is not where it
// keeps its MCP configuration: two files, two purposes.
func SettingsPath(home string, env func(string) string) string {
	directory := filepath.Join(home, settingsDirName)
	if env != nil {
		if declared := env(EnvSettingsDir); declared != "" {
			directory = declared
		}
	}
	return filepath.Join(directory, settingsFile)
}

// Install declares every lifecycle hook in the runtime's settings file.
func Install(runtime, path, executable string) (agentcfg.Outcome, error) {
	if err := supported(runtime); err != nil {
		return agentcfg.Outcome{}, err
	}
	if strings.TrimSpace(executable) == "" {
		executable = "roca"
	}
	return agentcfg.Edit(runtime, path, func(text string) (string, error) {
		return declared(text, executable)
	}, true)
}

// Uninstall withdraws them, leaving the rest of the file alone.
func Uninstall(runtime, path string) (agentcfg.Outcome, error) {
	if err := supported(runtime); err != nil {
		return agentcfg.Outcome{}, err
	}
	return agentcfg.Edit(runtime, path, withdrawn, false)
}

// Status describes what is declared without modifying the settings file.
func Status(runtime, path string) (Report, error) {
	if err := supported(runtime); err != nil {
		return Report{}, err
	}
	report := Report{Runtime: runtime, Path: path}
	content, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		report.State = StateMissing
		return report, nil
	}
	if err != nil {
		report.State, report.Error = StateInvalid, err.Error()
		return report, nil
	}
	text := string(content)

	declared, err := hooksMember(text)
	if err != nil {
		report.State, report.Error = StateInvalid, err.Error()
		return report, nil
	}
	for _, event := range SupportedEvents {
		if ownsAny(declared[event]) {
			report.Events = append(report.Events, event)
		}
	}
	report.State = StateNotConfigured
	if len(report.Events) > 0 {
		report.State = StateConfigured
	}
	return report, nil
}

// Command is the CLI line one event runs. It is the whole transport: a hook
// runs this and reads its standard output. It never opens the database itself
// and it never speaks MCP.
func Command(event, executable string) string {
	args := map[string]string{
		EventSessionStart: "hook context --runtime " + RuntimeClaude,
		EventPreCompact:   "hook record --trigger precompact",
		EventSessionEnd:   "hook record --trigger session_end",
	}[event]
	if args == "" {
		return ""
	}
	return shellQuote(executable) + " " + args
}

// safeInACommandLine is the set of characters a shell passes through untouched.
// A path made only of these is written as it is, so the settings file of every
// ordinary installation keeps the bytes it already had.
var safeInACommandLine = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

// shellQuote wraps an executable the shell would otherwise take apart.
//
// A hook entry is a command LINE and not an argv array: the runtime hands it to
// a shell, and a shell splits on whitespace. An installation under
// `/Users/Ana Maria/.local/bin` therefore declared a hook that ran `/Users/Ana`,
// and nothing failed loudly about it — the entries were written, `roca hook
// status` said configured, and no context ever reached a single session.
//
// Single quotes, because inside them a shell interprets nothing at all, which
// is the only thing that also holds for the `$`, the `&` and the `'` a home
// directory is allowed to carry.
func shellQuote(executable string) string {
	if safeInACommandLine.MatchString(executable) {
		return executable
	}
	return "'" + strings.ReplaceAll(executable, "'", `'\''`) + "'"
}

// RenderSessionStart is Claude Code's own protocol for injecting context: the
// block goes on standard output inside the envelope the runtime reads.
func RenderSessionStart(context string) (string, error) {
	if context == "" {
		return "", nil
	}
	encoded, err := json.Marshal(map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName":     EventSessionStart,
			"additionalContext": context,
		},
	})
	return string(encoded), err
}

// declared returns the settings text with every Roca hook declared exactly
// once, and every hook that is not Roca's left where it is.
func declared(text, executable string) (string, error) {
	member, err := hooksMember(text)
	if err != nil {
		return "", err
	}
	merged := map[string]any{}
	for event, groups := range member {
		if kept := withoutRoca(groups); len(kept) > 0 {
			merged[event] = kept
		}
	}
	for _, event := range SupportedEvents {
		existing, _ := merged[event].([]any)
		merged[event] = append(existing, map[string]any{
			hooksKey: []any{
				map[string]any{"type": "command", "command": Command(event, executable)},
			},
		})
	}
	return agentcfg.ReplaceMember(text, hooksKey, merged)
}

// withdrawn returns the settings text with every Roca hook removed and nothing
// else touched.
func withdrawn(text string) (string, error) {
	member, err := hooksMember(text)
	if err != nil {
		return "", err
	}
	remaining := map[string]any{}
	for event, groups := range member {
		if kept := withoutRoca(groups); len(kept) > 0 {
			remaining[event] = kept
		}
	}
	if len(remaining) == 0 {
		// A `hooks` member with nothing in it is noise in somebody's settings.
		return agentcfg.ReplaceMember(text, hooksKey, nil)
	}
	return agentcfg.ReplaceMember(text, hooksKey, remaining)
}

// hooksMember reads the top-level `hooks` mapping, or an empty one. A settings
// file Roca cannot parse is a settings file Roca must not edit, so this is
// where an install stops.
func hooksMember(text string) (map[string]any, error) {
	if strings.TrimSpace(text) == "" {
		return map[string]any{}, nil
	}
	var settings map[string]any
	if err := json.Unmarshal([]byte(text), &settings); err != nil {
		return nil, err
	}
	value, ok := settings[hooksKey]
	if !ok || value == nil {
		return map[string]any{}, nil
	}
	member, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s must be a JSON object", hooksKey)
	}
	return member, nil
}

// withoutRoca drops Roca's own entries and keeps every group the user still
// needs, including a group where only some of the commands were Roca's.
func withoutRoca(groups any) []any {
	list, ok := groups.([]any)
	if !ok {
		if groups == nil {
			return nil
		}
		return []any{groups}
	}
	var kept []any
	for _, item := range list {
		matcher, ok := item.(map[string]any)
		entries, hasEntries := matcher[hooksKey].([]any)
		if !ok || !hasEntries {
			kept = append(kept, item)
			continue
		}
		var remaining []any
		for _, entry := range entries {
			if !owns(entry) {
				remaining = append(remaining, entry)
			}
		}
		switch {
		case len(remaining) == len(entries):
			kept = append(kept, item)
		case len(remaining) > 0:
			replaced := maps.Clone(matcher)
			replaced[hooksKey] = remaining
			kept = append(kept, replaced)
		}
	}
	return kept
}

func owns(entry any) bool {
	if declared, ok := entry.(map[string]any); ok {
		if cmd, ok := declared["command"].(string); ok {
			return ownedCommand.MatchString(cmd)
		}
	}
	return false
}

func ownsAny(groups any) bool {
	list, ok := groups.([]any)
	if !ok {
		return false
	}
	for _, item := range list {
		if matcher, ok := item.(map[string]any); ok {
			if entries, ok := matcher[hooksKey].([]any); ok {
				for _, entry := range entries {
					if owns(entry) {
						return true
					}
				}
			}
		}
	}
	return false
}

func supported(runtime string) error {
	if runtime == RuntimeClaude {
		return nil
	}
	return fmt.Errorf("no lifecycle-hook adapter for %q; only %q is supported", runtime, RuntimeClaude)
}
