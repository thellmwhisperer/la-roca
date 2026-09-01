package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/thellmwhisperer/la-roca/internal/artifact"
	"github.com/thellmwhisperer/la-roca/internal/distribution/agentcfg"
	"github.com/thellmwhisperer/la-roca/internal/distribution/lifecycle"
	"github.com/thellmwhisperer/la-roca/internal/distribution/logfile"
	"github.com/thellmwhisperer/la-roca/internal/distribution/plugininstall"
	"github.com/thellmwhisperer/la-roca/internal/distribution/rocavector"
	"github.com/thellmwhisperer/la-roca/internal/distribution/skill"
	"github.com/thellmwhisperer/la-roca/internal/provider"
	"github.com/thellmwhisperer/la-roca/internal/provider/config"
	"github.com/thellmwhisperer/la-roca/internal/provider/plugin"
	"github.com/thellmwhisperer/la-roca/internal/provider/service"
	"github.com/thellmwhisperer/la-roca/internal/securefile"
)

const legacyCredentialsDir = "credentials"

var legacyProviderCredentialFiles = map[string]string{
	provider.NameCodex: "codex.json",
	"deepseek":         "deepseek.key",
	"zai":              "zai.key",
	"xai":              "xai.key",
}

// uninstallCommand leaves the machine as it was.
//
// Interactive by default, with `--keep-data` and `--purge` for scripts. The
// default answer keeps the
// data: a question whose Enter key deletes a corpus is a trap, and the operator
// who wants it gone types `n` and gets it.
func uninstallCommand(env *cliEnv) *cobra.Command {
	var keepData, purge bool
	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Remove La Roca, and with your consent its data too",
		Long: "Unlinks the binary, withdraws La Roca's entry from every agent\n" +
			"configuration it was declared in, and asks whether to keep the database.\n\n" +
			"The purge converges over the state it finds: it runs on a machine a\n" +
			"previous attempt left halfway, it can be run twice, and it deletes nothing\n" +
			"La Roca did not create. Whatever it refuses to delete is named, with the\n" +
			"reason, so the operator can finish the job themselves.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if keepData && purge {
				return fmt.Errorf("--keep-data and --purge ask for opposite things")
			}
			// One reader for both questions. A second buffered reader over the
			// same standard input finds the answer to the second question
			// already swallowed by the first, and reads a `y` as silence.
			input := bufio.NewReader(cmd.InOrStdin())
			wipe := purge
			if !keepData && !purge {
				answer, err := env.askAboutTheData(input)
				if err != nil {
					return err
				}
				wipe = answer
			}
			return env.uninstall(cmd, input, wipe)
		},
	}
	cmd.Flags().BoolVar(&keepData, "keep-data", false, "remove the binary and keep the database")
	cmd.Flags().BoolVar(&purge, "purge", false, "remove the binary and the data")
	return cmd
}

// askAboutTheData is the question from the operator's own flow, worded the way
// the operator sees it. `n` purges; Enter keeps, because the destructive answer may not
// be the one a distracted operator gives by reflex.
func (env *cliEnv) askAboutTheData(in io.Reader) (bool, error) {
	paths, err := env.resolvePaths()
	if err != nil {
		return false, err
	}
	fmt.Fprintf(env.errOut, "Keep the Roca database and data at %s? [Y/n]: ",
		dirOf(paths.DB))

	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && strings.TrimSpace(line) == "" {
		// Nobody answered. Silence is read as the answer that can be taken
		// back, which is the one that keeps the data.
		fmt.Fprintln(env.errOut, "no answer: your data stays where it is")
		return false, nil
	}
	return strings.EqualFold(strings.TrimSpace(line), "n"), nil
}

// uninstall reverts the integrations and applies the removal plan.
//
// The integrations go first, and on purpose: they name the binary, so an agent
// left pointing at a file that is gone is the one residue an operator does not
// find until their next session fails to start.
func (env *cliEnv) uninstall(cmd *cobra.Command, in io.Reader, purge bool) (returnErr error) {
	paths, err := env.resolvePaths()
	if err != nil {
		return err
	}
	lockTimeout := env.zcodeLockTimeout()
	if ctx := cmd.Context(); ctx != nil {
		if deadline, ok := ctx.Deadline(); ok {
			if remaining := time.Until(deadline); remaining < lockTimeout {
				lockTimeout = remaining
			}
		}
	}
	releaseZcode, err := env.lockManagedZcodeLifecycleWithin(lockTimeout)
	if err != nil {
		return fmt.Errorf("lock ZCode lifecycle for uninstall: %w", err)
	}
	zcodeLockPath := paths.Artifacts + ".zcode.lock"
	env.zcodeLifecycleLocked = true
	releasedZcode := false
	defer func() {
		if !releasedZcode {
			env.zcodeLifecycleLocked = false
			returnErr = errors.Join(returnErr, releaseZcode())
		}
	}()
	report := lifecycle.Report{Purged: true, Deleted: []string{}}
	runtimes := env.withdrawTheIntegrations(&report, purge)
	withdrawalFailed := len(report.Errors) > 0

	// A binary that cannot say where it is is one this command leaves alone: the
	// rest of the uninstall still happens and the operator deletes one file.
	running, _ := os.Executable()
	binary := running
	if withdrawalFailed {
		binary = ""
		if running != "" {
			report.Kept = append(report.Kept, lifecycle.Kept{
				Path: running, Reason: "integration withdrawal failed; retained to keep active declarations callable",
			})
		}
	}
	plan := lifecycle.Plan{Binary: binary}
	var applied lifecycle.Report
	dataDir := dirOf(paths.DB)
	if purge {
		// The execution is recorded before the operator-authorized purge removes
		// the log directory itself. Execute then suppresses its ordinary post-run
		// record so uninstall leaves the promised zero residue.
		if !env.started.IsZero() {
			env.capture(map[string]any{"purge_requested": true})
			// The trace never fails the command, and least of all this one: the
			// operator authorized a purge. A log that could not be written is
			// named in the report and the purge goes on. prelogged is set either
			// way, so the ordinary post-run record cannot recreate the log
			// directory the purge is about to remove.
			if err := env.logExecution(cmd, env.started, ExitOK, nil); err != nil {
				failed(&report, "record this run before the purge: %v", err)
			}
			env.prelogged = true
		}
		// The archived plugin data is decided before the sweep and stays outside
		// the inventory unless the operator just said so with their own answer.
		archives, keptArchives := env.consentToCustody(in, paths)
		report.Kept = append(report.Kept, keptArchives...)
		applied = applyPurge(dataDir, func() lifecycle.Plan {
			owned := slices.DeleteFunc(ownedPaths(paths), func(path string) bool {
				return path == zcodeLockPath
			})
			return lifecycle.Plan{Binary: binary,
				Owned: append(owned, archives...), DataDir: dataDir}
		})
		if len(keptArchives) > 0 {
			applied.Kept = exceptCustodyTree(applied.Kept, custodyRoot(paths))
		}
		for index := range applied.Kept {
			if applied.Kept[index].Path == zcodeLockPath {
				applied.Kept[index].Reason = "retained so concurrent ZCode operations remain serialized"
			}
		}
	} else {
		applied = plan.Apply()
	}
	report.Deleted = append(report.Deleted, applied.Deleted...)
	report.Kept = append(report.Kept, applied.Kept...)
	report.Errors = append(report.Errors, applied.Errors...)
	report.Purged = report.Purged && applied.Purged

	env.zcodeLifecycleLocked = false
	if err := releaseZcode(); err != nil {
		failed(&report, "release ZCode lifecycle lock: %v", err)
	}
	releasedZcode = true

	if !report.Purged {
		env.code = ExitError
	}
	if env.json {
		kept := withoutDBKept(report.Kept, paths.DB)
		return env.printJSON(map[string]any{
			"purged": purge && report.Purged,
			// With no daemon there is no process to stop: every command opens the
			// database, works and exits.
			"stopped":    true,
			"deleted":    withoutDBPaths(report.Deleted, paths.DB),
			"kept":       kept,
			"kept_count": len(kept),
			"runtimes":   runtimes,
			"errors":     scrubDBPaths(report.Errors, paths.DB),
		})
	}
	renderUninstall(env, purge, report, runtimes)
	return nil
}

