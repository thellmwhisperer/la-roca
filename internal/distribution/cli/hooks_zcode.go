package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/thellmwhisperer/la-roca/internal/distribution/agentcfg"
	"github.com/thellmwhisperer/la-roca/internal/securefile"
)

const (
	zcodeHookWrapperMarker = "# Managed by roca hooks install zcode."
	zcodeHookTimeoutMs     = 15000
	zcodeWrapperClaim      = "files.zcode-hook-wrapper"
)

func zcodeRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("I do not know where your HOME is")
	}
	if declared := os.Getenv("ZCODE_HOME"); declared != "" {
		return agentcfg.Expand(declared, home), nil
	}
	return filepath.Join(home, ".zcode"), nil
}

func zcodeHookWrapperPath() (string, error) {
	root, err := zcodeRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "hooks", "roca-handoff.sh"), nil
}

func hookConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("I do not know where your HOME is")
	}
	return agentcfg.ConfigPath(agentcfg.RuntimeZcode, home, os.Getenv)
}

// installZcodeSessionHook writes ZCode's wrapper and its nested SessionStart
// entry. ZCode discards plain-text hook stdout, so the wrapper is what
// guarantees valid JSON even when the binary behind it fails.
func installZcodeSessionHook(configPath, executable string, req sessionRequest) (agentcfg.Outcome, string, error) {
	wrapperPath, err := zcodeHookWrapperPath()
	if err != nil {
		return agentcfg.Outcome{Runtime: agentcfg.RuntimeZcode, Path: configPath}, "", err
	}
	wrapperBefore, err := readZcodeWrapperState(wrapperPath)
	if err != nil {
		return agentcfg.Outcome{Runtime: agentcfg.RuntimeZcode, Path: configPath}, "", err
	}
	wrapperContent := zcodeWrapper(executable, req)
	_, claims, err := agentcfg.LoadOwnedHooks(configPath)
	if err != nil {
		return agentcfg.Outcome{Runtime: agentcfg.RuntimeZcode, Path: configPath}, "", err
	}
	if wrapperBefore.exists && !zcodeOwnsWrapper(claims, wrapperBefore.body, wrapperContent) {
		return agentcfg.Outcome{Runtime: agentcfg.RuntimeZcode, Path: configPath}, "",
			fmt.Errorf("refuse to overwrite operator-owned zcode hook wrapper %s", wrapperPath)
	}
	release, err := agentcfg.LockOwned(configPath, true)
	if err != nil {
		return agentcfg.Outcome{Runtime: agentcfg.RuntimeZcode, Path: configPath}, "", err
	}
	defer release()
	containers, claims, err := agentcfg.LoadOwnedHooks(configPath)
	if err != nil {
		return agentcfg.Outcome{Runtime: agentcfg.RuntimeZcode, Path: configPath}, "", err
	}
	wrapperBefore, err = readZcodeWrapperState(wrapperPath)
	if err != nil {
		return agentcfg.Outcome{Runtime: agentcfg.RuntimeZcode, Path: configPath}, "", err
	}
	if wrapperBefore.exists && !zcodeOwnsWrapper(claims, wrapperBefore.body, wrapperContent) {
		return agentcfg.Outcome{Runtime: agentcfg.RuntimeZcode, Path: configPath}, "",
			fmt.Errorf("refuse to overwrite operator-owned zcode hook wrapper %s", wrapperPath)
	}
	if _, err := agentcfg.Edit(agentcfg.RuntimeZcode, configPath, func(previous string) (string, error) {
		settings, err := jsonObject(previous)
		if err != nil {
			return "", err
		}
		if _, _, _, err := zcodeHookTree(settings); err != nil {
			return "", err
		}
		return previous, nil
	}, true); err != nil {
		return agentcfg.Outcome{Runtime: agentcfg.RuntimeZcode, Path: configPath}, "", err
	}
	wrapperBackup, err := writeZcodeWrapper(wrapperPath, wrapperContent, wrapperBefore)
	if err != nil {
		return agentcfg.Outcome{Runtime: agentcfg.RuntimeZcode, Path: configPath}, "", err
	}
	var created []string
	written := zcodeHookEntry(wrapperPath)
	claimed := map[string]any{}
	outcome, err := agentcfg.Edit(agentcfg.RuntimeZcode, configPath, func(previous string) (string, error) {
		settings, err := jsonObject(previous)
		if err != nil {
			return "", err
		}
		hooks, events, entries, err := zcodeHookTree(settings)
		if err != nil {
			return "", err
		}
		created = zcodeMissingHookContainers(settings)
		claimed[zcodeEnabledClaim] = nil
		if enabled, present := hooks["enabled"]; !present {
			hooks["enabled"] = true
			claimed[zcodeEnabledClaim] = true
		} else if zcodeOwnsEnabled(containers, claims, enabled) {
			claimed[zcodeEnabledClaim] = enabled
		}
		// The entry La Roca wrote is refreshed; one that no longer matches its
		// claim is the operator's and is neither reset nor duplicated.
		claimed[zcodeHookClaim] = nil
		found := false
		for _, raw := range entries {
			for _, hook := range commandHooksOf(raw) {
				if commandOf(hook) != wrapperPath {
					continue
				}
				if !found && zcodeOwnsHook(claims, hook, written) {
					hook["timeoutMs"] = zcodeHookTimeoutMs
					claimed[zcodeHookClaim] = written
				}
				found = true
			}
		}
		if !found {
			entries = append(entries, map[string]any{"hooks": []any{written}})
			claimed[zcodeHookClaim] = written
		}
		events["SessionStart"] = entries
		hooks["events"] = events
		settings["hooks"] = hooks
		return agentcfg.ReplaceMember(previous, "hooks", hooks)
	}, true)
	if err != nil {
		return outcome, "", errors.Join(err,
			rollbackZcodeWrapper(wrapperPath, wrapperContent, wrapperBefore, wrapperBackup))
	}
	if err := agentcfg.SaveOwnedHooks(configPath, created, claimed); err != nil {
		return outcome, "", err
	}
	if err := agentcfg.SaveOwnedHookFile(configPath, zcodeWrapperClaim, []byte(wrapperContent)); err != nil {
		return outcome, "", err
	}
	if !wrapperBefore.exists || string(wrapperBefore.body) != wrapperContent {
		outcome.Changed = true
	}
	if wrapperBackup != "" && outcome.Backup == "" {
		outcome.Backup = wrapperBackup
	}
	return outcome, "", nil
}

