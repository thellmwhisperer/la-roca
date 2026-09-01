package cli

import (
	"encoding/json"
	"errors"
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
	artifactKindSkill        = "skill"
	artifactKindSkillCatalog = "skill-catalog"
	artifactKindPrompt       = "prompt"
	artifactKindHook         = "hook"
	artifactKindMCP          = "mcp-config"
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
	paths, err := env.resolvePaths()
	if err != nil {
		return err
	}
	zones, err := artifact.ParseFile(path)
	if err != nil {
		return fmt.Errorf("register managed artifact %s: %w", path, err)
	}
	desiredChecksum := artifact.Checksum(desiredSystem)
	currentChecksum := artifact.Checksum(zones.System)
	_, err = mutateArtifactRegistry(paths.Artifacts, func(registry *artifact.Registry) (bool, error) {
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
		return true, nil
	})
	return err
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
	paths, err := env.resolvePaths()
	if err != nil {
		return err
	}
	if _, err := os.Stat(paths.Artifacts); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	key := (artifact.Entry{Kind: kind, Runtime: runtime, Path: path}).Key()
	_, err = mutateArtifactRegistry(paths.Artifacts, func(registry *artifact.Registry) (bool, error) {
		before := len(registry.Entries)
		removeArtifactEntry(registry, key)
		return len(registry.Entries) != before, nil
	})
	return err
}