func applyPurge(dataDir string, plan func() lifecycle.Plan) lifecycle.Report {
	report := lifecycle.Report{Purged: true, Deleted: []string{}}
	logs := logfile.New(dataDir)
	if info, err := os.Lstat(filepath.Dir(logs.LockPath())); err == nil && !info.IsDir() {
		return plan().Apply()
	}
	release, err := logs.Lock()
	if err != nil {
		failed(&report, "lock product logs for purge: %v", err)
		return report
	}
	report = plan().Apply()
	if err := release(); err != nil {
		failed(&report, "release the product log lock after purge: %v", err)
	}
	final := lifecycle.Plan{
		Owned:   []string{logs.LockPath(), filepath.Dir(logs.LockPath())},
		DataDir: dataDir,
	}.Apply()
	return reconcilePurge(report, final, logs.LockPath())
}

func reconcilePurge(report, final lifecycle.Report, lockPath string) lifecycle.Report {
	report.Deleted = append(report.Deleted, final.Deleted...)
	report.Kept = append(report.Kept, final.Kept...)
	report.Errors = append(report.Errors, final.Errors...)
	if _, err := os.Lstat(lockPath); os.IsNotExist(err) {
		remaining := report.Errors[:0]
		prefix := "delete " + lockPath + ":"
		for _, failure := range report.Errors {
			if !strings.HasPrefix(failure, prefix) {
				remaining = append(remaining, failure)
			}
		}
		report.Errors = remaining
	}
	kept := report.Kept[:0]
	seen := map[string]bool{}
	for _, survivor := range report.Kept {
		if _, err := os.Lstat(survivor.Path); (err == nil || !os.IsNotExist(err)) && !seen[survivor.Path] {
			kept = append(kept, survivor)
			seen[survivor.Path] = true
		}
	}
	report.Kept = kept
	report.Purged = len(report.Errors) == 0
	return report
}

// failed records an error and takes the verdict down with it. An error appended
// without touching Purged printed "purged: yes" directly under its own error
// lines, which is the divergence the readable report exists to prevent.
func failed(report *lifecycle.Report, format string, args ...any) {
	report.Errors = append(report.Errors, fmt.Sprintf(format, args...))
	report.Purged = false
}

type zcodeLifecycleLockResult struct {
	release func() error
	err     error
}

func (env *cliEnv) lockManagedZcodeLifecycleWithin(timeout time.Duration) (func() error, error) {
	acquired := make(chan zcodeLifecycleLockResult)
	cancelled := make(chan struct{})
	go func() {
		release, err := env.lockManagedZcodeLifecycleUnbounded()
		result := zcodeLifecycleLockResult{release: release, err: err}
		select {
		case acquired <- result:
		case <-cancelled:
			if release != nil {
				_ = release()
			}
		}
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case result := <-acquired:
		return result.release, result.err
	case <-timer.C:
		close(cancelled)
		return nil, fmt.Errorf("timed out after %s", timeout)
	}
}

