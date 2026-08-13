package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/thellmwhisperer/la-roca/internal/artifact"
	"github.com/thellmwhisperer/la-roca/internal/distribution/agentcfg"
	"github.com/thellmwhisperer/la-roca/internal/distribution/skill"
	"github.com/thellmwhisperer/la-roca/internal/provider/config"
	"github.com/thellmwhisperer/la-roca/internal/provider/service"
)

const (
	artifactKindSkill  = "skill"
	artifactKindPrompt = "prompt"
	artifactKindHook   = "hook"
)

func artifactsCommand(env *cliEnv) *cobra.Command {
	var executable string
	var force bool
	command := &cobra.Command{
		Use:    "_artifacts",
		Hidden: true,
		RunE: func(*cobra.Command, []string) error {
			if executable == "" {
				return fmt.Errorf("the artifact refresher needs --executable")
			}
			report, err := env.refreshManagedArtifacts(executable, force)
			if err != nil {
				return err
			}
			return env.printJSON(report)
		},
	}
	command.Flags().StringVar(&executable, "executable", "", "absolute roca binary path")
	command.Flags().BoolVar(&force, "force", false, "replace edited SYSTEM zones")
	return command
}

func (env *cliEnv) artifactRegistry() (string, artifact.Registry, error) {
	paths, err := env.resolvePaths()
	if err != nil {
		return "", artifact.Registry{}, err
	}
	registry, err := artifact.LoadRegistry(paths.Artifacts)
	return paths.Artifacts, registry, err
}

func (env *cliEnv) registerZonedArtifact(kind, runtime, path, desiredSystem string) error {
	registryPath, registry, err := env.artifactRegistry()
	if err != nil {
		return err
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("register managed artifact %s: %w", path, err)
	}
	zones, err := artifact.Parse(string(body))
	if err != nil {
		return fmt.Errorf("register managed artifact %s: %w", path, err)
	}
	desiredChecksum := artifact.Checksum(desiredSystem)
	currentChecksum := artifact.Checksum(zones.System)
	entry, exists := registry.Find(kind, runtime, path)
	if !exists {
		entry = artifact.Entry{Kind: kind, Runtime: runtime, Path: path}
	}
	entry.AvailableVersion = env.build.Version
	entry.Format = "zoned-v1"
	if currentChecksum == desiredChecksum {
		entry.InstalledVersion = env.build.Version
		entry.SystemSHA256 = currentChecksum
	} else if !exists {
		entry.InstalledVersion = "unknown"
		entry.SystemSHA256 = desiredChecksum
	}
	registry.Upsert(entry)
	return artifact.SaveRegistry(registryPath, registry)
}

func (env *cliEnv) registeredArtifact(kind, runtime, path string) (artifact.Entry, bool, error) {
	_, registry, err := env.artifactRegistry()
	if err != nil {
		return artifact.Entry{}, false, err
	}
	entry, ok := registry.Find(kind, runtime, path)
	return entry, ok, nil
}

func (env *cliEnv) unregisterArtifact(kind, runtime, path string) error {
	registryPath, registry, err := env.artifactRegistry()
	if err != nil {
		return err
	}
	if _, err := os.Stat(registryPath); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	key := (artifact.Entry{Kind: kind, Runtime: runtime, Path: path}).Key()
	kept := registry.Entries[:0]
	for _, entry := range registry.Entries {
		if entry.Key() != key {
			kept = append(kept, entry)
		}
	}
	if len(kept) == len(registry.Entries) {
		return nil
	}
	registry.Entries = kept
	return artifact.SaveRegistry(registryPath, registry)
}

func (env *cliEnv) registerHook(path, runtime, system string) error {
	registryPath, registry, err := env.artifactRegistry()
	if err != nil {
		return err
	}
	registry.Upsert(artifact.Entry{
		Kind: artifactKindHook, Runtime: runtime, Path: path,
		InstalledVersion: env.build.Version, AvailableVersion: env.build.Version,
		SystemSHA256: artifact.Checksum(system), Format: "json-fragment-v1",
	})
	return artifact.SaveRegistry(registryPath, registry)
}