func uninstallZcodeHandoffHook(configPath, wrapperPath string) (agentcfg.Outcome, string, error) {
	release, err := agentcfg.LockOwned(configPath, false)
	if err != nil {
		return agentcfg.Outcome{Runtime: agentcfg.RuntimeZcode, Path: configPath}, "", err
	}
	defer release()
	created, claims, err := agentcfg.LoadOwnedHooks(configPath)
	if err != nil {
		return agentcfg.Outcome{Runtime: agentcfg.RuntimeZcode, Path: configPath}, "", err
	}
	written := zcodeHookEntry(wrapperPath)
	var warning string
	wrapperReferenced := false
	outcome, err := agentcfg.Edit(agentcfg.RuntimeZcode, configPath, func(previous string) (string, error) {
		settings, err := jsonObject(previous)
		if err != nil {
			wrapperReferenced = true
			warning = fmt.Sprintf("warning: %s is not readable as zcode settings; remove the nested hooks.events.SessionStart command %s by hand",
				configPath, wrapperPath)
			return previous, nil
		}
		wrapperReferenced = zcodeHookReferencesWrapper(settings, wrapperPath)
		if settings["hooks"] == nil {
			return previous, nil
		}
		hooks, events, entries, err := zcodeHookTree(settings)
		if err != nil {
			wrapperReferenced = true
			warning = fmt.Sprintf("warning: %s is not readable as zcode settings; remove the nested hooks.events.SessionStart command %s by hand",
				configPath, wrapperPath)
			return previous, nil
		}
		remaining := make([]any, 0, len(entries))
		withdrawn := false
		for _, raw := range entries {
			group, ok := raw.(map[string]any)
			groupHooks, isList := group["hooks"].([]any)
			if !ok || !isList {
				remaining = append(remaining, raw)
				continue
			}
			kept := make([]any, 0, len(groupHooks))
			for _, candidate := range groupHooks {
				hook, ok := candidate.(map[string]any)
				if !withdrawn && ok && hook["type"] == "command" && commandOf(hook) == wrapperPath &&
					zcodeOwnsHook(claims, hook, written) {
					withdrawn = true
					continue
				}
				kept = append(kept, candidate)
			}
			if len(kept) < len(groupHooks) && len(kept) == 0 && len(group) == 1 {
				continue
			}
			group["hooks"] = kept
			remaining = append(remaining, group)
		}
		if !withdrawn {
			return previous, nil
		}
		owns := func(name string) bool { return slices.Contains(created, name) }
		if len(remaining) == 0 && owns("hooks.events.SessionStart") {
			delete(events, "SessionStart")
		} else {
			events["SessionStart"] = remaining
		}
		if len(events) == 0 && owns("hooks.events") {
			delete(hooks, "events")
		} else {
			hooks["events"] = events
		}
		if enabled, present := hooks["enabled"]; present && zcodeOwnsEnabled(created, claims, enabled) {
			delete(hooks, "enabled")
		}
		if owns("hooks") && len(hooks) == 0 {
			wrapperReferenced = zcodeHookReferencesWrapper(settings, wrapperPath)
			return agentcfg.ReplaceMember(previous, "hooks", nil)
		}
		settings["hooks"] = hooks
		wrapperReferenced = zcodeHookReferencesWrapper(settings, wrapperPath)
		return agentcfg.ReplaceMember(previous, "hooks", hooks)
	}, false)
	if err != nil {
		return outcome, warning, err
	}
	if !wrapperReferenced {
		if err := removeZcodeWrapper(wrapperPath, claims[zcodeWrapperClaim]); err != nil {
			return outcome, warning, err
		}
		if err := agentcfg.ClearOwnedHookFile(configPath, zcodeWrapperClaim); err != nil {
			return outcome, warning, err
		}
	}
	if err := agentcfg.ClearOwnedHooks(configPath); err != nil {
		return outcome, warning, err
	}
	return outcome, warning, nil
}