// withdrawTheIntegrations takes La Roca's entry out of every runtime's own MCP
// configuration.
//
// A runtime whose file is not there is not an error and not a line of output: a
// machine where the operator never installed `hermes` is the normal case, and
// one such line for every supported runtime would be noise.
func (env *cliEnv) withdrawTheIntegrations(report *lifecycle.Report, purge bool) []agentcfg.Outcome {
	var outcomes []agentcfg.Outcome
	if !env.zcodeLifecycleLocked {
		release, err := env.lockManagedZcodeLifecycle()
		if err != nil {
			failed(report, "lock ZCode lifecycle: %v", err)
			return outcomes
		}
		env.zcodeLifecycleLocked = true
		defer func() {
			env.zcodeLifecycleLocked = false
			if err := release(); err != nil {
				failed(report, "release ZCode lifecycle lock: %v", err)
			}
		}()
	}
	registryPath, registry, registryErr := env.artifactRegistry()
	registryExists := false
	if registryErr != nil {
		failed(report, "read managed artifact registry: %v", registryErr)
	} else if _, err := os.Stat(registryPath); err == nil {
		registryExists = true
	}
	// What changed is named, what failed is named, and what was left behind is
	// named.
	withdrawn := func(what string, outcome agentcfg.Outcome, err error) {
		if err != nil {
			failed(report, "withdraw %s: %v", what, err)
			return
		}
		if outcome.Changed {
			outcomes = append(outcomes, outcome)
			if !purge {
				keepTheBackup(report, outcome)
			}
		}
	}

	purgedMCPPaths := map[string]bool{}
	purgedHookPaths := map[string]bool{}
	if purge && registryErr == nil {
		var zcodeOutcomes []agentcfg.Outcome
		zcodeOutcomes, purgedMCPPaths, purgedHookPaths = env.purgeRegisteredZcodeIntegrations(report, registry)
		outcomes = append(outcomes, zcodeOutcomes...)
	}

	processedMCPPaths := map[string]bool{}
	withdrawMCP := func(runtime, path string) (agentcfg.Outcome, error) {
		if runtime != agentcfg.RuntimeZcode {
			return agentcfg.Uninstall(runtime, path)
		}
		if registryErr != nil {
			return agentcfg.Outcome{Runtime: runtime, Path: path}, fmt.Errorf("ownership registry unavailable")
		}
		if purge {
			return env.uninstallZcodeMCP(path, func(entry artifact.Entry, identity os.FileInfo) error {
				removeRecoveryBackups(report, path)
				return cleanupCreatedZcodeMCPPaths(entry, path, identity)
			})
		}
		return env.uninstallZcodeMCP(path)
	}

	for _, runtime := range agentcfg.Runtimes() {
		path, err := configFileOf(runtime, "")
		if err != nil {
			failed(report, "%s", err)
			continue
		}
		key := runtime + "\x00" + path
		processedMCPPaths[key] = true
		if purgedMCPPaths[key] {
			continue
		}
		outcome, err := withdrawMCP(runtime, path)
		withdrawn("roca from "+runtime, outcome, err)
		if purge {
			removeRecoveryBackups(report, path)
		}
	}
	if registryErr == nil {
		for _, entry := range registry.Entries {
			key := entry.Runtime + "\x00" + entry.Path
			if entry.Kind != artifactKindMCP || entry.Runtime != agentcfg.RuntimeZcode ||
				processedMCPPaths[key] || purgedMCPPaths[key] {
				continue
			}
			processedMCPPaths[key] = true
			outcome, err := withdrawMCP(entry.Runtime, entry.Path)
			withdrawn("roca from "+entry.Runtime+" at "+entry.Path, outcome, err)
			if purge {
				removeRecoveryBackups(report, entry.Path)
			}
		}
	}

	// The Claude signing hook lives in a different file from Claude's MCP
	// declaration and names the binary this command is about to unlink, so a
	// hook left behind fires `roca` on every Bash tool call and finds nothing.
	if settings, err := claudeSettingsPath(); err != nil {
		failed(report, "%s", err)
	} else {
		outcome, warning, err := uninstallClaudeAuthorshipHook(settings)
		if warning != "" {
			fmt.Fprintln(env.errOut, warning)
		}
		withdrawn("the Claude signing hook from "+settings, outcome, err)
		for _, kind := range []string{"pills", "handoff"} {
			session, sessionWarning, sessionErr := uninstallClaudeSessionHook(settings, kind)
			if sessionWarning != "" {
				fmt.Fprintln(env.errOut, sessionWarning)
			}
			withdrawn("the Claude SessionStart "+kind+" hook from "+settings, session, sessionErr)
		}
		if purge {
			removeRecoveryBackups(report, settings)
		}
	}
	type zcodeHookTarget struct {
		settings, wrapper string
		entry             artifact.Entry
		registered        bool
	}
	var zcodeHookTargets []zcodeHookTarget
	seenZcodeWrappers := map[string]int{}
	addZcodeHookTarget := func(wrapper string, entry artifact.Entry, registered bool) {
		if index, found := seenZcodeWrappers[wrapper]; found {
			if registered {
				zcodeHookTargets[index].entry = entry
				zcodeHookTargets[index].registered = true
			}
			return
		}
		settings, err := zcodeHookConfigForWrapper(wrapper)
		if err != nil {
			failed(report, "%s", err)
			return
		}
		seenZcodeWrappers[wrapper] = len(zcodeHookTargets)
		zcodeHookTargets = append(zcodeHookTargets, zcodeHookTarget{
			settings: settings, wrapper: wrapper, entry: entry, registered: registered,
		})
	}
	if wrapper, err := zcodeHookWrapperPath(); err != nil {
		failed(report, "%s", err)
	} else if settings, err := zcodeHookConfigForWrapper(wrapper); err != nil {
		failed(report, "%s", err)
	} else if selected, err := zcodeHookSelected(settings, wrapper); err != nil {
		failed(report, "inspect ZCode hook selection: %v", err)
	} else if selected {
		addZcodeHookTarget(wrapper, artifact.Entry{}, false)
	}
	if registryErr == nil {
		for _, entry := range registry.Entries {
			if entry.Kind == artifactKindHook && entry.Runtime == agentcfg.RuntimeZcode && !purgedHookPaths[entry.Path] {
				addZcodeHookTarget(entry.Path, entry, true)
			}
		}
	}
	for _, target := range zcodeHookTargets {
		var outcome agentcfg.Outcome
		var warning string
		var err error
		if registryErr != nil {
			err = fmt.Errorf("ownership registry unavailable")
		} else if purge && target.registered {
			finalize := func(entry artifact.Entry, identity os.FileInfo) error {
				removeRecoveryBackups(report, target.settings)
				if cleanupErr := cleanupCreatedZcodeHookPaths(entry, target.settings, target.wrapper, identity); cleanupErr != nil {
					return cleanupErr
				}
				return env.unregisterArtifactEntry(entry)
			}
			outcome, warning, err = env.uninstallManagedZcodeHandoffHook(
				target.settings, target.wrapper, finalize)
		} else {
			outcome, warning, err = env.uninstallManagedZcodeHandoffHook(target.settings, target.wrapper)
			if purge {
				removeRecoveryBackups(report, target.settings)
			}
		}
		if warning != "" {
			fmt.Fprintln(env.errOut, warning)
		}
		withdrawn("the ZCode handoff hook from "+target.settings, outcome, err)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		failed(report, "home: %v", err)
		return outcomes
	}
	processedSkillPaths := map[string]bool{}
	removedSkillState := map[string]artifact.Entry{}
	for _, runtime := range skill.Runtimes() {
		// Each embedded skill falls back to this build's own bytes when no
		// registry entry names them. The generated catalog has no shipped bytes,
		// so its unregistered fallback is empty: an empty answer refuses the
		// withdrawal, which is the safe reading of a file this product cannot
		// prove.
		var withdrawals []struct {
			kind, path, fallback string
		}
		for _, embedded := range skill.EmbeddedSkills() {
			path, err := skill.NamedPath(runtime, embedded.Name, home, os.Getenv)
			if err != nil {
				failed(report, "%s", err)
				continue
			}
			withdrawals = append(withdrawals, struct {
				kind, path, fallback string
			}{artifactKindSkill, path, artifact.Checksum(embedded.Body)})
		}
		catalogPath, err := skill.CatalogPath(runtime, home, os.Getenv)
		if err != nil {
			failed(report, "%s", err)
			continue
		}
		withdrawals = append(withdrawals, struct {
			kind, path, fallback string
		}{artifactKindSkillCatalog, catalogPath, ""})
		for _, file := range withdrawals {
			processedSkillPaths[file.kind+"\x00"+runtime+"\x00"+file.path] = true
			checksum := file.fallback
			registeredEntry, registered := registry.Find(file.kind, runtime, file.path)
			if registered {
				checksum = registeredEntry.SystemSHA256
			}
			outcome, err := skill.UninstallWithChecksum(runtime, file.path, checksum)
			if err != nil {
				failed(report, "withdraw skill from %s: %v", runtime, err)
				continue
			}
			if registered && (outcome.Changed || outcome.Missing) {
				removedSkillState[registeredEntry.Key()] = registeredEntry
			}
			if outcome.Changed {
				report.Deleted = append(report.Deleted, outcome.Removed...)
			}
			// The copy this withdrawal just made is spared from the purge and named
			// like any other survivor: it holds the bytes the operator wrote, and the
			// withdrawal made it precisely because they are left nowhere else.
			if purge {
				removeRecoveryBackups(report, file.path, outcome.Backup)
				removeHollowSkillDirs(report, file.path)
			}
			nameSurvivingBackups(report, file.path)
		}
	}
	if registryErr == nil {
		for _, entry := range registry.Entries {
			key := entry.Kind + "\x00" + entry.Runtime + "\x00" + entry.Path
			if entry.Runtime != agentcfg.RuntimeZcode || processedSkillPaths[key] ||
				(entry.Kind != artifactKindSkill && entry.Kind != artifactKindSkillCatalog) {
				continue
			}
			processedSkillPaths[key] = true
			outcome, err := skill.UninstallWithChecksum(entry.Runtime, entry.Path, entry.SystemSHA256)
			if err != nil {
				failed(report, "withdraw skill from %s at %s: %v", entry.Runtime, entry.Path, err)
				continue
			}
			if outcome.Changed || outcome.Missing {
				removedSkillState[entry.Key()] = entry
			}
			if outcome.Changed {
				report.Deleted = append(report.Deleted, outcome.Removed...)
			}
			if purge {
				removeRecoveryBackups(report, entry.Path, outcome.Backup)
				removeHollowSkillDirs(report, entry.Path)
			}
			nameSurvivingBackups(report, entry.Path)
		}
	}
	if registryErr == nil && registryExists {
		removable := map[string]artifact.Entry{}
		for key, entry := range removedSkillState {
			removable[key] = entry
		}
		for _, entry := range registry.Entries {
			if entry.Kind == artifactKindHook && entry.Runtime != agentcfg.RuntimeZcode {
				removable[entry.Key()] = entry
			}
		}
		_, err := mutateArtifactRegistry(registryPath, func(current *artifact.Registry) (bool, error) {
			kept := current.Entries[:0]
			changed := false
			for _, entry := range current.Entries {
				if expected, remove := removable[entry.Key()]; remove && entry == expected {
					changed = true
					continue
				}
				kept = append(kept, entry)
			}
			current.Entries = kept
			return changed, nil
		})
		if err != nil {
			failed(report, "update managed artifact registry: %v", err)
		}
	}
	return outcomes
}

type zcodePurgeGroup struct {
	config string
	mcp    []artifact.Entry
	hooks  []artifact.Entry
}

func zcodeMCPContinuity(config string, entry artifact.Entry) (bool, error) {
	if entry.Executable == "" {
		return false, nil
	}
	return agentcfg.ZcodeMCPMatches(config, entry.Executable)
}

func zcodeHookDeclarationPresent(config string) (bool, error) {
	present, verified := zcodeManagedHookState(config)
	if !verified {
		return false, fmt.Errorf("could not verify ZCode hook markers in %s", config)
	}
	return present, nil
}

