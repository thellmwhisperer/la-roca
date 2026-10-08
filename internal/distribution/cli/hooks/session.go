package hooks

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/thellmwhisperer/la-roca/internal/artifact"
	"github.com/thellmwhisperer/la-roca/internal/distribution/agentcfg"
	"github.com/thellmwhisperer/la-roca/internal/provider/service"
)

const (
	claudeSessionStartEvent = "SessionStart"
	claudePreToolUseEvent   = "PreToolUse"
)

// Env is what the hooks need from the command line that runs them: where to
// write, the session database, the artifact registry, and the Claude signing
// hook, which stays with the command that signs `roca store`.
type Env struct {
	Out                           io.Writer
	OpenSessionContext            func() (*service.Service, error)
	RegisteredHook                func(runtime, path string) (artifact.Entry, bool, error)
	RegisterHook                  func(path, runtime, system string) error
	UnregisterHook                func(runtime, path string) error
	InstallClaudeAuthorshipHook   func(path, executable string) (agentcfg.Outcome, error)
	UninstallClaudeAuthorshipHook func(path string) (agentcfg.Outcome, string, error)
	RefreshClaudeHook             func(path, executable, previousChecksum string, enabled, force bool) (RefreshOutcome, error)
	ClaudeHookSystem              func(path string) (string, bool, error)
}

// RefreshOutcome is what refreshing the Claude signing hook found and did.
type RefreshOutcome struct {
	Changed, Diverged, Current bool
	// Missing means the registered entry is no longer in the settings document,
	// which is a withdrawal by the operator rather than an edit to our fragment.
	Missing      bool
	Backup       string
	SystemSHA256 string
}

var (
	claudePillsHookInvocation = regexp.MustCompile(
		`^` + ShellCommandExecutablePattern + `[ \t]+hooks[ \t]+run[ \t]+claude-pills$`,
	)
	claudeHandoffHookInvocation = regexp.MustCompile(
		`^` + ShellCommandExecutablePattern + `[ \t]+hooks[ \t]+run[ \t]+claude-handoff$`,
	)
)

func claudeSessionHookInvocation(kind string) *regexp.Regexp {
	if kind == "handoff" {
		return claudeHandoffHookInvocation
	}
	return claudePillsHookInvocation
}

type ClaudeHookSpec struct {
	Event      string
	Invocation *regexp.Regexp
	Command    func(string) string
	Entry      func(string) map[string]any
}

func InstallClaudeHook(path, declared string, spec ClaudeHookSpec) (agentcfg.Outcome, error) {
	if !filepath.IsAbs(declared) {
		return agentcfg.Outcome{Runtime: "claude", Path: path},
			fmt.Errorf("resolve the running executable %q to an absolute path", declared)
	}
	command := spec.Command(declared)
	return agentcfg.EditLinked("claude", path, func(previous string) (string, error) {
		settings, hooks, entries, err := ClaudeEventHookSettings(previous, spec.Event)
		if err != nil {
			return "", err
		}
		if hooks == nil {
			hooks = map[string]any{}
			settings["hooks"] = hooks
		}
		found, repointed := adoptClaudeHook(entries, command, spec.Invocation)
		if found && !repointed {
			return previous, nil
		}
		if !found {
			entries = append(entries, spec.Entry(command))
		}
		hooks[spec.Event] = entries
		return EncodeClaudeSettings(settings)
	}, true)
}

func uninstallClaudeSessionHook(path, kind string) (agentcfg.Outcome, string, error) {
	return UninstallClaudeHook(path, ClaudeHookSpec{
		Event: claudeSessionStartEvent, Invocation: claudeSessionHookInvocation(kind),
	}, foreignClaudeSessionSettingsWarning(path, kind))
}

