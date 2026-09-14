package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/thellmwhisperer/la-roca/internal/distribution/agentcfg"
)

const (
	claudeSessionStartEvent = "SessionStart"
	claudePreToolUseEvent   = "PreToolUse"
)

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

type claudeHookSpec struct {
	event      string
	invocation *regexp.Regexp
	command    func(string) string
	entry      func(string) map[string]any
}

func installClaudeSessionHook(path, executable, kind string) (agentcfg.Outcome, error) {
	return installClaudeHook(path, executable, claudeHookSpec{
		event: claudeSessionStartEvent, invocation: claudeSessionHookInvocation(kind),
		command: func(declared string) string { return claudeSessionHookCommand(kind, declared) },
		entry:   claudeSessionHookEntry,
	})
}

func claudeSessionHookEntry(command string) map[string]any {
	return map[string]any{
		"hooks": []any{map[string]any{"type": "command", "command": command}},
	}
}

func installClaudeHook(path, executable string, spec claudeHookSpec) (agentcfg.Outcome, error) {
	declared := chosenExecutable(executable)
	if !filepath.IsAbs(declared) {
		return agentcfg.Outcome{Runtime: "claude", Path: path},
			fmt.Errorf("resolve the running executable %q to an absolute path", declared)
	}
	command := spec.command(declared)
	return agentcfg.Edit("claude", path, func(previous string) (string, error) {
		settings, hooks, entries, err := claudeEventHookSettings(previous, spec.event)
		if err != nil {
			return "", err
		}
		if hooks == nil {
			hooks = map[string]any{}
			settings["hooks"] = hooks
		}
		found, repointed := adoptClaudeHook(entries, command, spec.invocation)
		if found && !repointed {
			return previous, nil
		}
		if !found {
			entries = append(entries, spec.entry(command))
		}
		hooks[spec.event] = entries
		return encodeClaudeSettings(settings)
	}, true)
}

func uninstallClaudeSessionHook(path, kind string) (agentcfg.Outcome, string, error) {
	return uninstallClaudeHook(path, claudeHookSpec{
		event: claudeSessionStartEvent, invocation: claudeSessionHookInvocation(kind),
	}, foreignClaudeSessionSettingsWarning(path, kind))
}