func zcodeHookDeclarationContinuity(config string, entry artifact.Entry) (bool, error) {
	configBody, err := os.ReadFile(config)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	next, err := agentcfg.DeclareZcodeSessionStartHook(string(configBody), zcodeSessionStartMarker,
		zcodeOwnedHookCommand(entry.Path), 15000)
	return err == nil && next == string(configBody), err
}

func zcodeRootContinuity(config string, entry artifact.Entry) (bool, bool, error) {
	root := filepath.Dir(filepath.Dir(config))
	identity, err := zcodeRootIdentity(root)
	if os.IsNotExist(err) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	current := artifact.Entry{RootIdentity: identity}
	return continuousZcodeOwnership(entry, current), true, nil
}

func zcodeWrapperContinuity(entry artifact.Entry) (bool, error) {
	expected, err := zcodeWrapperExpectedFromEntry(entry)
	if err != nil {
		return false, err
	}
	info, err := os.Lstat(entry.Path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() {
		return false, nil
	}
	body, err := os.ReadFile(entry.Path)
	if err != nil {
		return false, err
	}
	return string(body) == string(expected), nil
}

func (env *cliEnv) purgeRegisteredZcodeIntegrations(report *lifecycle.Report, registry artifact.Registry) ([]agentcfg.Outcome, map[string]bool, map[string]bool) {
	groups := map[string]*zcodePurgeGroup{}
	mcpPaths := map[string]bool{}
	hookPaths := map[string]bool{}
	groupFor := func(config string) *zcodePurgeGroup {
		group := groups[config]
		if group == nil {
			group = &zcodePurgeGroup{config: config}
			groups[config] = group
		}
		return group
	}
	for _, entry := range registry.Entries {
		if entry.Runtime != agentcfg.RuntimeZcode {
			continue
		}
		switch entry.Kind {
		case artifactKindMCP:
			groupFor(entry.Path).mcp = append(groupFor(entry.Path).mcp, entry)
			mcpPaths[entry.Runtime+"\x00"+entry.Path] = true
		case artifactKindHook:
			config, err := zcodeHookConfigForWrapper(entry.Path)
			if err != nil {
				failed(report, "%s", err)
				continue
			}
			groupFor(config).hooks = append(groupFor(config).hooks, entry)
			hookPaths[entry.Path] = true
		}
	}
	configs := make([]string, 0, len(groups))
	for config := range groups {
		configs = append(configs, config)
	}
	slices.Sort(configs)
	var outcomes []agentcfg.Outcome
	for _, config := range configs {
		group := groups[config]
		stableRelease, err := env.lockManagedZcodeLifecycle()
		if err != nil {
			failed(report, "lock ZCode lifecycle for %s: %v", config, err)
			continue
		}
		groupErr := func() (err error) {
			defer func() { err = errors.Join(err, stableRelease()) }()
			_, configErr := os.Lstat(config)
			configMissing := os.IsNotExist(configErr)
			if configErr != nil && !configMissing {
				return configErr
			}
			replacementReported := false
			reportReplacement := func(rootExists bool) {
				if rootExists && !replacementReported {
					failed(report, "retained replacement ZCode tree at %s", filepath.Dir(filepath.Dir(config)))
					replacementReported = true
				}
			}
			liveHooks := make([]artifact.Entry, 0, len(group.hooks))
			wrapperOnlyHooks := make([]artifact.Entry, 0, len(group.hooks))
			withdrawOnlyHooks := make([]artifact.Entry, 0, len(group.hooks))
			for _, entry := range group.hooks {
				rootContinuous, rootExists, rootErr := zcodeRootContinuity(config, entry)
				if rootErr != nil {
					return rootErr
				}
				if !rootContinuous {
					reportReplacement(rootExists)
					if !configMissing {
						declared, declarationErr := zcodeHookDeclarationPresent(config)
						if declarationErr != nil {
							return declarationErr
						}
						if declared {
							outcome, warning, uninstallErr := uninstallZcodeHandoffHookUnlocked(
								config, entry.Path, nil, false)
							if warning != "" {
								fmt.Fprintln(env.errOut, warning)
							}
							if uninstallErr != nil {
								return fmt.Errorf("withdraw the ZCode handoff hook from %s: %w", config, uninstallErr)
							}
							if outcome.Changed {
								outcomes = append(outcomes, outcome)
							}
							removeRecoveryBackups(report, config)
						}
					}
					if _, wrapperErr := os.Lstat(entry.Path); wrapperErr == nil {
						failed(report, "retained uncertain ZCode hook wrapper %s", entry.Path)
					} else if !os.IsNotExist(wrapperErr) {
						return wrapperErr
					}
					if unregisterErr := env.unregisterArtifactEntry(entry); unregisterErr != nil {
						return unregisterErr
					}
					continue
				}
				if configMissing {
					continuous, continuityErr := zcodeWrapperContinuity(entry)
					if continuityErr != nil {
						return continuityErr
					}
					wrapperMissing := false
					if !continuous {
						_, wrapperErr := os.Lstat(entry.Path)
						wrapperMissing = os.IsNotExist(wrapperErr)
						if wrapperErr != nil && !wrapperMissing {
							return wrapperErr
						}
					}
					if continuous || wrapperMissing {
						liveHooks = append(liveHooks, entry)
						continue
					}
					withdrawOnlyHooks = append(withdrawOnlyHooks, entry)
					failed(report, "retained uncertain ZCode hook artifacts at %s", config)
					continue
				}
				declared, declarationErr := zcodeHookDeclarationPresent(config)
				if declarationErr != nil {
					return declarationErr
				}
				if !declared {
					continuous, continuityErr := zcodeWrapperContinuity(entry)
					if continuityErr != nil {
						return continuityErr
					}
					_, wrapperErr := os.Lstat(entry.Path)
					wrapperMissing := os.IsNotExist(wrapperErr)
					if wrapperErr != nil && !wrapperMissing {
						return wrapperErr
					}
					if continuous || wrapperMissing {
						wrapperOnlyHooks = append(wrapperOnlyHooks, entry)
						continue
					}
					if unregisterErr := env.unregisterArtifactEntry(entry); unregisterErr != nil {
						return unregisterErr
					}
					failed(report, "retained operator-modified ZCode hook wrapper %s", entry.Path)
					continue
				}
				canonical, canonicalErr := zcodeHookDeclarationContinuity(config, entry)
				if canonicalErr != nil {
					return canonicalErr
				}
				continuous, continuityErr := zcodeWrapperContinuity(entry)
				if continuityErr != nil {
					return continuityErr
				}
				if !canonical || !continuous {
					withdrawOnlyHooks = append(withdrawOnlyHooks, entry)
					failed(report, "retained uncertain ZCode hook artifacts at %s", config)
					continue
				}
				liveHooks = append(liveHooks, entry)
			}
			liveMCP := make([]artifact.Entry, 0, len(group.mcp))
			for _, entry := range group.mcp {
				rootContinuous, rootExists, rootErr := zcodeRootContinuity(config, entry)
				if rootErr != nil {
					return rootErr
				}
				if !rootContinuous {
					reportReplacement(rootExists)
					if !configMissing {
						configured, continuityErr := zcodeMCPContinuity(config, entry)
						if continuityErr != nil {
							return continuityErr
						}
						if configured {
							outcome, uninstallErr := agentcfg.UninstallZcodeMCP(config, agentcfg.ZcodeMCPPreimageNone)
							if uninstallErr != nil {
								return fmt.Errorf("withdraw roca from zcode at %s: %w", config, uninstallErr)
							}
							if outcome.Changed {
								outcomes = append(outcomes, outcome)
							}
							removeRecoveryBackups(report, config)
						}
					}
					if unregisterErr := env.unregisterArtifactEntry(entry); unregisterErr != nil {
						return unregisterErr
					}
					continue
				}
				if configMissing {
					liveMCP = append(liveMCP, entry)
					continue
				}
				continuous, continuityErr := zcodeMCPContinuity(config, entry)
				if continuityErr != nil {
					return continuityErr
				}
				if !continuous {
					if unregisterErr := env.unregisterArtifactEntry(entry); unregisterErr != nil {
						return unregisterErr
					}
					failed(report, "retained uncertain ZCode MCP artifacts at %s", config)
					continue
				}
				liveMCP = append(liveMCP, entry)
			}
			for _, entry := range wrapperOnlyHooks {
				expected, expectedErr := zcodeWrapperExpectedFromEntry(entry)
				if expectedErr != nil {
					return expectedErr
				}
				retained, removeErr := removeZcodeWrapper(entry.Path, expected)
				if removeErr != nil {
					return removeErr
				}
				if retained {
					return fmt.Errorf("ZCode hook wrapper changed while reconciling %s", entry.Path)
				}
				if err := env.unregisterArtifactEntry(entry); err != nil {
					return err
				}
			}
			var configIdentity os.FileInfo
			for _, entry := range withdrawOnlyHooks {
				outcome, warning, uninstallErr := uninstallZcodeHandoffHookUnlocked(
					config, entry.Path, nil, entry.CreatedHooksEnabled)
				if warning != "" {
					fmt.Fprintln(env.errOut, warning)
				}
				if uninstallErr != nil {
					return fmt.Errorf("withdraw the ZCode handoff hook from %s: %w", config, uninstallErr)
				}
				if outcome.Changed {
					outcomes = append(outcomes, outcome)
					configIdentity = outcome.FileIdentity
				}
				removeRecoveryBackups(report, config)
				if err := env.unregisterArtifactEntry(entry); err != nil {
					return err
				}
			}
			aggregate := artifact.Entry{}
			wrappers := make([]string, 0, len(liveHooks))
			for _, entry := range liveHooks {
				expected, expectedErr := zcodeWrapperExpectedFromEntry(entry)
				if expectedErr != nil {
					return expectedErr
				}
				outcome, warning, uninstallErr := uninstallZcodeHandoffHookUnlocked(
					config, entry.Path, expected, entry.CreatedHooksEnabled)
				if warning != "" {
					fmt.Fprintln(env.errOut, warning)
				}
				if uninstallErr != nil {
					return fmt.Errorf("withdraw the ZCode handoff hook from %s: %w", config, uninstallErr)
				}
				if outcome.Changed {
					outcomes = append(outcomes, outcome)
					configIdentity = outcome.FileIdentity
				}
				removeRecoveryBackups(report, config)
				aggregateZcodeProvenance(&aggregate, entry)
				wrappers = append(wrappers, entry.Path)
			}
			for _, entry := range liveMCP {
				preimage, preimageErr := zcodeMCPPreimageFromEntry(entry)
				if preimageErr != nil {
					return preimageErr
				}
				outcome, uninstallErr := agentcfg.UninstallZcodeMCP(config, preimage)
				if uninstallErr != nil {
					return fmt.Errorf("withdraw roca from zcode at %s: %w", config, uninstallErr)
				}
				if outcome.Changed {
					outcomes = append(outcomes, outcome)
					configIdentity = outcome.FileIdentity
				}
				removeRecoveryBackups(report, config)
				aggregateZcodeProvenance(&aggregate, entry)
			}
			if err := cleanupCreatedZcodePaths(aggregate, config, wrappers, configIdentity); err != nil {
				return err
			}
			for _, entry := range append(append([]artifact.Entry{}, liveHooks...), liveMCP...) {
				if err := env.unregisterArtifactEntry(entry); err != nil {
					return err
				}
			}
			return nil
		}()
		if groupErr != nil {
			failed(report, "purge ZCode integrations at %s: %v", config, groupErr)
		}
	}
	return outcomes, mcpPaths, hookPaths
}

func aggregateZcodeProvenance(target *artifact.Entry, entry artifact.Entry) {
	target.CreatedRoot = target.CreatedRoot || entry.CreatedRoot
	target.CreatedConfigDir = target.CreatedConfigDir || entry.CreatedConfigDir
	target.CreatedHooksDir = target.CreatedHooksDir || entry.CreatedHooksDir
	target.CreatedConfig = target.CreatedConfig || entry.CreatedConfig
	target.CreatedLock = target.CreatedLock || entry.CreatedLock
}

func cleanupCreatedZcodePaths(entry artifact.Entry, configPath string, wrappers []string, identity os.FileInfo) error {
	configDir := filepath.Dir(configPath)
	root := filepath.Dir(configDir)
	var cleanupErr error
	if entry.CreatedConfig {
		if identity != nil {
			retained, err := removeEmptyZcodeConfigMatching(configPath, identity)
			cleanupErr = errors.Join(cleanupErr, err)
			if retained && err == nil {
				cleanupErr = errors.Join(cleanupErr,
					fmt.Errorf("operator configuration remains in proven-created ZCode config %s", configPath))
			}
		} else if _, identityErr := os.Lstat(configPath); identityErr == nil {
			cleanupErr = errors.Join(cleanupErr,
				fmt.Errorf("operator configuration remains in proven-created ZCode config %s", configPath))
		} else if !os.IsNotExist(identityErr) {
			cleanupErr = errors.Join(cleanupErr, identityErr)
		}
	} else if body, err := os.ReadFile(configPath); err == nil && strings.TrimSpace(string(body)) == "{}" {
		cleanupErr = errors.Join(cleanupErr, fmt.Errorf("unproven ZCode artifact remains at %s", configPath))
	} else if err != nil && !os.IsNotExist(err) {
		cleanupErr = errors.Join(cleanupErr, err)
	}
	if entry.CreatedLock || len(wrappers) > 0 {
		lockPath := filepath.Join(root, ".roca-hooks.lock")
		if entry.CreatedLock {
			retained, err := removeEmptyZcodeLock(lockPath)
			cleanupErr = errors.Join(cleanupErr, err)
			if retained && err == nil {
				cleanupErr = errors.Join(cleanupErr, fmt.Errorf("operator-owned ZCode artifact remains at %s", lockPath))
			}
		} else if _, err := os.Lstat(lockPath); err == nil {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("unproven ZCode artifact remains at %s", lockPath))
		} else if !os.IsNotExist(err) {
			cleanupErr = errors.Join(cleanupErr, err)
		}
	}
	directories := []struct {
		path    string
		created bool
	}{{configDir, entry.CreatedConfigDir}, {root, entry.CreatedRoot}}
	seen := map[string]bool{}
	for _, wrapper := range wrappers {
		directory := filepath.Dir(wrapper)
		if !seen[directory] {
			directories = append([]struct {
				path    string
				created bool
			}{{directory, entry.CreatedHooksDir}}, directories...)
			seen[directory] = true
		}
	}
	for _, directory := range directories {
		if !directory.created {
			continue
		}
		retained, err := removeEmptyZcodeDirectory(directory.path)
		cleanupErr = errors.Join(cleanupErr, err)
		if retained && err == nil {
			cleanupErr = errors.Join(cleanupErr,
				fmt.Errorf("operator-owned ZCode artifact remains at %s", directory.path))
		}
	}
	return cleanupErr
}

