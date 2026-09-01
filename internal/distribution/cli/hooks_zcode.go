package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/thellmwhisperer/la-roca/internal/distribution/agentcfg"
	"github.com/thellmwhisperer/la-roca/internal/securefile"
)

const (
	zcodeHookWrapperMarker = "# Managed by roca hooks install zcode."
	zcodeHookTimeoutMs     = 15000
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

func hookConfigPath(runtime string) (string, error) {
	if runtime == agentcfg.RuntimeClaude {
		return claudeSettingsPath()
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("I do not know where your HOME is")
	}
	return agentcfg.ConfigPath(runtime, home, os.Getenv)
}

func installZcodeHandoffHook(configPath, executable string) (agentcfg.Outcome, string, error) {
	wrapperPath, err := zcodeHookWrapperPath()
	if err != nil {
		return agentcfg.Outcome{Runtime: agentcfg.RuntimeZcode, Path: configPath}, "", err
	}
	if _, err := agentcfg.LoadOwnedHooks(configPath); err != nil {
		return agentcfg.Outcome{Runtime: agentcfg.RuntimeZcode, Path: configPath}, "", err
	}
	if err := writeZcodeWrapper(wrapperPath, zcodeWrapper(executable)); err != nil {
		return agentcfg.Outcome{Runtime: agentcfg.RuntimeZcode, Path: configPath}, "", err
	}
	var created []string
	outcome, err := agentcfg.Edit(agentcfg.RuntimeZcode, configPath, func(previous string) (string, error) {
		settings, err := jsonObject(previous)
		if err != nil {
			return "", err
		}
		created = zcodeMissingHookContainers(settings)
		hooks, events, entries, err := zcodeHookTree(settings)
		if err != nil {
			return "", err
		}
		hooks["enabled"] = true
		found := false
		for _, raw := range entries {
			for _, hook := range commandHooksOf(raw) {
				if commandOf(hook) != wrapperPath {
					continue
				}
				hook["type"] = "command"
				hook["timeoutMs"] = zcodeHookTimeoutMs
				found = true
			}
		}
		if !found {
			entries = append(entries, map[string]any{"hooks": []any{map[string]any{
				"type": "command", "command": wrapperPath, "timeoutMs": zcodeHookTimeoutMs,
			}}})
		}
		events["SessionStart"] = entries
		hooks["events"] = events
		settings["hooks"] = hooks
		return agentcfg.ReplaceMember(previous, "hooks", hooks)
	}, true)
	if err != nil {
		return outcome, "", err
	}
	if outcome.Changed {
		if err := agentcfg.SaveOwnedHooks(configPath, created); err != nil {
			return outcome, "", err
		}
	}
	return outcome, "", nil
}

func uninstallZcodeHandoffHook(configPath, wrapperPath string) (agentcfg.Outcome, string, error) {
	created, err := agentcfg.LoadOwnedHooks(configPath)
	if err != nil {
		return agentcfg.Outcome{Runtime: agentcfg.RuntimeZcode, Path: configPath}, "", err
	}
	var warning string
	outcome, err := agentcfg.Edit(agentcfg.RuntimeZcode, configPath, func(previous string) (string, error) {
		settings, err := jsonObject(previous)
		if err != nil {
			warning = fmt.Sprintf("warning: %s is not readable as zcode settings; remove the nested hooks.events.SessionStart command %s by hand",
				configPath, wrapperPath)
			return previous, nil
		}
		if settings["hooks"] == nil {
			return previous, nil
		}
		hooks, events, entries, err := zcodeHookTree(settings)
		if err != nil {
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
				if ok && hook["type"] == "command" && commandOf(hook) == wrapperPath {
					withdrawn = true
					continue
				}
				kept = append(kept, candidate)
			}
			if len(kept) == 0 && len(group) == 1 {
				continue
			}
			group["hooks"] = kept
			remaining = append(remaining, group)
		}
		if !withdrawn {
			return previous, nil
		}
		owns := func(name string) bool {
			for _, createdName := range created {
				if createdName == name {
					return true
				}
			}
			return false
		}
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
		if owns("hooks") && zcodeHooksOnlyEnabled(hooks) {
			return agentcfg.ReplaceMember(previous, "hooks", nil)
		}
		return agentcfg.ReplaceMember(previous, "hooks", hooks)
	}, false)
	if err != nil {
		return outcome, warning, err
	}
	if err := agentcfg.ClearOwnedHooks(configPath); err != nil {
		return outcome, warning, err
	}
	if err := removeZcodeWrapper(wrapperPath); err != nil {
		return outcome, warning, err
	}
	return outcome, warning, nil
}