const (
	zcodeHookClaim    = "hooks.events.SessionStart.roca"
	zcodeEnabledClaim = "hooks.enabled"
)

func zcodeHookEntry(wrapperPath string) map[string]any {
	return map[string]any{"type": "command", "command": wrapperPath, "timeoutMs": zcodeHookTimeoutMs}
}

// zcodeOwnsHook answers whether hook is still the entry La Roca wrote. With no
// claim recorded (a legacy sidecar, or none) it is La Roca's only while it is
// exactly what this version writes, so a verbatim operator copy counts as ours.
func zcodeOwnsHook(claims map[string]string, hook, written map[string]any) bool {
	digest, ok := claims[zcodeHookClaim]
	if !ok {
		digest = agentcfg.ValueDigest(written)
	}
	return digest == agentcfg.ValueDigest(hook)
}

func zcodeOwnsWrapper(claims map[string]string, current []byte, written string) bool {
	if digest, ok := claims[zcodeWrapperClaim]; ok {
		return digest == agentcfg.BytesDigest(current)
	}
	return strings.Contains(string(current), zcodeHookWrapperMarker) && string(current) == written
}

func zcodeHookReferencesWrapper(settings map[string]any, wrapperPath string) bool {
	hooks, ok := settings["hooks"].(map[string]any)
	if !ok {
		return false
	}
	events, ok := hooks["events"].(map[string]any)
	if !ok {
		return false
	}
	for _, rawGroups := range events {
		groups, ok := rawGroups.([]any)
		if !ok {
			continue
		}
		for _, rawGroup := range groups {
			group, ok := rawGroup.(map[string]any)
			if !ok {
				continue
			}
			entries, ok := group["hooks"].([]any)
			if !ok {
				continue
			}
			for _, rawEntry := range entries {
				entry, ok := rawEntry.(map[string]any)
				if ok && entry["type"] == "command" && commandOf(entry) == wrapperPath {
					return true
				}
			}
		}
	}
	return false
}