func cleanupCreatedZcodeMCPPaths(entry artifact.Entry, configPath string, identity os.FileInfo) error {
	return cleanupCreatedZcodePaths(entry, configPath, nil, identity)
}

func rollbackCreatedZcodeMCPPaths(preimage zcodeMCPPathState, configPath string) error {
	var err error
	if preimage.createdConfig {
		retained, removeErr := removeEmptyZcodeConfig(configPath)
		if retained && removeErr == nil {
			removeErr = fmt.Errorf("operator configuration remains in %s", configPath)
		}
		err = errors.Join(err, removeErr)
	}
	for _, directory := range []struct {
		path    string
		created bool
	}{
		{filepath.Dir(configPath), preimage.createdConfigDir},
		{filepath.Dir(filepath.Dir(configPath)), preimage.createdRoot},
	} {
		if !directory.created {
			continue
		}
		retained, removeErr := removeEmptyZcodeDirectory(directory.path)
		if retained && removeErr == nil {
			removeErr = fmt.Errorf("operator-owned ZCode artifact remains at %s", directory.path)
		}
		err = errors.Join(err, removeErr)
	}
	return err
}

func cleanupCreatedZcodeHookPaths(entry artifact.Entry, configPath, wrapperPath string, identity os.FileInfo) error {
	return cleanupCreatedZcodePaths(entry, configPath, []string{wrapperPath}, identity)
}