func claudeHookSystem(path string) (string, bool, error) {
	body, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	_, _, entries, err := claudeHookSettings(string(body))
	if err != nil {
		return "", false, err
	}
	for _, raw := range entries {
		for _, hook := range commandHooksOf(raw) {
			if !claudeHookInvocation.MatchString(commandOf(hook)) {
				continue
			}
			encoded, err := json.Marshal(claudeAuthorshipCommandHook(commandOf(hook)))
			return string(encoded), true, err
		}
	}
	return "", false, nil
}

type artifactRefreshReport struct {
	Enabled   bool     `json:"enabled"`
	Outdated  int      `json:"outdated"`
	Refreshed int      `json:"refreshed"`
	Diverged  []string `json:"diverged"`
	Proposals []string `json:"proposals"`
}

func (env *cliEnv) refreshManagedArtifacts(executable string, force bool) (artifactRefreshReport, error) {
	paths, err := env.resolvePaths()
	if err != nil {
		return artifactRefreshReport{}, err
	}
	file, err := config.LoadFile(paths.Config)
	if err != nil {
		return artifactRefreshReport{}, err
	}
	registry, err := artifact.LoadRegistry(paths.Artifacts)
	if err != nil {
		return artifactRefreshReport{}, err
	}
	report := artifactRefreshReport{Enabled: file.Features.ArtifactRefresh,
		Diverged: []string{}, Proposals: []string{}}
	if err := env.adoptLegacyArtifacts(paths, executable, &registry, &report); err != nil {
		return report, err
	}
	for index := range registry.Entries {
		entry := &registry.Entries[index]
		entry.AvailableVersion = env.build.Version
		switch entry.Kind {
		case artifactKindSkill:
			out, err := artifact.RefreshFile(artifact.FileRequest{
				Path: entry.Path, System: skill.Content(), LegacySignature: skill.LegacySignature(),
				PreviousSystemSHA256: entry.SystemSHA256, Enabled: report.Enabled, Force: force,
			})
			if err != nil {
				return report, err
			}
			env.finishFileRefresh(entry, out, skill.Content(), &report)
		case artifactKindPrompt:
			out, err := artifact.RefreshFile(artifact.FileRequest{
				Path: entry.Path, System: service.PresentationPrompt(),
				LegacySignature:      service.PresentationPromptSignature(),
				PreviousSystemSHA256: entry.SystemSHA256, Enabled: report.Enabled, Force: force,
			})
			if err != nil {
				return report, err
			}
			env.finishFileRefresh(entry, out, service.PresentationPrompt(), &report)
		case artifactKindHook:
			out, err := refreshClaudeHook(entry.Path, executable, entry.SystemSHA256,
				report.Enabled, force)
			if err != nil {
				return report, err
			}
			env.finishHookRefresh(entry, out, &report)
		}
	}
	sort.Strings(report.Diverged)
	sort.Strings(report.Proposals)
	if err := artifact.SaveRegistry(paths.Artifacts, registry); err != nil {
		return report, err
	}
	return report, nil
}

func (env *cliEnv) finishFileRefresh(entry *artifact.Entry, out artifact.FileOutcome,
	desired string, report *artifactRefreshReport) {
	if out.Diverged {
		report.Diverged = append(report.Diverged, entry.Path)
		report.Outdated++
		return
	}
	if out.Changed {
		report.Refreshed++
	}
	body, err := os.ReadFile(entry.Path)
	if err != nil {
		report.Outdated++
		return
	}
	zones, err := artifact.Parse(string(body))
	if err != nil || artifact.Checksum(zones.System) != artifact.Checksum(desired) {
		report.Outdated++
		return
	}
	entry.SystemSHA256 = artifact.Checksum(zones.System)
	entry.Format = "zoned-v1"
	entry.InstalledVersion = env.build.Version
}

type hookRefreshOutcome struct {
	Changed, Diverged, Current bool
	Backup                     string
	SystemSHA256               string
}