func uninstallClaudeHook(path string, spec claudeHookSpec, unreadableWarning string) (agentcfg.Outcome, string, error) {
	var warning string
	outcome, err := agentcfg.Edit("claude", path, func(previous string) (string, error) {
		settings, hooks, entries, err := claudeEventHookSettings(previous, spec.event)
		if err != nil {
			warning = unreadableWarning
			return previous, nil
		}
		remaining, withdrawn := withoutJSONHook(entries, true, spec.invocation)
		if !withdrawn {
			return previous, nil
		}
		if len(remaining) == 0 {
			delete(hooks, spec.event)
		} else {
			hooks[spec.event] = remaining
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

// adoptClaudeHook repoints an entry this product already installed at the
// currently resolved binary, so reinstalling after a move heals its command.
func adoptClaudeHook(entries []any, command string, matcher *regexp.Regexp) (found, repointed bool) {
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

func mergeHookOutcomes(base agentcfg.Outcome, extra agentcfg.Outcome) agentcfg.Outcome {
	if extra.Changed {
		base.Changed = true
	}
	if extra.Backup != "" {
		base.Backup = extra.Backup
	}
	return base
}

// installRuntimeHooks is what `roca hooks install <runtime>` does, and it does
// the same thing on every harness: one session-start hook that injects the
// fixed SYSTEM fragment, the active pills when asked, and the latest handoff
// when asked. Claude Code keeps one extra hook nothing else can offer — the
// PreToolUse entry that signs `roca store` with the model in its transcript —
// so a Claude install writes that too.
func installRuntimeHooks(env *cliEnv, runtime, path, declared string,
	force, pills, handoff bool) (agentcfg.Outcome, string, error) {
	var outcome agentcfg.Outcome
	var warning string
	if runtime == agentcfg.RuntimeClaude {
		// The signing hook goes first because it is the strict reader of this
		// file: settings it cannot parse must refuse the whole install before
		// the session entry is written, never halfway through it.
		signing, signingWarning, err := installClaudeSigningHook(env, path, declared, force)
		if err != nil {
			return signing, signingWarning, err
		}
		outcome, warning = signing, signingWarning
		// A pre-1.85 install left two separate SessionStart entries behind.
		// Leaving them there would inject the same pills twice from one file.
		for _, kind := range []string{"pills", "handoff"} {
			legacy, _, err := uninstallClaudeSessionHook(path, kind)
			if err != nil {
				return outcome, warning, err
			}
			outcome = mergeHookOutcomes(outcome, legacy)
		}
	}
	session, sessionWarning, err := installSessionHook(
		env, runtime, path, declared, sessionRequest{pills: pills, handoff: handoff}, force)
	return mergeHookOutcomes(session, outcome),
		combineWarnings(warning, sessionWarning), err
}

// installSessionHook routes one runtime to its native transport. The hook is
// the same hook everywhere; only the file it is written into differs.
func installSessionHook(env *cliEnv, runtime, path, declared string,
	req sessionRequest, force bool) (agentcfg.Outcome, string, error) {
	if !filepath.IsAbs(declared) {
		return agentcfg.Outcome{Runtime: runtime, Path: path}, "",
			fmt.Errorf("resolve the running executable %q to an absolute path", declared)
	}
	switch hookRuntimes[runtime].transport {
	case transportScript:
		return installSessionScript(env, runtime, path, declared, req, force)
	case transportZcodeWrapper:
		return installZcodeSessionHook(path, declared, req)
	default:
		outcome, err := installJSONSessionHook(runtime, path, declared, req)
		return outcome, "", err
	}
}

// uninstallRuntimeHooks withdraws everything La Roca owns for one runtime and
// leaves every neighbouring hook, in every file, exactly as it was.
//
// Claude keeps two hooks in one file, and an event this product cannot read
// must not hold the other one hostage: each is withdrawn on its own, and the
// markers left behind are named together in a single warning, because the
// operator has a single file to fix.
func uninstallRuntimeHooks(env *cliEnv, runtime, path string) (agentcfg.Outcome, string, error) {
	if runtime != agentcfg.RuntimeClaude {
		return uninstallSessionHook(env, runtime, path)
	}
	sessionReadable := claudeEventReadable(path, claudeSessionStartEvent)
	signingReadable := claudeEventReadable(path, claudePreToolUseEvent)
	outcome := agentcfg.Outcome{Runtime: runtime, Path: path}
	if sessionReadable {
		session, _, err := uninstallJSONSessionHook(runtime, path)
		if err != nil {
			return outcome, "", err
		}
		outcome = mergeHookOutcomes(outcome, session)
		for _, kind := range []string{"pills", "handoff"} {
			legacy, _, err := uninstallClaudeSessionHook(path, kind)
			if err != nil {
				return outcome, "", err
			}
			outcome = mergeHookOutcomes(outcome, legacy)
		}
	}
	if signingReadable {
		signing, _, err := uninstallClaudeAuthorshipHook(path)
		outcome = mergeHookOutcomes(outcome, signing)
		if err == nil {
			err = env.unregisterArtifact(artifactKindHook, agentcfg.RuntimeClaude, path)
		}
		if err != nil {
			return outcome, "", err
		}
	}
	return outcome, claudeWithdrawalWarning(path, !sessionReadable, !signingReadable), nil
}

// claudeEventReadable answers whether one event of Claude's settings is the
// shape this product can edit. Settings that are not there are readable: there
// is nothing in them to misread, and nothing to withdraw.
func claudeEventReadable(path, event string) bool {
	body, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return true
	}
	if err != nil {
		return false
	}
	_, _, _, err = claudeEventHookSettings(string(body), event)
	return err == nil
}

// claudeWithdrawalWarning is the one line an operator gets for the entries a
// withdrawal could not take out, naming each ownership marker to delete so no
// hook survives calling a binary that is gone.
func claudeWithdrawalWarning(path string, session, signing bool) string {
	var left []string
	if session {
		left = append(left, "the hooks.SessionStart entries whose command contains "+
			"`hooks run session --runtime claude`, `hooks run claude-pills` or "+
			"`hooks run claude-handoff`")
	}
	if signing {
		left = append(left, "the hooks.PreToolUse entry whose command ends in "+
			"`hooks run claude`")
	}
	if len(left) == 0 {
		return ""
	}
	return fmt.Sprintf("warning: %s is not readable as Claude settings, so nothing "+
		"there was changed; remove %s by hand", path, strings.Join(left, " and "))
}

func uninstallSessionHook(env *cliEnv, runtime, path string) (agentcfg.Outcome, string, error) {
	switch hookRuntimes[runtime].transport {
	case transportScript:
		return uninstallSessionScript(env, runtime, path)
	case transportZcodeWrapper:
		wrapper, err := zcodeHookWrapperPath()
		if err != nil {
			return agentcfg.Outcome{Runtime: runtime, Path: path}, "", err
		}
		return uninstallZcodeHandoffHook(path, wrapper)
	default:
		return uninstallJSONSessionHook(runtime, path)
	}
}

// installClaudeSigningHook installs the Claude-only PreToolUse entry and keeps
// the registry honest about the fragment it owns.
func installClaudeSigningHook(env *cliEnv, path, declared string, force bool) (agentcfg.Outcome, string, error) {
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

func combineWarnings(existing, next string) string {
	if existing == "" {
		return next
	}
	if next == "" {
		return existing
	}
	return existing + "\n" + next
}