// zcodeOwnsEnabled answers whether hooks.enabled still holds the value La Roca
// set. A legacy sidecar has no claim on it, but the hooks object it created
// carried La Roca's true.
func zcodeOwnsEnabled(containers []string, claims map[string]string, enabled any) bool {
	if digest, ok := claims[zcodeEnabledClaim]; ok {
		return digest == agentcfg.ValueDigest(enabled)
	}
	return enabled == true && slices.Contains(containers, "hooks")
}

func zcodeMissingHookContainers(settings map[string]any) []string {
	rawHooks, hasHooks := settings["hooks"]
	if !hasHooks {
		return []string{"hooks", "hooks.events", "hooks.events.SessionStart"}
	}
	hooks, ok := rawHooks.(map[string]any)
	if !ok {
		return nil
	}
	var created []string
	rawEvents, hasEvents := hooks["events"]
	if !hasEvents {
		return append(created, "hooks.events", "hooks.events.SessionStart")
	}
	events, _ := rawEvents.(map[string]any)
	if _, hasSessionStart := events["SessionStart"]; !hasSessionStart {
		created = append(created, "hooks.events.SessionStart")
	}
	return created
}

func zcodeHookTree(settings map[string]any) (hooks, events map[string]any, entries []any, err error) {
	rawHooks, hasHooks := settings["hooks"]
	if !hasHooks {
		hooks = map[string]any{}
	} else {
		var ok bool
		hooks, ok = rawHooks.(map[string]any)
		if !ok {
			return nil, nil, nil, fmt.Errorf("zcode settings hooks must be an object")
		}
	}
	rawEvents, hasEvents := hooks["events"]
	if !hasEvents {
		events = map[string]any{}
	} else {
		var ok bool
		events, ok = rawEvents.(map[string]any)
		if !ok {
			return nil, nil, nil, fmt.Errorf("zcode settings hooks.events must be an object")
		}
	}
	rawEntries, hasEntries := events["SessionStart"]
	if hasEntries {
		var ok bool
		entries, ok = rawEntries.([]any)
		if !ok {
			return nil, nil, nil, fmt.Errorf("zcode settings hooks.events.SessionStart must be an array")
		}
		for i, raw := range entries {
			group, ok := raw.(map[string]any)
			if !ok {
				return nil, nil, nil, fmt.Errorf("zcode settings hooks.events.SessionStart[%d] must be an object", i)
			}
			groupHooks, ok := group["hooks"].([]any)
			if !ok {
				return nil, nil, nil, fmt.Errorf("zcode settings hooks.events.SessionStart[%d].hooks must be an array", i)
			}
			for j, hook := range groupHooks {
				entry, ok := hook.(map[string]any)
				if !ok {
					return nil, nil, nil, fmt.Errorf("zcode settings hooks.events.SessionStart[%d].hooks[%d] must be an object", i, j)
				}
				hookType, ok := entry["type"].(string)
				if !ok || hookType == "" {
					return nil, nil, nil, fmt.Errorf("zcode settings hooks.events.SessionStart[%d].hooks[%d].type must be a string", i, j)
				}
				if hookType != "command" {
					continue
				}
				command, ok := entry["command"].(string)
				if !ok || strings.TrimSpace(command) == "" {
					return nil, nil, nil, fmt.Errorf("zcode settings hooks.events.SessionStart[%d].hooks[%d].command must be a string", i, j)
				}
				timeout, ok := entry["timeoutMs"].(json.Number)
				if !ok {
					return nil, nil, nil, fmt.Errorf("zcode settings hooks.events.SessionStart[%d].hooks[%d].timeoutMs must be a number", i, j)
				}
				milliseconds, err := timeout.Int64()
				if err != nil || milliseconds <= 0 {
					return nil, nil, nil, fmt.Errorf("zcode settings hooks.events.SessionStart[%d].hooks[%d].timeoutMs must be a positive integer", i, j)
				}
			}
		}
	}
	return hooks, events, entries, nil
}

func jsonObject(previous string) (map[string]any, error) {
	return readJSONDocument("zcode settings", previous)
}