func (env *cliEnv) finishHookRefresh(entry *artifact.Entry, out hookRefreshOutcome,
	report *artifactRefreshReport) {
	if out.Diverged {
		report.Diverged = append(report.Diverged, entry.Path)
		report.Outdated++
		return
	}
	if out.Changed {
		report.Refreshed++
	}
	if !out.Current {
		report.Outdated++
		return
	}
	entry.SystemSHA256 = out.SystemSHA256
	entry.Format = "json-fragment-v1"
	entry.InstalledVersion = env.build.Version
}

func (env *cliEnv) adoptLegacyArtifacts(paths config.Paths, executable string,
	registry *artifact.Registry, report *artifactRefreshReport) error {
	for _, runtime := range skill.Runtimes() {
		path, err := skill.Path(runtime, paths.Home, os.Getenv)
		if err != nil {
			return err
		}
		if _, exists := registry.Find(artifactKindSkill, runtime, path); exists {
			continue
		}
		if body, err := os.ReadFile(path); err == nil {
			registry.Upsert(discoveredFileEntry(artifactKindSkill, runtime, path,
				string(body), env.build.Version))
			continue
		}
		root := filepath.Dir(filepath.Dir(filepath.Dir(path)))
		if info, err := os.Stat(root); err == nil && info.IsDir() {
			report.Proposals = append(report.Proposals, "roca skill install "+runtime)
		}
	}
	prompt := filepath.Join(filepath.Dir(paths.DB), "prompt.md")
	if _, exists := registry.Find(artifactKindPrompt, "", prompt); !exists {
		if body, err := os.ReadFile(prompt); err == nil {
			registry.Upsert(discoveredFileEntry(artifactKindPrompt, "", prompt,
				string(body), env.build.Version))
		}
	}
	settings, err := claudeSettingsPath()
	if err != nil {
		return err
	}
	if _, exists := registry.Find(artifactKindHook, "claude", settings); !exists {
		system, found, err := claudeHookSystem(settings)
		if err != nil {
			return err
		}
		if found {
			registry.Upsert(artifact.Entry{Kind: artifactKindHook, Runtime: "claude", Path: settings,
				InstalledVersion: "legacy", AvailableVersion: env.build.Version,
				SystemSHA256: artifact.Checksum(system), Format: "json-fragment-v1"})
		} else if info, err := os.Stat(filepath.Dir(settings)); err == nil && info.IsDir() {
			report.Proposals = append(report.Proposals, "roca hooks install claude")
		}
	}
	return nil
}

// discoveredFileEntry records what is on disk, not what this release wants: a
// registry that stated the desired checksum for a pre-zone file would be
// claiming an install that never happened.
func discoveredFileEntry(kind, runtime, path, body, version string) artifact.Entry {
	checksum, format := artifact.Checksum(body), "legacy"
	if zones, err := artifact.Parse(body); err == nil {
		checksum, format = artifact.Checksum(zones.System), "zoned-v1"
	}
	return artifact.Entry{Kind: kind, Runtime: runtime, Path: path,
		InstalledVersion: "legacy", AvailableVersion: version,
		SystemSHA256: checksum, Format: format}
}

func canonicalClaudeHookSystem(executable string) (string, error) {
	encoded, err := json.Marshal(claudeAuthorshipCommandHook(claudeHookCommand(executable)))
	return string(encoded), err
}

