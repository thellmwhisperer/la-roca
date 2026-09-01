package cli

import (
	"fmt"
	"path/filepath"
	"regexp"

	"github.com/thellmwhisperer/la-roca/internal/distribution/agentcfg"
)

const claudeSessionStartEvent = "SessionStart"

var (
	claudePillsHookInvocation = regexp.MustCompile(
		`^` + shellCommandExecutablePattern + `[ \t]+hooks[ \t]+run[ \t]+claude-pills$`,
	)
	claudeHandoffHookInvocation = regexp.MustCompile(
		`^` + shellCommandExecutablePattern + `[ \t]+hooks[ \t]+run[ \t]+claude-handoff$`,
	)
)

func claudePillsHookCommand(executable string) string {
	return shellQuote(executable) + " hooks run claude-pills"
}

func claudeHandoffHookCommand(executable string) string {
	return shellQuote(executable) + " hooks run claude-handoff"
}

func claudeSessionHookCommand(kind, executable string) string {
	if kind == "handoff" {
		return claudeHandoffHookCommand(executable)
	}
	return claudePillsHookCommand(executable)
}

func claudeSessionHookInvocation(kind string) *regexp.Regexp {
	if kind == "handoff" {
		return claudeHandoffHookInvocation
	}
	return claudePillsHookInvocation
}

func installClaudeSessionHook(path, executable, kind string) (agentcfg.Outcome, error) {
	declared := chosenExecutable(executable)
	if !filepath.IsAbs(declared) {
		return agentcfg.Outcome{Runtime: "claude", Path: path},
			fmt.Errorf("resolve the running executable %q to an absolute path", declared)
	}
	command := claudeSessionHookCommand(kind, declared)
	matcher := claudeSessionHookInvocation(kind)
	return agentcfg.Edit("claude", path, func(previous string) (string, error) {
		settings, hooks, entries, err := claudeEventHookSettings(previous, claudeSessionStartEvent)
		if err != nil {
			return "", err
		}
		if hooks == nil {
			hooks = map[string]any{}
			settings["hooks"] = hooks
		}
		found, repointed := adoptSessionHook(entries, command, matcher)
		if found && !repointed {
			return previous, nil
		}
		if !found {
			entries = append(entries, map[string]any{
				"hooks": []any{map[string]any{"type": "command", "command": command}},
			})
		}
		hooks[claudeSessionStartEvent] = entries
		return encodeClaudeSettings(settings)
	}, true)
}

func uninstallClaudeSessionHook(path, kind string) (agentcfg.Outcome, string, error) {
	var warning string
	matcher := claudeSessionHookInvocation(kind)
	outcome, err := agentcfg.Edit("claude", path, func(previous string) (string, error) {
		settings, hooks, entries, err := claudeEventHookSettings(previous, claudeSessionStartEvent)
		if err != nil {
			warning = foreignClaudeSessionSettingsWarning(path, kind)
			return previous, nil
		}
		remaining, withdrawn := withoutSessionHook(entries, matcher)
		if !withdrawn {
			return previous, nil
		}
		if len(remaining) == 0 {
			delete(hooks, claudeSessionStartEvent)
		} else {
			hooks[claudeSessionStartEvent] = remaining
		}
		if len(hooks) == 0 {
			delete(settings, "hooks")
		}
		return encodeClaudeSettings(settings)
	}, false)
	return outcome, warning, err
}

func foreignClaudeSessionSettingsWarning(path, kind string) string {
	marker := "claude-pills"
	if kind == "handoff" {
		marker = "claude-handoff"
	}
	return fmt.Sprintf("warning: %s is not readable as Claude SessionStart settings, "+
		"so nothing there was changed; remove the hooks.SessionStart entry whose "+
		"command ends in `hooks run %s` by hand", path, marker)
}

func claudeEventHookSettings(previous, event string) (settings, hooks map[string]any, entries []any, err error) {
	settings, err = claudeSettings(previous)
	if err != nil {
		return nil, nil, nil, err
	}
	hooks, ok := settings["hooks"].(map[string]any)
	if settings["hooks"] != nil && !ok {
		return nil, nil, nil, fmt.Errorf("Claude settings hooks must be an object")
	}
	if hooks == nil {
		return settings, nil, nil, nil
	}
	entries, ok = hooks[event].([]any)
	if hooks[event] != nil && !ok {
		return nil, nil, nil, fmt.Errorf("Claude settings hooks.%s must be an array", event)
	}
	return settings, hooks, entries, nil
}