func rollbackCreatedZcodeHookPaths(preimage zcodeHookPathState, configPath, wrapperPath string) error {
	root := filepath.Dir(filepath.Dir(wrapperPath))
	var err error
	if preimage.createdConfig {
		retained, removeErr := removeEmptyZcodeConfig(configPath)
		if retained && removeErr == nil {
			removeErr = fmt.Errorf("operator-owned ZCode artifact remains at %s", configPath)
		}
		err = errors.Join(err, removeErr)
	}
	if preimage.createdLock {
		lockPath := filepath.Join(root, ".roca-hooks.lock")
		retained, removeErr := removeEmptyZcodeLock(lockPath)
		if retained && removeErr == nil {
			removeErr = fmt.Errorf("operator-owned ZCode artifact remains at %s", lockPath)
		}
		err = errors.Join(err, removeErr)
	}
	for _, directory := range []struct {
		path    string
		created bool
	}{
		{filepath.Dir(wrapperPath), preimage.createdHooksDir},
		{filepath.Dir(configPath), preimage.createdConfigDir},
		{root, preimage.createdRoot},
	} {
		if directory.created {
			retained, removeErr := removeEmptyZcodeDirectory(directory.path)
			if retained && removeErr == nil {
				removeErr = fmt.Errorf("operator-owned ZCode artifact remains at %s", directory.path)
			}
			err = errors.Join(err, removeErr)
		}
	}
	return err
}

type zcodeArtifactVerifier func(string, os.FileInfo) (bool, error)

func removeOwnedZcodeArtifact(path string, verify zcodeArtifactVerifier, afterRename func(), removeQuarantine func(string) error) (bool, error) {
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return false, nil
	} else if err != nil {
		return true, err
	}
	if verify == nil {
		return true, nil
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-remove-*")
	if err != nil {
		return true, err
	}
	quarantine := temporary.Name()
	if closeErr := temporary.Close(); closeErr != nil {
		os.Remove(quarantine)
		return true, closeErr
	}
	if err := os.Remove(quarantine); err != nil {
		return true, err
	}
	if err := os.Rename(path, quarantine); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return true, err
	}
	if afterRename != nil {
		afterRename()
	}
	restore := func(cause error) (bool, error) {
		if restoreErr := securefile.RenameNoReplace(quarantine, path); restoreErr != nil {
			return true, errors.Join(cause, fmt.Errorf("restore ZCode artifact from %s: %w", quarantine, restoreErr))
		}
		return true, cause
	}
	info, err := os.Lstat(quarantine)
	if err != nil {
		return restore(err)
	}
	owned, err := verify(quarantine, info)
	if err != nil {
		return restore(err)
	}
	if !owned {
		return restore(nil)
	}
	if err := removeQuarantine(quarantine); err != nil {
		return restore(err)
	}
	if _, err := os.Lstat(path); err == nil {
		return true, nil
	} else if !os.IsNotExist(err) {
		return true, err
	}
	return false, nil
}

func zcodeRegularFileVerifier(accept func([]byte) bool) zcodeArtifactVerifier {
	return func(path string, info os.FileInfo) (bool, error) {
		if !info.Mode().IsRegular() {
			return false, nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return false, err
		}
		return accept(body), nil
	}
}

func zcodeEmptyDirectoryVerifier(path string, info os.FileInfo) (bool, error) {
	if !info.IsDir() {
		return false, nil
	}
	entries, err := os.ReadDir(path)
	return len(entries) == 0, err
}

func removeEmptyZcodeConfig(path string) (bool, error) {
	return removeEmptyZcodeConfigAfterQuarantine(path, nil, os.Remove)
}

func removeEmptyZcodeConfigMatching(path string, expected os.FileInfo) (bool, error) {
	return removeOwnedZcodeArtifact(path, func(path string, info os.FileInfo) (bool, error) {
		if !info.Mode().IsRegular() || !os.SameFile(expected, info) {
			return false, nil
		}
		body, err := os.ReadFile(path)
		return strings.TrimSpace(string(body)) == "{}", err
	}, nil, os.Remove)
}

func removeEmptyZcodeConfigAfterQuarantine(path string, afterRename func(), removeQuarantine func(string) error) (bool, error) {
	return removeOwnedZcodeArtifact(path, zcodeRegularFileVerifier(func(body []byte) bool {
		return strings.TrimSpace(string(body)) == "{}"
	}), afterRename, removeQuarantine)
}

func removeEmptyZcodeLock(path string) (bool, error) {
	return removeOwnedZcodeArtifact(path, zcodeRegularFileVerifier(func(body []byte) bool {
		return len(body) == 0
	}), nil, os.Remove)
}

func removeEmptyZcodeDirectory(path string) (bool, error) {
	return removeOwnedZcodeArtifact(path, zcodeEmptyDirectoryVerifier, nil, os.Remove)
}

// removeHollowSkillDirs takes back the chain a skill withdrawal could not,
// because the recovery copies beside the file were still in it when the
// withdrawal tried. Only an empty directory goes: os.Remove is the whole guard.
func removeHollowSkillDirs(report *lifecycle.Report, skillFile string) {
	dir := filepath.Dir(skillFile)
	if !skill.OwnedDir(filepath.Base(dir)) {
		return
	}
	for _, hollow := range []string{dir, filepath.Dir(dir)} {
		if err := os.Remove(hollow); err != nil {
			return
		}
		report.Deleted = append(report.Deleted, hollow)
	}
}

// keepTheBackup names every recovery copy left beside a configuration file the
// withdrawal touched, and removes the one the withdrawal itself just made.
//
// The withdrawal's own copy holds the file with La Roca's entry still in it —
// the exact state being removed — so it is taken back. A regular uninstall
// keeps earlier recovery copies and names them. Purge takes the other branch
// and deletes the whole recovery family under the operator's consent.
func keepTheBackup(report *lifecycle.Report, outcome agentcfg.Outcome) {
	if outcome.Backup != "" {
		os.Remove(outcome.Backup)
	}
	nameSurvivingBackups(report, outcome.Path)
}

// removeRecoveryBackups applies the purge consent to the recovery copies La
// Roca created beside an agent configuration. A regular uninstall keeps and
// names them; --purge removes the whole product-owned family, including copies
// left by a previous interrupted withdrawal.
//
// A copy named in spared is the exception the consent does not reach: what an
// operator wrote is never this product's to delete, whatever it was authorized
// to remove of its own. It is the same rule that keeps a prompt.md with content
// in its USER zone out of the owned-path inventory.
func removeRecoveryBackups(report *lifecycle.Report, configFile string, spared ...string) {
	for _, path := range recoveryBackupsFor(report, configFile) {
		if slices.Contains(spared, path) {
			continue
		}
		if err := os.Remove(path); err != nil {
			failed(report, "delete %s: %v", path, err)
			continue
		}
		report.Deleted = append(report.Deleted, path)
	}
}