func refreshClaudeHook(path, executable, previousChecksum string,
	enabled, force bool) (hookRefreshOutcome, error) {
	desired, err := canonicalClaudeHookSystem(executable)
	if err != nil {
		return hookRefreshOutcome{}, err
	}
	out := hookRefreshOutcome{SystemSHA256: artifact.Checksum(desired)}
	current, found, err := claudeHookSystem(path)
	if err != nil {
		return out, err
	}
	currentChecksum := ""
	currentCommand := ""
	if found {
		currentChecksum = artifact.Checksum(current)
		var hook map[string]any
		if err := json.Unmarshal([]byte(current), &hook); err != nil {
			return out, err
		}
		currentCommand = commandOf(hook)
	}
	out.Diverged = currentChecksum != previousChecksum
	if out.Diverged && !force {
		return out, nil
	}
	if currentChecksum == out.SystemSHA256 {
		out.Current = true
		out.Diverged = false
		return out, nil
	}
	if !enabled {
		out.Diverged = false
		return out, nil
	}
	changed, err := agentcfg.Edit("claude", path, func(previous string) (string, error) {
		if found {
			return replaceClaudeHookCommand(previous, currentCommand, claudeHookCommand(executable))
		}
		settings, hooks, entries, err := claudeHookSettings(previous)
		if err != nil {
			return "", err
		}
		canonical := claudeAuthorshipCommandHook(claudeHookCommand(executable))
		replaced := false
		for _, raw := range entries {
			for _, hook := range commandHooksOf(raw) {
				if claudeHookInvocation.MatchString(commandOf(hook)) {
					for key, value := range canonical {
						hook[key] = value
					}
					replaced = true
					break
				}
			}
		}
		if !replaced {
			entries = append(entries, claudeAuthorshipHookEntry(claudeHookCommand(executable)))
		}
		if hooks == nil {
			hooks = map[string]any{}
			settings["hooks"] = hooks
		}
		hooks["PreToolUse"] = entries
		return encodeClaudeSettings(settings)
	}, true)
	if err != nil {
		return out, err
	}
	out.Changed, out.Backup = changed.Changed, changed.Backup
	out.Current, out.Diverged = true, false
	return out, nil
}

// claudePreToolUseSpan is the byte range the registered hook lives in. The
// reader only ever looks inside PreToolUse, so the byte-preserving edit looks
// there too: an operator who declared the same command under another event owns
// those bytes, and their copy is neither rewritten nor a reason to refuse.
// A span that cannot be located falls back to the whole document, where an
// ambiguous match is still refused rather than guessed at.
func claudePreToolUseSpan(document string) (int, int) {
	var settings, hooks map[string]json.RawMessage
	if json.Unmarshal([]byte(document), &settings) != nil {
		return 0, len(document)
	}
	if json.Unmarshal(settings["hooks"], &hooks) != nil {
		return 0, len(document)
	}
	span := string(hooks["PreToolUse"])
	if span == "" {
		return 0, len(document)
	}
	for offset := 0; offset < len(document); {
		start := strings.Index(document[offset:], span)
		if start < 0 {
			break
		}
		start += offset
		if keyPrecedes(document[:start], "PreToolUse") {
			return start, start + len(span)
		}
		offset = start + 1
	}
	return 0, len(document)
}

// keyPrecedes reports whether `"<key>":` is what sits immediately before a
// value, which is how the raw bytes of one member are told apart from an
// identical value the operator stored under a different key.
func keyPrecedes(before, key string) bool {
	trimmed := strings.TrimRight(before, " \t\r\n")
	if !strings.HasSuffix(trimmed, ":") {
		return false
	}
	trimmed = strings.TrimRight(strings.TrimSuffix(trimmed, ":"), " \t\r\n")
	return strings.HasSuffix(trimmed, `"`+key+`"`)
}

var claudeCommandField = regexp.MustCompile(
	`("command"[ \t\r\n]*:[ \t\r\n]*)("(?:\\.|[^"\\])*")`,
)

func replaceClaudeHookCommand(document, current, desired string) (string, error) {
	from, to := claudePreToolUseSpan(document)
	start, end := -1, -1
	for _, location := range claudeCommandField.FindAllStringSubmatchIndex(document[from:to], -1) {
		var decoded string
		if err := json.Unmarshal([]byte(document[from+location[4]:from+location[5]]), &decoded); err != nil || decoded != current {
			continue
		}
		if start >= 0 {
			return "", fmt.Errorf("Claude settings contain the registered hook command more than once")
		}
		start, end = from+location[4], from+location[5]
	}
	if start < 0 {
		return "", fmt.Errorf("the registered Claude hook command could not be located in its source file")
	}
	encoded, err := json.Marshal(desired)
	if err != nil {
		return "", err
	}
	return document[:start] + string(encoded) + document[end:], nil
}