func zcodeHooksOnlyEnabled(hooks map[string]any) bool {
	if len(hooks) == 0 {
		return true
	}
	if len(hooks) == 1 {
		_, ok := hooks["enabled"]
		return ok
	}
	return false
}

func zcodeMissingHookContainers(settings map[string]any) []string {
	if settings["hooks"] == nil {
		return []string{"hooks"}
	}
	hooks, ok := settings["hooks"].(map[string]any)
	if !ok {
		return nil
	}
	var created []string
	events, _ := hooks["events"].(map[string]any)
	if hooks["events"] == nil {
		created = append(created, "hooks.events")
	}
	if events == nil || events["SessionStart"] == nil {
		created = append(created, "hooks.events.SessionStart")
	}
	return created
}

func zcodeHookTree(settings map[string]any) (hooks, events map[string]any, entries []any, err error) {
	hooks, ok := settings["hooks"].(map[string]any)
	if settings["hooks"] != nil && !ok {
		return nil, nil, nil, fmt.Errorf("zcode settings hooks must be an object")
	}
	if hooks == nil {
		hooks = map[string]any{}
	}
	events, ok = hooks["events"].(map[string]any)
	if hooks["events"] != nil && !ok {
		return nil, nil, nil, fmt.Errorf("zcode settings hooks.events must be an object")
	}
	if events == nil {
		events = map[string]any{}
	}
	entries, ok = events["SessionStart"].([]any)
	if events["SessionStart"] != nil && !ok {
		return nil, nil, nil, fmt.Errorf("zcode settings hooks.events.SessionStart must be an array")
	}
	return hooks, events, entries, nil
}

func jsonObject(previous string) (map[string]any, error) {
	settings := map[string]any{}
	if strings.TrimSpace(previous) == "" {
		return settings, nil
	}
	decoder := json.NewDecoder(strings.NewReader(previous))
	decoder.UseNumber()
	if err := decoder.Decode(&settings); err != nil {
		return nil, fmt.Errorf("read zcode settings: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("read zcode settings: multiple JSON values")
		}
		return nil, fmt.Errorf("read zcode settings: %w", err)
	}
	if settings == nil {
		return nil, fmt.Errorf("zcode settings must be an object")
	}
	return settings, nil
}

func zcodeWrapper(executable string) string {
	return `#!/bin/bash
` + zcodeHookWrapperMarker + `
set -euo pipefail
if OUTPUT=$(` + shellQuote(executable) + ` hooks run zcode 2>/dev/null) && [ -n "$OUTPUT" ]; then
  printf '%s\n' "$OUTPUT"
else
  printf '{}\n'
fi
`
}

func writeZcodeWrapper(path, content string) error {
	previous, err := os.ReadFile(path)
	if err == nil && string(previous) == content {
		return os.Chmod(path, 0o700)
	}
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read %s: %w", path, err)
	}
	if err == nil {
		if _, backupErr := securefile.BackUp(path, previous); backupErr != nil {
			return backupErr
		}
		if err := securefile.Replace(path, []byte(content), previous); err != nil {
			return err
		}
	} else if err := securefile.Write(path, []byte(content), 0o700, 0o700); err != nil {
		return err
	}
	return os.Chmod(path, 0o700)
}

func removeZcodeWrapper(path string) error {
	body, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	if !strings.Contains(string(body), zcodeHookWrapperMarker) {
		return fmt.Errorf("refuse to remove unrecognized zcode hook wrapper %s", path)
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