// nameSurvivingBackups reports every `config.bak*` beside configFile as a kept
// survivor, so an operator reads what the withdrawal left behind. The live
// config file is excluded: it is named on the "withdrawn from" line.
func nameSurvivingBackups(report *lifecycle.Report, configFile string) {
	for _, path := range recoveryBackupsFor(report, configFile) {
		report.Kept = append(report.Kept, lifecycle.Kept{
			Path:   path,
			Reason: "La Roca's recovery backup of your configuration: delete it yourself if you no longer need it",
		})
	}
}

func recoveryBackupsFor(report *lifecycle.Report, configFile string) []string {
	paths, err := recoveryBackups(configFile)
	if err != nil {
		failed(report, "%s", err)
	}
	return paths
}

func recoveryBackups(configFile string) ([]string, error) {
	dir := filepath.Dir(configFile)
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read recovery backups beside %s: %w", configFile, err)
	}
	base := filepath.Base(configFile) + ".roca.bak"
	var paths []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !recoveryBackupName(name, base) {
			continue
		}
		paths = append(paths, filepath.Join(dir, name))
	}
	return paths, nil
}

func recoveryBackupName(name, base string) bool {
	if name == base {
		return true
	}
	suffix := strings.TrimPrefix(name, base+".")
	if suffix == name || suffix == "" {
		return false
	}
	_, err := strconv.ParseUint(suffix, 10, 64)
	return err == nil
}

// ownedPaths is the declaration of exact product paths and product file-name
// families. Directories are included only so lifecycle can remove them empty.
//
// The journals are named explicitly because SQLite writes them beside the
// database and a WAL left behind is a file with the operator's data in it.
func ownedPaths(paths config.Paths) []string {
	dataDir := dirOf(paths.DB)
	owned := []string{
		paths.DB, paths.DB + "-wal", paths.DB + "-shm", paths.DB + "-journal",
		paths.Config, paths.Reconciliation,
	}
	managed, err := artifact.OwnedPaths(paths.Artifacts)
	if err != nil {
		managed = []string{paths.Artifacts, paths.Artifacts + ".lock", paths.Artifacts + ".mcp.lock", paths.Artifacts + ".hooks.lock", paths.Artifacts + ".zcode.lock"}
	}
	for _, path := range managed {
		if !slices.Contains(owned, path) {
			owned = append(owned, path)
		}
	}
	prompt := filepath.Join(dataDir, "prompt.md")
	if !slices.Contains(owned, prompt) {
		if body, err := os.ReadFile(prompt); err == nil && promptWasGenerated(string(body)) {
			owned = append(owned, prompt)
		}
	}
	// Every refresh that rewrote a managed artifact left a recovery copy beside
	// it. They are this product's files, and one left behind keeps the data
	// directory alive while being reported as a file La Roca never created.
	recovery, _ := recoveryBackups(prompt)
	for _, path := range recovery {
		if !slices.Contains(owned, path) {
			owned = append(owned, path)
		}
	}
	backupPrefix := strings.TrimSuffix(filepath.Base(paths.DB), ".db") + "."
	backups, backupsExist := ownedFiles(paths.Backups, func(name string) bool {
		if !strings.HasPrefix(name, backupPrefix) || !strings.HasSuffix(name, ".backup.db") {
			return false
		}
		stamp := strings.TrimSuffix(strings.TrimPrefix(name, backupPrefix), ".backup.db")
		_, err := time.Parse("20060102T150405Z", stamp)
		return err == nil
	})
	owned = append(owned, backups...)
	if backupsExist {
		owned = append(owned, paths.Backups)
	}
	cacheDir := filepath.Join(dataDir, "cache")
	if realDirectory(cacheDir) {
		owned = append(owned, filepath.Join(cacheDir, modelsDevCacheFile), cacheDir)
	}
	credentialsDir := filepath.Join(dataDir, legacyCredentialsDir)
	if realDirectory(credentialsDir) {
		credentialPaths := legacyProviderCredentialPaths(dataDir)
		for _, name := range slices.Sorted(maps.Keys(credentialPaths)) {
			owned = append(owned, credentialPaths[name])
		}
		owned = append(owned, credentialsDir)
	}
	logDir := filepath.Join(dataDir, logfile.DirName)
	logs, logsExist := ownedFiles(logDir, ownedLogName)
	owned = append(owned, logs...)
	if logsExist {
		owned = append(owned, logfile.New(dataDir).LockPath(), logDir)
	}
	return append(owned, installedPluginPaths(paths)...)
}

// promptWasGenerated recognizes prompt.md by the heading every release has
// written, so a purge still owns the file an older release generated instead of
// reporting a file init wrote as somebody else's. An operator zone with content
// in it is never ours to delete, whatever release opened the file.
func promptWasGenerated(body string) bool {
	zones, err := artifact.Parse(body)
	if err != nil {
		return strings.HasPrefix(body, service.PresentationPromptSignature())
	}
	return zones.User == "" &&
		strings.HasPrefix(zones.System, service.PresentationPromptSignature())
}

// The three trees the plugin system writes. They hang off ~/.roca and not off
// the resolved data directory: a plugin is installed for the operator, not for
// one database, and an operator who moved their database with `--db-path` still
// finds their plugins here.
const (
	pluginDirName        = "plugins"
	pluginCustodyDirName = "plugin-custody"
	pluginDownloadsDir   = ".plugin-downloads"
)

func pluginRoot(paths config.Paths) string { return ownedTree(paths, pluginDirName) }

func custodyRoot(paths config.Paths) string { return ownedTree(paths, pluginCustodyDirName) }

func pluginDownloads(paths config.Paths) string { return ownedTree(paths, pluginDownloadsDir) }

func ownedTree(paths config.Paths, name string) string {
	if paths.Home == "" {
		return ""
	}
	return filepath.Join(paths.Home, config.DirOwn, name)
}

// pluginExecutableDir is where the installer put `roca-<name>`, resolved the way
// `roca plugin install` resolved it. The purge deletes an executable only from
// here, which is the containment an update already refuses to cross.
func pluginExecutableDir(paths config.Paths) string {
	if bin := os.Getenv(envRocaPrefix); bin != "" {
		return bin
	}
	if paths.Home == "" {
		return ""
	}
	return filepath.Join(paths.Home, ".local", "bin")
}

// installedPluginPaths declares the plugin packages a purge removes, so that no
// plugin code is quietly kept on a machine La Roca was removed from.
//
// A directory is claimed only through the manifest the installer generated in
// it: the payload files that manifest records, its database journals, and the
// `roca-<name>` executable while that file is still the verified one. A
// directory with no manifest was not installed by this product and is left
// alone with everything in it, like any other path the operator put there.
func installedPluginPaths(paths config.Paths) []string {
	root := pluginRoot(paths)
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	bin := pluginExecutableDir(paths)
	var owned []string
	for _, entry := range entries {
		directory := filepath.Join(root, entry.Name())
		manifest, err := plugininstall.ReadManifest(directory)
		if err != nil {
			continue
		}
		owned = append(owned, plugininstall.InstalledPaths(directory, manifest)...)
		executable := plugininstall.InstalledExecutable(manifest)
		if executable != "" && filepath.Dir(executable) == filepath.Clean(bin) {
			owned = append(owned, executable)
		}
	}
	if lock := rocavector.RelocationLockPath(root); lock != "" {
		if info, err := os.Lstat(lock); err == nil && info.Mode().IsRegular() {
			owned = append(owned, lock)
		}
	}
	owned = append(owned, plugin.VectorRegistryPath(root))
	return append(owned, pluginDownloads(paths), root)
}

// custodyArchive is one protected removal: the complete plugin directory a
// `roca plugin uninstall` moved aside instead of deleting, with the weight of
// the operator's data inside it.
type custodyArchive struct {
	path    string
	bytes   int64
	entries []string
}