func zcodeWrapper(executable string, req sessionRequest) string {
	return `#!/bin/bash
` + zcodeHookWrapperMarker + `
set -euo pipefail
if OUTPUT=$(` + sessionHookCommand(executable, agentcfg.RuntimeZcode, req) + ` 2>/dev/null) && [ -n "$OUTPUT" ]; then
  printf '%s\n' "$OUTPUT"
else
  printf '{}\n'
fi
`
}

type zcodeWrapperState struct {
	body   []byte
	mode   os.FileMode
	exists bool
}

func readZcodeWrapperState(path string) (zcodeWrapperState, error) {
	body, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return zcodeWrapperState{}, nil
	}
	if err != nil {
		return zcodeWrapperState{}, fmt.Errorf("read %s: %w", path, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return zcodeWrapperState{}, fmt.Errorf("inspect %s: %w", path, err)
	}
	return zcodeWrapperState{body: body, mode: info.Mode().Perm(), exists: true}, nil
}

func writeZcodeWrapper(path, content string, previous zcodeWrapperState) (string, error) {
	var backup string
	if previous.exists && string(previous.body) != content {
		var err error
		backup, err = securefile.BackUp(path, previous.body)
		if err != nil {
			return "", err
		}
	}
	var expected []byte
	if previous.exists {
		expected = previous.body
	}
	if err := securefile.Replace(path, []byte(content), expected); err != nil {
		if backup != "" {
			return "", errors.Join(err, os.Remove(backup))
		}
		return "", err
	}
	return backup, os.Chmod(path, 0o700)
}

func rollbackZcodeWrapper(path, installed string, previous zcodeWrapperState, backup string) error {
	current, err := os.ReadFile(path)
	if os.IsNotExist(err) && !previous.exists {
		return nil
	}
	if err != nil {
		return fmt.Errorf("roll back %s: %w", path, err)
	}
	if string(current) != installed {
		return fmt.Errorf("refuse to roll back %s because it changed after installation", path)
	}
	if previous.exists {
		if string(current) != string(previous.body) {
			if err := securefile.Replace(path, previous.body, current); err != nil {
				return fmt.Errorf("roll back %s: %w", path, err)
			}
		}
		if err := os.Chmod(path, previous.mode); err != nil {
			return fmt.Errorf("restore permissions on %s: %w", path, err)
		}
	} else if err := os.Remove(path); err != nil {
		return fmt.Errorf("roll back %s: %w", path, err)
	}
	if backup != "" {
		if err := os.Remove(backup); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove rolled-back backup %s: %w", backup, err)
		}
	}
	return nil
}

func removeZcodeWrapper(path, digest string) error {
	body, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	if digest == "" || digest != agentcfg.BytesDigest(body) {
		return nil
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove %s: %w", path, err)
	}
	return nil
}

func zcodeHookJSON(context string) []byte {
	if strings.TrimSpace(context) == "" {
		return []byte("{}\n")
	}
	encoded, err := json.Marshal(map[string]string{"additionalContext": context})
	if err != nil {
		return []byte("{}\n")
	}
	return append(encoded, '\n')
}

// runZcodeHandoffHook answers the wrappers written before session hooks became
// the same feature on every harness. Those wrappers are on operators' disks and
// still call `hooks run zcode`; reinstalling replaces them.
func runZcodeHandoffHook(ctx context.Context, env *cliEnv) error {
	fmt.Fprint(env.out, string(zcodeHookJSON(zcodeHandoffContext(ctx, env))))
	return nil
}

func zcodeHandoffContext(ctx context.Context, env *cliEnv) string {
	svc, _, err := env.openSessionContextService()
	if err != nil {
		return ""
	}
	defer svc.Close()
	project, err := resolveProject("")
	if err != nil {
		return ""
	}
	list, err := svc.LatestHandoffs(ctx, project)
	if err != nil || len(list.Handoffs) == 0 {
		return ""
	}
	var body strings.Builder
	for i, handoff := range list.Handoffs {
		if i > 0 {
			body.WriteByte('\n')
		}
		body.WriteString(handoff.Content)
	}
	return body.String()
}