func (env *cliEnv) registerHook(path, runtime, system string) error {
	paths, err := env.resolvePaths()
	if err != nil {
		return err
	}
	_, err = mutateArtifactRegistry(paths.Artifacts, func(registry *artifact.Registry) (bool, error) {
		registry.Upsert(artifact.Entry{
			Kind: artifactKindHook, Runtime: runtime, Path: path,
			InstalledVersion: env.build.Version, AvailableVersion: env.build.Version,
			SystemSHA256: artifact.Checksum(system), Format: "json-fragment-v1",
		})
		return true, nil
	})
	return err
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

// divergedArtifact carries why an artifact was left alone, not only which one.
// An edited SYSTEM zone, a deleted file and a file no registry entry stands
// behind all need the same consent to replace, and they are not the same
// sentence to an operator.
type divergedArtifact struct {
	Path         string `json:"path"`
	Missing      bool   `json:"missing,omitempty"`
	Unregistered bool   `json:"unregistered,omitempty"`
}

// artifactFailure is one artifact this refresh could not finish, with the
// reason it could not. Repairable marks the single class force can fix, so a
// permission error, a disk failure or a concurrent-edit refusal is never
// answered with a force command that cannot help or must not be run twice.
type artifactFailure struct {
	Path       string `json:"path"`
	Reason     string `json:"reason"`
	Repairable bool   `json:"repairable,omitempty"`
}

type artifactRefreshReport struct {
	Enabled   bool               `json:"enabled"`
	Outdated  int                `json:"outdated"`
	Refreshed int                `json:"refreshed"`
	Diverged  []divergedArtifact `json:"diverged"`
	// Failed names each artifact this refresh could not finish. One of them must
	// not hide the state of every other registered artifact.
	Failed    []artifactFailure `json:"failed"`
	Backups   []string          `json:"backups"`
	Proposals []string          `json:"proposals"`
}

func (env *cliEnv) refreshManagedArtifacts(executable string, force bool) (report artifactRefreshReport, err error) {
	paths, err := env.resolvePaths()
	if err != nil {
		return artifactRefreshReport{}, err
	}
	file, err := config.LoadFile(paths.Config)
	if err != nil {
		return artifactRefreshReport{}, err
	}
	release, err := lockArtifactRegistry(paths.Artifacts)
	if err != nil {
		return artifactRefreshReport{}, err
	}
	defer func() { err = errors.Join(err, release()) }()
	registry, err := artifact.LoadRegistry(paths.Artifacts)
	if err != nil {
		return artifactRefreshReport{}, err
	}
	report = artifactRefreshReport{Enabled: file.Features.ArtifactRefresh,
		Diverged: []divergedArtifact{}, Failed: []artifactFailure{},
		Backups: []string{}, Proposals: []string{}}
	if err := env.adoptLegacyArtifacts(paths, executable, &registry, &report); err != nil {
		return report, err
	}
	// The catalog skill's desired body is composed from the installed plugins,
	// not shipped inside the binary, so it is built once and only when a
	// registered entry actually wants it.
	var catalog string
	catalogReady := false
	desiredCatalog := func() string {
		if !catalogReady {
			catalogReady = true
			if composed, err := env.composedCatalogSkill(); err == nil {
				catalog = composed
			} else {
				report.failed("the semantic catalog skill", err, "")
			}
		}
		return catalog
	}
	for index := range registry.Entries {
		entry := &registry.Entries[index]
		if entry.Runtime != "" && !skill.AutomaticallyManaged(entry.Runtime) {
			continue
		}
		entry.AvailableVersion = env.build.Version
		switch entry.Kind {
		case artifactKindSkill, artifactKindSkillCatalog, artifactKindPrompt:
			desired, signature := desiredFileContent(entry.Kind, entry.Path, desiredCatalog)
			if desired == "" {
				continue
			}
			out, err := artifact.RefreshFile(artifact.FileRequest{
				Path: entry.Path, System: desired, LegacySignature: signature,
				PreviousSystemSHA256: entry.SystemSHA256, Enabled: report.Enabled, Force: force,
			})
			if err != nil {
				report.failed(entry.Path, err, out.Backup)
				continue
			}
			env.finishFileRefresh(entry, out, desired, &report)
		case artifactKindHook:
			out, err := refreshClaudeHook(entry.Path, executable, entry.SystemSHA256,
				report.Enabled, force)
			if err != nil {
				report.failed(entry.Path, err, out.Backup)
				continue
			}
			env.finishHookRefresh(entry, out, &report)
		}
	}
	sort.Slice(report.Diverged, func(i, j int) bool {
		return report.Diverged[i].Path < report.Diverged[j].Path
	})
	sort.Slice(report.Failed, func(i, j int) bool {
		return report.Failed[i].Path < report.Failed[j].Path
	})
	sort.Strings(report.Backups)
	sort.Strings(report.Proposals)
	if err := artifact.SaveRegistry(paths.Artifacts, registry); err != nil {
		return report, err
	}
	return report, nil
}

// desiredFileContent says what this release wants one zoned file artifact to
// hold, and which older shipped text it still recognizes as its own. The two
// shipped kinds read their text from the binary; the generated catalog composes
// its own from the installed plugins and has no legacy form — it is a new
// artifact, so a pre-zone file at its path is the operator's.
func desiredFileContent(kind, path string, composedCatalog func() string) (string, string) {
	if kind == artifactKindSkillCatalog {
		return composedCatalog(), ""
	}
	if kind == artifactKindPrompt {
		return service.PresentationPrompt(), service.PresentationPromptSignature()
	}
	if body, legacy := skill.ContentForPath(path); body != "" {
		return body, legacy
	}
	return skill.Content(), skill.LegacySignature()
}

func (report *artifactRefreshReport) failed(path string, err error, backup string) {
	reason := err.Error()
	if !strings.Contains(reason, path) {
		reason = path + ": " + reason
	}
	report.Failed = append(report.Failed, artifactFailure{
		Path: path, Reason: reason, Repairable: errors.Is(err, artifact.ErrBrokenZones),
	})
	if backup != "" {
		report.Backups = append(report.Backups, backup)
	}
	report.Outdated++
}

func (report *artifactRefreshReport) diverged(path string, missing, unregistered bool) {
	report.Diverged = append(report.Diverged,
		divergedArtifact{Path: path, Missing: missing, Unregistered: unregistered})
	report.Outdated++
}

// noteRefresh records the part of an outcome a file and a hook report the same
// way, and answers whether the entry may still be stamped: a diverged artifact
// was left alone, so nothing about it is this release's.
func (report *artifactRefreshReport) noteRefresh(path string,
	diverged, missing, unregistered, changed bool, backup string) bool {
	if diverged {
		report.diverged(path, missing, unregistered)
		return false
	}
	if changed {
		report.Refreshed++
	}
	if backup != "" {
		report.Backups = append(report.Backups, backup)
	}
	return true
}

func (env *cliEnv) finishFileRefresh(entry *artifact.Entry, out artifact.FileOutcome,
	desired string, report *artifactRefreshReport) {
	if !report.noteRefresh(entry.Path, out.Diverged, out.Missing, out.Unregistered,
		out.Changed, out.Backup) {
		return
	}
	zones, err := artifact.ParseFile(entry.Path)
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
	// Missing means the registered entry is no longer in the settings document,
	// which is a withdrawal by the operator rather than an edit to our fragment.
	Missing      bool
	Backup       string
	SystemSHA256 string
}

func (env *cliEnv) finishHookRefresh(entry *artifact.Entry, out hookRefreshOutcome,
	report *artifactRefreshReport) {
	if !report.noteRefresh(entry.Path, out.Diverged, out.Missing, false,
		out.Changed, out.Backup) {
		return
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
		if !skill.AutomaticallyManaged(runtime) {
			continue
		}
		proposed := false
		for _, embedded := range skill.EmbeddedSkills() {
			path, err := skill.NamedPath(runtime, embedded.Name, paths.Home, os.Getenv)
			if err != nil {
				return err
			}
			if _, exists := registry.Find(artifactKindSkill, runtime, path); exists {
				continue
			}
			if body, err := os.ReadFile(path); err == nil {
				content := string(body)
				_, zonedErr := artifact.Parse(content)
				legacyOwned := embedded.Legacy != "" && strings.HasPrefix(content, embedded.Legacy)
				if zonedErr == nil || legacyOwned {
					registry.Upsert(discoveredFileEntry(artifactKindSkill, runtime, path,
						content, env.build.Version))
				}
				continue
			}
			if embedded.Name != skill.SkillName || proposed {
				continue
			}
			root := filepath.Dir(filepath.Dir(filepath.Dir(path)))
			if info, err := os.Stat(root); err == nil && info.IsDir() {
				report.Proposals = append(report.Proposals, "roca skill install "+runtime)
				proposed = true
			}
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
	out.Missing = !found
	if out.Diverged && !force {
		return out, nil
	}
	if currentChecksum == out.SystemSHA256 {
		out.Current = true
		out.Diverged = false
		return out, nil
	}
	// A refresh that is turned off reports what it found and mutates nothing, so
	// the divergence stands: forcing while the feature is off must not turn an
	// edited SYSTEM fragment into a file merely called outdated, which is how the
	// hook came to say less about itself than a zoned file does.
	if !enabled {
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