func custodyArchives(paths config.Paths) []custodyArchive {
	root := custodyRoot(paths)
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	archives := make([]custodyArchive, 0, len(entries))
	for _, entry := range entries {
		archive := custodyArchive{path: filepath.Join(root, entry.Name())}
		_ = filepath.WalkDir(archive.path, func(path string, item fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			archive.entries = append(archive.entries, path)
			if info, err := item.Info(); err == nil && info.Mode().IsRegular() {
				archive.bytes += info.Size()
			}
			return nil
		})
		archives = append(archives, archive)
	}
	return archives
}

// consentToCustody splits the archived plugin data in two: what the operator
// just authorized this purge to own, and what stays with its location named.
//
// The split exists because the archives are the product of a refusal. A
// custodial plugin was archived instead of deleted precisely so its data would
// outlive the uninstall of the plugin, and a flag the operator passed for La
// Roca's own artefacts is not the answer to that question.
func (env *cliEnv) consentToCustody(in io.Reader, paths config.Paths) ([]string, []lifecycle.Kept) {
	archives := custodyArchives(paths)
	if len(archives) == 0 {
		return nil, nil
	}
	if env.askAboutTheCustodyArchives(in, archives) {
		owned := []string{custodyRoot(paths)}
		for _, archive := range archives {
			owned = append(owned, archive.entries...)
		}
		return owned, nil
	}
	kept := make([]lifecycle.Kept, 0, len(archives))
	for _, archive := range archives {
		kept = append(kept, lifecycle.Kept{Path: archive.path, Reason: fmt.Sprintf(
			"archived plugin data (%d bytes) you did not consent to delete: it stays here",
			archive.bytes)})
	}
	return nil, kept
}

// askAboutTheCustodyArchives names every archive with its size and waits. `y`
// deletes; Enter keeps, and so does a run with nobody at the terminal, because
// this data may never leave without somebody saying it in this run.
func (env *cliEnv) askAboutTheCustodyArchives(in io.Reader, archives []custodyArchive) bool {
	if env.json {
		return false
	}
	fmt.Fprintln(env.errOut, "Archived plugin data a protected removal kept for you:")
	for _, archive := range archives {
		fmt.Fprintf(env.errOut, "  %s (%d bytes)\n", archive.path, archive.bytes)
	}
	fmt.Fprint(env.errOut, "Delete this archived plugin data too? [y/N]: ")
	line, err := bufio.NewReader(in).ReadString('\n')
	answer := strings.ToLower(strings.TrimSpace(line))
	if err != nil && answer == "" {
		fmt.Fprintln(env.errOut, "no answer: your archived plugin data stays where it is")
		return false
	}
	return answer == "y" || answer == "yes"
}

// exceptCustodyTree drops the data-directory walk's generic line for anything
// inside an archive this report already named. One accurate sentence about the
// archive is what the operator needs; one "La Roca did not create it" per file
// inside it is both noise and false.
func exceptCustodyTree(kept []lifecycle.Kept, root string) []lifecycle.Kept {
	if root == "" {
		return kept
	}
	out := make([]lifecycle.Kept, 0, len(kept))
	for _, survivor := range kept {
		if survivor.Path != root &&
			!strings.HasPrefix(survivor.Path, root+string(os.PathSeparator)) {
			out = append(out, survivor)
		}
	}
	return out
}

func legacyProviderCredentialPaths(dataDir string) map[string]string {
	directory := filepath.Join(dataDir, legacyCredentialsDir)
	paths := make(map[string]string, len(legacyProviderCredentialFiles))
	for name, file := range legacyProviderCredentialFiles {
		paths[name] = filepath.Join(directory, file)
	}
	return paths
}

func ownedFiles(dir string, owns func(string) bool) ([]string, bool) {
	if !realDirectory(dir) {
		return nil, false
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, true
	}
	var paths []string
	for _, entry := range entries {
		if !entry.IsDir() && owns(entry.Name()) {
			paths = append(paths, filepath.Join(dir, entry.Name()))
		}
	}
	return paths, true
}

func realDirectory(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.IsDir()
}

func ownedLogName(name string) bool {
	for _, stream := range []string{
		logfile.Executions, logfile.MCPAudit, logfile.Ingest, logfile.Migrations,
	} {
		prefix := stream + "-"
		if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		stamp := strings.TrimSuffix(strings.TrimPrefix(name, prefix), ".jsonl")
		if len(stamp) < len(time.DateOnly) {
			continue
		}
		if _, err := time.Parse(time.DateOnly, stamp[:len(time.DateOnly)]); err != nil {
			continue
		}
		segment := strings.TrimPrefix(stamp[len(time.DateOnly):], "-")
		if segment == "" && len(stamp) == len(time.DateOnly) {
			return true
		}
		if _, err := strconv.ParseUint(segment, 10, 64); err == nil {
			return true
		}
	}
	return false
}

// scrubDBPaths takes the database's location out of error PROSE. withoutDBPaths
// filters entries that are the path exactly, which is what the deleted list
// holds; an error is a sentence with the path inside it, so filtering left it
// published. The suffixes are replaced before the bare path, or `roca.db-wal`
// would come out as `the database-wal`.
func scrubDBPaths(messages []string, dbPath string) []string {
	if dbPath == "" {
		return messages
	}
	out := make([]string, 0, len(messages))
	for _, message := range messages {
		for _, suffix := range []string{"-wal", "-shm", "-journal", ""} {
			message = strings.ReplaceAll(message, dbPath+suffix, "the database"+suffix)
		}
		out = append(out, message)
	}
	return out
}

// withoutDBPaths filters out paths that reveal the database file from a string
// slice. It matches the database path and its WAL/SHM/journal siblings exactly,
// so that the JSON surface never exposes where the database lives.
func withoutDBPaths(paths []string, dbPath string) []string {
	if dbPath == "" {
		return paths
	}
	out := make([]string, 0, len(paths))
	vault := map[string]bool{
		dbPath: true, dbPath + "-wal": true,
		dbPath + "-shm": true, dbPath + "-journal": true,
	}
	for _, p := range paths {
		if !vault[p] {
			out = append(out, p)
		}
	}
	return out
}

// withoutDBKept filters a Kept list the same way withoutDBPaths filters strings.
func withoutDBKept(kept []lifecycle.Kept, dbPath string) []lifecycle.Kept {
	if dbPath == "" {
		return kept
	}
	out := make([]lifecycle.Kept, 0, len(kept))
	vault := map[string]bool{
		dbPath: true, dbPath + "-wal": true,
		dbPath + "-shm": true, dbPath + "-journal": true,
	}
	for _, k := range kept {
		if !vault[k.Path] {
			out = append(out, k)
		}
	}
	return out
}

func renderUninstall(env *cliEnv, purge bool, report lifecycle.Report,
	runtimes []agentcfg.Outcome) {

	for _, outcome := range runtimes {
		env.print("withdrawn from %s: %s", outcome.Runtime, outcome.Path)
	}
	for _, path := range report.Deleted {
		env.print("deleted: %s", path)
	}
	if purge {
		env.print("kept paths: %d", len(report.Kept))
	}
	for _, kept := range report.Kept {
		env.print("kept: %s (%s)", kept.Path, kept.Reason)
	}
	for _, failure := range report.Errors {
		env.print("error: %s", failure)
	}
	// The verdict is the OUTCOME and never the request. --json reports
	// `purge && report.Purged`, and a readable line that branched on `purge`
	// alone printed "purged: yes" directly under the errors that say it did not.
	switch {
	case purge && report.Purged:
		env.print("purged: yes")
	case purge:
		env.print("purged: no (what is left is named above: run the uninstall again)")
	default:
		env.print("purged: no (your data is still where it was)")
	}
}
