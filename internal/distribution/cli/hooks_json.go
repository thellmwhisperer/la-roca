package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/thellmwhisperer/la-roca/internal/distribution/agentcfg"
)

// This file owns the harnesses that keep their session hooks in a JSON
// document of their own: Codex in ~/.codex/hooks.json and Cursor in
// ~/.cursor/hooks.json. Both files belong to the operator and routinely hold
// other tools' hooks, so every edit here adds or removes exactly one entry and
// rewrites exactly one top-level member, leaving the rest of the bytes alone.

// readJSONDocument reads one operator-owned JSON file. Numbers are kept as
// written so an operator's timeout comes back out of a rewrite as the integer
// they typed, and `what` names the file in every refusal the way its own
// harness would, because that is the name the operator has to go and fix.
func readJSONDocument(what, previous string) (map[string]any, error) {
	settings := map[string]any{}
	if strings.TrimSpace(previous) == "" {
		return settings, nil
	}
	decoder := json.NewDecoder(strings.NewReader(previous))
	decoder.UseNumber()
	if err := decoder.Decode(&settings); err != nil {
		return nil, fmt.Errorf("read %s: %w", what, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("read %s: multiple JSON values", what)
		}
		return nil, fmt.Errorf("read %s: %w", what, err)
	}
	if settings == nil {
		return nil, fmt.Errorf("%s must be an object", what)
	}
	return settings, nil
}

func decodeHookDocument(runtime, previous string) (map[string]any, error) {
	return readJSONDocument(runtime+" hooks", previous)
}

// jsonHookTree hands back the hooks table and the entries of one event, and
// refuses a document shaped in a way this product cannot edit without guessing.
func jsonHookTree(runtime, event string, settings map[string]any) (hooks map[string]any, entries []any, err error) {
	raw, present := settings["hooks"]
	if !present || raw == nil {
		return map[string]any{}, nil, nil
	}
	hooks, ok := raw.(map[string]any)
	if !ok {
		return nil, nil, fmt.Errorf("%s hooks must be an object", runtime)
	}
	rawEntries, present := hooks[event]
	if !present || rawEntries == nil {
		return hooks, nil, nil
	}
	entries, ok = rawEntries.([]any)
	if !ok {
		return nil, nil, fmt.Errorf("%s hooks.%s must be an array", runtime, event)
	}
	return hooks, entries, nil
}

// hookCommandsOf returns the command objects of one entry in the shape this
// runtime writes them: grouped under a "hooks" array, or the entry itself when
// the harness lists bare commands. Returning the maps, not their text, is what
// lets a reinstall repoint a moved binary in place.
func hookCommandsOf(entry any, nested bool) []map[string]any {
	if nested {
		return commandHooksOf(entry)
	}
	object, ok := entry.(map[string]any)
	if !ok {
		return nil
	}
	if _, isCommand := object["command"].(string); !isCommand {
		return nil
	}
	return []map[string]any{object}
}

func jsonHookEntry(spec hookRuntime, command string) map[string]any {
	hook := map[string]any{"command": command}
	if spec.nested {
		hook["type"] = "command"
	}
	if spec.timeoutKey != "" {
		hook[spec.timeoutKey] = spec.timeout
	}
	if !spec.nested {
		return hook
	}
	return map[string]any{"hooks": []any{hook}}
}