func UninstallClaudeHook(path string, spec ClaudeHookSpec, unreadableWarning string) (agentcfg.Outcome, string, error) {
	var warning string
	outcome, err := agentcfg.EditLinked("claude", path, func(previous string) (string, error) {
		settings, hooks, entries, err := ClaudeEventHookSettings(previous, spec.Event)
		if err != nil {
			warning = unreadableWarning
			return previous, nil
		}
		remaining, withdrawn := withoutJSONHook(entries, true, spec.Invocation)
		if !withdrawn {
			return previous, nil
		}
		if len(remaining) == 0 {
			delete(hooks, spec.Event)
		} else {
			hooks[spec.Event] = remaining
		}
		if len(hooks) == 0 {
			delete(settings, "hooks")
		}
		return EncodeClaudeSettings(settings)
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

func EncodeClaudeSettings(settings map[string]any) (string, error) {
	encoded, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode Claude settings: %w", err)
	}
	return string(append(encoded, '\n')), nil
}

func CommandHooksOf(entry any) []map[string]any {
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

func CommandOf(hook map[string]any) string {
	command, _ := hook["command"].(string)
	return command
}

func ClaudeEventHookSettings(previous, event string) (settings, hooks map[string]any, entries []any, err error) {
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
		for _, hook := range CommandHooksOf(entry) {
			if !matcher.MatchString(CommandOf(hook)) {
				continue
			}
			found = true
			if CommandOf(hook) != command {
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

// InstallRuntimeHooks is what `roca hooks install <runtime>` does, and it does
// the same thing on every harness: one session-start hook that injects the
// fixed SYSTEM fragment, the active pills when asked, and the latest handoff
// when asked. Claude Code keeps one extra hook nothing else can offer — the
// PreToolUse entry that signs `roca store` with the model in its transcript —
// so a Claude install writes that too.
func InstallRuntimeHooks(env Env, runtime, path, declared string,
	force, pills, handoff bool) (agentcfg.Outcome, string, error) {
	var outcome agentcfg.Outcome
	var warning string
	if runtime == agentcfg.RuntimeClaude {
		// Settings this product cannot parse refuse the whole install rather
		// than leave one hook written and the other not.
		if err := refuseUnreadableClaudeInstall(path); err != nil {
			return agentcfg.Outcome{Runtime: runtime, Path: path}, "", err
		}
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
		env, runtime, path, declared, SessionRequest{Pills: pills, Handoff: handoff}, force)
	return mergeHookOutcomes(session, outcome),
		combineWarnings(warning, sessionWarning), err
}

// installSessionHook routes one runtime to its native transport. The hook is
// the same hook everywhere; only the file it is written into differs.
func installSessionHook(env Env, runtime, path, declared string,
	req SessionRequest, force bool) (agentcfg.Outcome, string, error) {
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
		return outcome, sessionHookInstallNote(runtime, outcome), err
	}
}

// sessionHookInstallNote is the one thing an operator still has to do after an
// install that otherwise finished. Codex runs only hooks whose exact command it
// has been trusted with, and skips an untrusted one in silence, so an install
// that said nothing here would look complete and inject nothing.
func sessionHookInstallNote(runtime string, outcome agentcfg.Outcome) string {
	if runtime != agentcfg.RuntimeCodex || !outcome.Changed {
		return ""
	}
	return "note: Codex runs a hook only once you have trusted its exact command; " +
		"open Codex and accept the new hook, or the session context is skipped in silence"
}

// UninstallRuntimeHooks withdraws everything La Roca owns for one runtime and
// leaves every neighbouring hook, in every file, exactly as it was.
//
// Claude keeps two hooks in one file, and an event this product cannot read
// must not hold the other one hostage: each is withdrawn on its own, and the
// markers left behind are named together in a single warning, because the
// operator has a single file to fix.
func UninstallRuntimeHooks(env Env, runtime, path string) (agentcfg.Outcome, string, error) {
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
		signing, _, err := env.UninstallClaudeAuthorshipHook(path)
		outcome = mergeHookOutcomes(outcome, signing)
		if err == nil {
			err = env.UnregisterHook(agentcfg.RuntimeClaude, path)
		}
		if err != nil {
			return outcome, "", err
		}
	}
	return outcome, claudeWithdrawalWarning(path, !sessionReadable, !signingReadable), nil
}

// refuseUnreadableClaudeInstall reads both Claude events before any of them is
// written. A missing file is writable; a file this product cannot parse is not.
func refuseUnreadableClaudeInstall(path string) error {
	body, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	previous := string(body)
	for _, event := range []string{claudePreToolUseEvent, claudeSessionStartEvent} {
		if _, _, _, err := ClaudeEventHookSettings(previous, event); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
	}
	return nil
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
	_, _, _, err = ClaudeEventHookSettings(string(body), event)
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

func uninstallSessionHook(env Env, runtime, path string) (agentcfg.Outcome, string, error) {
	switch hookRuntimes[runtime].transport {
	case transportScript:
		return uninstallSessionScript(env, runtime, path)
	case transportZcodeWrapper:
		wrapper, err := ZcodeWrapperPath()
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
func installClaudeSigningHook(env Env, path, declared string, force bool) (agentcfg.Outcome, string, error) {
	entry, registered, err := env.RegisteredHook("claude", path)
	if err != nil {
		return agentcfg.Outcome{Runtime: "claude", Path: path}, "", err
	}
	var outcome agentcfg.Outcome
	var warning string
	signatureCurrent := true
	if registered {
		refreshed, err := env.RefreshClaudeHook(path, declared, entry.SystemSHA256, true, force)
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
		outcome, err = env.InstallClaudeAuthorshipHook(path, declared)
		if err != nil {
			return outcome, "", err
		}
	}
	if signatureCurrent {
		system, found, err := env.ClaudeHookSystem(path)
		if err != nil || !found {
			if err == nil {
				err = fmt.Errorf("the installed Claude hook was not found in %s", path)
			}
			return outcome, "", err
		}
		if err := env.RegisterHook(path, "claude", system); err != nil {
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