func adoptSessionHook(entries []any, command string, matcher *regexp.Regexp) (found, repointed bool) {
	for _, entry := range entries {
		for _, hook := range commandHooksOf(entry) {
			if !matcher.MatchString(commandOf(hook)) {
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

func withoutSessionHook(entries []any, matcher *regexp.Regexp) ([]any, bool) {
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
			if isHook && hook["type"] == "command" && matcher.MatchString(commandOf(hook)) {
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

func mergeHookOutcomes(base agentcfg.Outcome, extra agentcfg.Outcome) agentcfg.Outcome {
	if extra.Changed {
		base.Changed = true
	}
	if extra.Backup != "" {
		base.Backup = extra.Backup
	}
	return base
}

func installClaudeAuthorshipAndSessionHooks(env *cliEnv, path, declared string, force, pills, handoff bool) (agentcfg.Outcome, string, error) {
	if pills || handoff {
		outcome := agentcfg.Outcome{Runtime: "claude", Path: path}
		if pills {
			extra, err := installClaudeSessionHook(path, declared, "pills")
			if err != nil {
				return outcome, "", err
			}
			outcome = mergeHookOutcomes(outcome, extra)
		}
		if handoff {
			extra, err := installClaudeSessionHook(path, declared, "handoff")
			if err != nil {
				return outcome, "", err
			}
			outcome = mergeHookOutcomes(outcome, extra)
		}
		return outcome, "", nil
	}

	entry, registered, err := env.registeredArtifact(artifactKindHook, "claude", path)
	if err != nil {
		return agentcfg.Outcome{Runtime: "claude", Path: path}, "", err
	}
	var outcome agentcfg.Outcome
	var warning string
	signatureCurrent := true
	if registered {
		refreshed, err := refreshClaudeHook(path, declared, entry.SystemSHA256, true, force)
		outcome = agentcfg.Outcome{Runtime: "claude", Path: path,
			Changed: refreshed.Changed, Backup: refreshed.Backup}
		if err != nil {
			return outcome, "", err
		}
		if refreshed.Diverged {
			signatureCurrent = false
			warning = fmt.Sprintf("warning: %s has edits in its SYSTEM fragment; run `roca hooks install claude --force` to replace it", path)
		} else if !refreshed.Current {
			return outcome, "", fmt.Errorf("the installed Claude hook was not found in %s", path)
		}
	} else {
		outcome, err = installClaudeAuthorshipHook(path, declared)
		if err != nil {
			return outcome, "", err
		}
	}
	if signatureCurrent {
		system, found, err := claudeHookSystem(path)
		if err != nil || !found {
			if err == nil {
				err = fmt.Errorf("the installed Claude hook was not found in %s", path)
			}
			return outcome, "", err
		}
		if err := env.registerHook(path, "claude", system); err != nil {
			return outcome, "", err
		}
	}
	return outcome, warning, nil
}

func uninstallClaudeAuthorshipAndSessionHooks(env *cliEnv, path string, pills, handoff bool) (agentcfg.Outcome, string, error) {
	if !pills && !handoff {
		outcome, warning, err := uninstallClaudeAuthorshipHook(path)
		if err == nil {
			err = env.unregisterArtifact(artifactKindHook, "claude", path)
		}
		return outcome, warning, err
	}
	outcome := agentcfg.Outcome{Runtime: "claude", Path: path}
	var warning string
	if pills {
		extra, extraWarning, err := uninstallClaudeSessionHook(path, "pills")
		if err != nil {
			return extra, extraWarning, err
		}
		outcome = mergeHookOutcomes(outcome, extra)
		warning = combineWarnings(warning, extraWarning)
	}
	if handoff {
		extra, extraWarning, err := uninstallClaudeSessionHook(path, "handoff")
		if err != nil {
			return extra, extraWarning, err
		}
		outcome = mergeHookOutcomes(outcome, extra)
		warning = combineWarnings(warning, extraWarning)
	}
	return outcome, warning, nil
}

func combineWarnings(existing, next string) string {
	if existing == "" {
		return next
	}
	if next == "" {
		return existing
	}
	return existing + "\n" + next
}