// installJSONSessionHook adds one session entry, or repoints the one already
// there. Foreign hooks in the same event keep their position and their bytes.
func installJSONSessionHook(runtime, path, executable string, req sessionRequest) (agentcfg.Outcome, error) {
	spec := hookRuntimes[runtime]
	command := sessionHookCommand(executable, runtime, req)
	matcher := sessionHookInvocation(runtime)
	return agentcfg.Edit(runtime, path, func(previous string) (string, error) {
		settings, err := decodeHookDocument(runtime, previous)
		if err != nil {
			return "", err
		}
		hooks, entries, err := jsonHookTree(runtime, spec.event, settings)
		if err != nil {
			return "", err
		}
		found := false
		for _, entry := range entries {
			for _, hook := range hookCommandsOf(entry, spec.nested) {
				if !matcher.MatchString(commandOf(hook)) {
					continue
				}
				found = true
				hook["command"] = command
			}
		}
		if !found {
			entries = append(entries, jsonHookEntry(spec, command))
		}
		hooks[spec.event] = entries
		next, err := agentcfg.ReplaceMember(previous, "hooks", hooks)
		if err != nil {
			return "", err
		}
		return withDocumentDefaults(next, spec, settings)
	}, true)
}

// withDocumentDefaults adds the top-level members a harness requires beside its
// hooks, such as Cursor's schema version, and never overwrites one the operator
// already declared.
func withDocumentDefaults(text string, spec hookRuntime, settings map[string]any) (string, error) {
	var err error
	for key, value := range spec.document {
		if _, declared := settings[key]; declared {
			continue
		}
		if text, err = agentcfg.ReplaceMember(text, key, value); err != nil {
			return "", err
		}
	}
	return text, nil
}

// uninstallJSONSessionHook takes La Roca's entry back out. A document this
// product cannot read never blocks a withdrawal: the file is left byte for
// byte as it is and the returned warning names the entry to delete by hand.
func uninstallJSONSessionHook(runtime, path string) (agentcfg.Outcome, string, error) {
	spec := hookRuntimes[runtime]
	matcher := sessionHookInvocation(runtime)
	var warning string
	outcome, err := agentcfg.Edit(runtime, path, func(previous string) (string, error) {
		settings, err := decodeHookDocument(runtime, previous)
		if err != nil {
			warning = foreignHookDocumentWarning(runtime, path, spec.event)
			return previous, nil
		}
		hooks, entries, err := jsonHookTree(runtime, spec.event, settings)
		if err != nil {
			warning = foreignHookDocumentWarning(runtime, path, spec.event)
			return previous, nil
		}
		remaining, withdrawn := withoutJSONHook(entries, spec.nested, matcher)
		if !withdrawn {
			return previous, nil
		}
		if len(remaining) == 0 {
			delete(hooks, spec.event)
		} else {
			hooks[spec.event] = remaining
		}
		if len(hooks) == 0 {
			return agentcfg.ReplaceMember(previous, "hooks", nil)
		}
		return agentcfg.ReplaceMember(previous, "hooks", hooks)
	}, false)
	return outcome, warning, err
}

// withoutJSONHook drops La Roca's command, and the group that held it only when
// that group holds nothing else. A group an operator gave a matcher or a name
// of their own keeps both, because emptying it is not the same as owning it.
func withoutJSONHook(entries []any, nested bool, matcher *regexp.Regexp) ([]any, bool) {
	remaining := make([]any, 0, len(entries))
	withdrawn := false
	for _, entry := range entries {
		object, isObject := entry.(map[string]any)
		if !isObject {
			remaining = append(remaining, entry)
			continue
		}
		if !nested {
			if matcher.MatchString(stringMember(object, "command")) {
				withdrawn = true
				continue
			}
			remaining = append(remaining, entry)
			continue
		}
		group, isList := object["hooks"].([]any)
		if !isList {
			remaining = append(remaining, entry)
			continue
		}
		kept := make([]any, 0, len(group))
		ours := false
		for _, raw := range group {
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
		object["hooks"] = kept
		remaining = append(remaining, object)
	}
	return remaining, withdrawn
}

func stringMember(object map[string]any, key string) string {
	value, _ := object[key].(string)
	return value
}

func foreignHookDocumentWarning(runtime, path, event string) string {
	return fmt.Sprintf("warning: %s is not readable as %s hooks, so nothing there was "+
		"changed; remove the hooks.%s entry whose command contains "+
		"`hooks run session --runtime %s` by hand", path, runtime, event, runtime)
}
