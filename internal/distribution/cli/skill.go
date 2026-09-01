package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	goruntime "runtime"
	"strings"

	"github.com/spf13/cobra"
	"github.com/thellmwhisperer/la-roca/internal/artifact"
	"github.com/thellmwhisperer/la-roca/internal/distribution/agentcfg"
	"github.com/thellmwhisperer/la-roca/internal/distribution/skill"
	"github.com/thellmwhisperer/la-roca/internal/provider/plugin"
	"github.com/thellmwhisperer/la-roca/internal/provider/service"
	"github.com/thellmwhisperer/la-roca/internal/securefile"
)

var rocaStoreInvocation = regexp.MustCompile(
	`(?:^|(?:&&|\|\||;|\n|\|)[ \t]*)(?:[^ \t;&|\n]*/)?roca[ \t]+store\b`,
)

// claudeHookInvocation recognizes La Roca's own PreToolUse entry whatever binary
// path it was installed with, so a reinstall repoints it and an uninstall finds
// it even after the operator moved the executable.
const shellCommandExecutablePattern = `(?:'(?:[^']|'"'"')*'|"[^"]*"|\S+)`

var claudeHookInvocation = regexp.MustCompile(
	`^` + shellCommandExecutablePattern + `[ \t]+hooks[ \t]+run[ \t]+claude$`,
)

func claudeHookCommand(executable string) string {
	return shellQuote(executable) + " hooks run claude"
}

// skillCommand installs the agent skills that teach runtimes how to use La
// Roca. Hidden plumbing: bare lists destinations; install writes the three
// embedded skills and the generated semantic catalog per runtime and narrates
// every path.
func skillCommand(env *cliEnv) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "skill",
		Short: "Install the agent skills that teach runtimes how to use La Roca",
		Long: "Three embedded skills (roca, roca-operations, roca-vector) plus the\n" +
			"generated semantic catalog of the installed plugins, each installed into\n" +
			"a runtime's personal skills directory with separate SYSTEM and USER zones\n" +
			"and a versioned registry.\n\n" +
			"Supported runtimes: " + strings.Join(skill.Runtimes(), ", "),
		Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			return env.listSkillDestinations()
		},
	}
	cmd.AddCommand(skillInstallCommand(env))
	return cmd
}

func skillInstallCommand(env *cliEnv) *cobra.Command {
	var all, force bool
	cmd := &cobra.Command{
		Use:   "install [runtime]",
		Short: "Write the roca skills into one runtime, or every supported one",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if all == (len(args) == 1) {
				return fmt.Errorf("name one runtime (%s) or ask for --all",
					strings.Join(skill.Runtimes(), ", "))
			}
			runtimes := args
			if all {
				runtimes = skill.Runtimes()
			}
			catalog, err := env.composedCatalogSkill()
			if err != nil {
				return err
			}
			outcomes := make([]skill.Outcome, 0, (len(skill.EmbeddedSkills())+1)*len(runtimes))
			var refused []error
			for _, runtime := range runtimes {
				written, failures := env.installRuntimeSkills(runtime, catalog, force, true, true)
				outcomes = append(outcomes, written...)
				refused = append(refused, failures...)
			}
			if env.json {
				if err := env.printJSON(map[string]any{"runtimes": outcomes}); err != nil {
					return err
				}
				return errors.Join(refused...)
			}
			for _, o := range outcomes {
				verb := "unchanged"
				if o.Changed {
					verb = "wrote"
				}
				line := fmt.Sprintf("%s: %s %s", o.Runtime, verb, o.Path)
				if o.Backup != "" {
					line += fmt.Sprintf(" (replaced content kept at %s)", o.Backup)
				}
				env.print("%s", line)
			}
			return errors.Join(refused...)
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "install into every supported runtime")
	cmd.Flags().BoolVar(&force, "force", false,
		"replace an edited SYSTEM zone, or rewrite a skill whose zone markers are broken, keeping a recovery copy")
	return cmd
}

// divergedArtifactWarning names what actually happened to a registered artifact
// this run refused to write, and is the one place either command says it. An
// artifact that was deleted has no edited SYSTEM zone, and one no registry entry
// stands behind was never proven to be ours in the first place; saying either is
// an edit sends the operator looking for edits that are not there.
func divergedArtifactWarning(path, forceCommand string, missing, unregistered bool) string {
	what := "has edits in its SYSTEM zone"
	switch {
	case missing:
		what = "was removed after La Roca registered it"
	case unregistered:
		what = "has no record in La Roca's artifact registry, so its SYSTEM zone cannot be proven to be ours"
	}
	return fmt.Sprintf("%s %s; run `%s` to replace it", path, what, forceCommand)
}

func forceSkillInstall(runtime string) string {
	return "roca skill install " + runtime + " --force"
}

// skillInstallFailure says what went wrong and offers the force remedy only for
// the one class it repairs. A permission error is not fixed by forcing, and
// re-running a write the runtime interrupted is the clobber that refusal
// exists to prevent, so neither is answered with a command to run again.
func skillInstallFailure(err error, runtime, backup string) error {
	if backup != "" {
		err = fmt.Errorf("%w (previous content kept at %s)", err, backup)
	}
	if !errors.Is(err, artifact.ErrBrokenZones) {
		return err
	}
	return fmt.Errorf("%w; run `%s` to replace it, keeping the file in its recovery copy",
		err, forceSkillInstall(runtime))
}

func (env *cliEnv) listSkillDestinations() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("I do not know where your HOME is")
	}
	type row struct {
		Runtime string `json:"runtime"`
		Skill   string `json:"skill"`
		Path    string `json:"path"`
	}
	rows := make([]row, 0, len(listedSkills())*len(skill.Runtimes()))
	for _, runtime := range skill.Runtimes() {
		for _, destination := range listedSkills() {
			path, err := destination.path(runtime, home, os.Getenv)
			if err != nil {
				return err
			}
			rows = append(rows, row{Runtime: runtime, Skill: destination.name, Path: path})
		}
	}
	if env.json {
		return env.printJSON(map[string]any{"runtimes": rows})
	}
	toonRows := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		toonRows = append(toonRows, map[string]any{"runtime": r.Runtime, "skill": r.Skill, "path": r.Path})
	}
	env.print("%s", rowOutput([]string{"runtime", "skill", "path"}, toonRows))
	env.print("%s", renderHelp(
		"Run `roca skill install <runtime>` to install one destination",
		"Run `roca skill install --all` to install every destination"))
	return nil
}

type listedSkill struct {
	name string
	path func(string, string, func(string) string) (string, error)
}

func listedSkills() []listedSkill {
	return []listedSkill{
		{skill.SkillName, skill.Path},
		{skill.OperationsName, skill.OperationsPath},
		{skill.VectorName, skill.VectorPath},
		{skill.CatalogName, skill.CatalogPath},
	}
}

// installRuntimeSkills writes the embedded skills, and optionally the catalog,
// into one runtime. restoreMissing is the consent an explicit install and init
// carry; ingest reseed leaves a deleted registered file alone. skipRegistered
// is the reseed gate: a runtime that already has the skill is left for
// artifact_refresh, not rewritten here.
func (env *cliEnv) installRuntimeSkills(runtime, catalog string,
	force, restoreMissing, includeCatalog bool) ([]skill.Outcome, []error) {
	return env.installRuntimeSkillsFiltered(runtime, catalog, force, restoreMissing, includeCatalog, false)
}

func (env *cliEnv) reseedRuntimeSkills(runtime string) ([]skill.Outcome, []error) {
	return env.installRuntimeSkillsFiltered(runtime, "", false, false, false, true)
}

func (env *cliEnv) installRuntimeSkillsFiltered(runtime, catalog string,
	force, restoreMissing, includeCatalog, skipRegistered bool) ([]skill.Outcome, []error) {
	home, err := env.skillHome()
	if err != nil {
		return nil, []error{err}
	}
	type file struct {
		kind, path, system string
		run                func(path, previous string) (skill.Outcome, error)
	}
	var files []file
	for _, embedded := range skill.EmbeddedSkills() {
		path, err := skill.NamedPath(runtime, embedded.Name, home, os.Getenv)
		if err != nil {
			return nil, []error{err}
		}
		embedded := embedded
		files = append(files, file{
			kind: artifactKindSkill, path: path, system: embedded.Body,
			run: func(path, previous string) (skill.Outcome, error) {
				return skill.InstallNamed(runtime, path, embedded.Body, embedded.Legacy,
					previous, force, restoreMissing)
			},
		})
	}
	if includeCatalog {
		path, err := skill.CatalogPath(runtime, home, os.Getenv)
		if err != nil {
			return nil, []error{err}
		}
		files = append(files, file{
			kind: artifactKindSkillCatalog, path: path, system: catalog,
			run: func(path, previous string) (skill.Outcome, error) {
				return skill.InstallCatalogWithOptions(runtime, path, catalog, previous, force, restoreMissing)
			},
		})
	}
	outcomes := make([]skill.Outcome, 0, len(files))
	var refused []error
	for _, file := range files {
		entry, found, err := env.registeredArtifact(file.kind, runtime, file.path)
		if err != nil {
			return outcomes, append(refused, err)
		}
		if skipRegistered && found {
			continue
		}
		previous := ""
		if found {
			previous = entry.SystemSHA256
		}
		outcome, err := file.run(file.path, previous)
		if err != nil {
			refused = append(refused, skillInstallFailure(err, runtime, outcome.Backup))
			continue
		}
		if outcome.Diverged {
			env.warnf("warning: %s\n",
				divergedArtifactWarning(file.path, forceSkillInstall(runtime),
					outcome.Missing, outcome.Unregistered))
			outcomes = append(outcomes, outcome)
			continue
		}
		if err := env.registerZonedArtifact(file.kind, runtime, file.path, file.system); err != nil {
			return outcomes, append(refused, err)
		}
		outcomes = append(outcomes, outcome)
	}
	return outcomes, refused
}

// seedDetectedSkills writes the embedded skills into every skill seat whose
// config directory exists. restoreMissing is init's consent to write again;
// ingest reseed skips a runtime that already has a registry entry so a later
// agent still receives the skills without rewriting ones already placed.
//
// Init also writes the generated catalog. It is the map of what is searchable on
// this machine, and an agent that has the craft skills but not the catalog
// composes SQL by guessing. Nothing should stand between the first ingest and a
// good first question.
func (env *cliEnv) seedDetectedSkills(restoreMissing bool) []string {
	home, err := env.skillHome()
	if err != nil {
		env.warnf("warning: skills were not installed: %v\n", err)
		return nil
	}
	catalog := ""
	if restoreMissing {
		composed, composeErr := env.composedCatalogSkill()
		if composeErr != nil {
			env.warnCatalogRefresh(composeErr)
		} else {
			catalog = composed
		}
	}
	detected := skill.Detected(home, os.Getenv)
	for _, runtime := range detected {
		var refused []error
		if restoreMissing {
			_, refused = env.installRuntimeSkills(runtime, catalog, false, true, catalog != "")
		} else {
			_, refused = env.reseedRuntimeSkills(runtime)
		}
		for _, seedErr := range refused {
			env.warnf("warning: skills were not installed for %s: %v\n", runtime, seedErr)
		}
	}
	return detected
}

func (env *cliEnv) skillHome() (string, error) {
	if paths, err := env.resolvePaths(); err == nil && paths.Home != "" {
		return paths.Home, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("I do not know where your HOME is")
	}
	return home, nil
}

func (env *cliEnv) warnf(format string, args ...any) {
	if env.errOut == nil {
		return
	}
	fmt.Fprintf(env.errOut, format, args...)
}

// composedCatalogSkill builds the generated semantic-catalog skill body from
// the installed plugin manifests: every database discovery finds is validated
// the same way the query route validates it, so the catalog only names tables
// a query can actually reach, and what could not serve one is said in the body
// instead of silently omitted.
func (env *cliEnv) composedCatalogSkill() (string, error) {
	_, databases, warnings, err := env.discoverPluginContracts()
	if err != nil {
		return "", err
	}
	return skill.CatalogBody(databases, warnings), nil
}

func (env *cliEnv) discoverPluginContracts() (string, []plugin.Database, []string, error) {
	paths, err := env.resolvePaths()
	if err != nil {
		return "", nil, nil, err
	}
	root := pluginRoot(paths)
	descriptors, warnings := plugin.Discover(root)
	databases := make([]plugin.Database, 0, len(descriptors))
	for _, descriptor := range descriptors {
		database, err := plugin.Validate(context.Background(), descriptor)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf(
				"plugin %s is unavailable: %v", descriptor.Name, err))
			continue
		}
		databases = append(databases, database)
	}
	return root, databases, warnings, nil
}

// refreshPluginContracts regenerates both declarative federation projections
// after install, update, or uninstall. The vector registry is always refreshed;
// the semantic catalog is written only to runtimes that previously asked for
// skills. A failure is a warning because the package action already succeeded.
func (env *cliEnv) refreshPluginContracts() {
	root, databases, warnings, err := env.discoverPluginContracts()
	if err != nil {
		env.warnCatalogRefresh(err)
		env.warnVectorRegistryRefresh(err)
		return
	}
	if err := saveVectorRegistry(root, databases); err != nil {
		env.warnVectorRegistryRefresh(err)
	}
	catalog := skill.CatalogBody(databases, warnings)
	home, err := os.UserHomeDir()
	if err != nil {
		env.warnCatalogRefresh(fmt.Errorf("I do not know where your HOME is"))
		return
	}
	for _, runtime := range skill.Runtimes() {
		if !skill.AutomaticallyManaged(runtime) {
			continue
		}
		path, err := skill.CatalogPath(runtime, home, os.Getenv)
		if err != nil {
			continue
		}
		entry, found, err := env.registeredArtifact(artifactKindSkillCatalog, runtime, path)
		if err != nil {
			env.warnCatalogRefresh(err)
			continue
		}
		if !found {
			continue
		}
		// RestoreMissing stays false: the plugin lifecycle is an automatic
		// refresh, and a file the operator deleted is a withdrawal it honors.
		outcome, err := skill.InstallCatalogWithOptions(
			runtime, path, catalog, entry.SystemSHA256, false, false)
		if err != nil {
			env.warnCatalogRefresh(err)
			continue
		}
		if outcome.Diverged {
			fmt.Fprintf(env.errOut, "warning: %s\n",
				divergedArtifactWarning(path, forceSkillInstall(runtime),
					outcome.Missing, outcome.Unregistered))
			continue
		}
		if err := env.registerZonedArtifact(artifactKindSkillCatalog, runtime, path, catalog); err != nil {
			env.warnCatalogRefresh(err)
		}
	}
}

func (env *cliEnv) refreshVectorRegistry() error {
	root, databases, _, err := env.discoverPluginContracts()
	if err != nil {
		return err
	}
	return saveVectorRegistry(root, databases)
}

func saveVectorRegistry(root string, databases []plugin.Database) error {
	return plugin.SaveVectorRegistry(plugin.VectorRegistryPath(root),
		plugin.ComposeVectorRegistry(databases))
}

func (env *cliEnv) warnCatalogRefresh(err error) {
	fmt.Fprintf(env.errOut,
		"warning: the semantic catalog skill was not refreshed: %v\n", err)
}

func (env *cliEnv) warnVectorRegistryRefresh(err error) {
	fmt.Fprintf(env.errOut,
		"warning: the vector declaration registry was not refreshed: %v\n", err)
}

func hooksCommand(env *cliEnv) *cobra.Command {
	command := &cobra.Command{
		Use:   "hooks",
		Short: "Install and withdraw client-side authorship signing hooks",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	command.AddCommand(
		hooksInstallCommand(env), hooksUninstallCommand(env), hooksRunCommand(env),
	)
	return command
}

// hooksEditCommand is the shape both `hooks install` and `hooks uninstall`
// have: one supported runtime, one settings file, one rendered outcome. An edit
// that has something to say about a file it left alone returns one warning line,
// printed here and only here so it cannot be doubled.
func hooksEditCommand(env *cliEnv, use, short, verb string,
	edit func(runtime, path string) (agentcfg.Outcome, string, error)) *cobra.Command {
	return &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if err := supportedHookRuntime(args[0]); err != nil {
				return err
			}
			path, err := hookConfigPath(args[0])
			if err != nil {
				return err
			}
			outcome, warning, err := edit(args[0], path)
			if err != nil {
				return err
			}
			if warning != "" && env.errOut != nil {
				fmt.Fprintln(env.errOut, warning)
			}
			return env.renderOutcome(outcome, verb)
		},
	}
}

func hooksInstallCommand(env *cliEnv) *cobra.Command {
	var executable string
	var force, pills, handoff bool
	cmd := hooksEditCommand(env, "install [runtime]",
		"Install a runtime's La Roca hook", "updated",
		func(runtime, path string) (agentcfg.Outcome, string, error) {
			declared := chosenExecutable(executable)
			if runtime == agentcfg.RuntimeZcode {
				if err := zcodeHookPlatformError(goruntime.GOOS, os.Stat); err != nil {
					return agentcfg.Outcome{Runtime: runtime, Path: path}, "", err
				}
				wrapper, err := zcodeHookWrapperPath()
				if err != nil {
					return agentcfg.Outcome{Runtime: runtime, Path: path}, "", err
				}
				rollback, err := env.recordZcodeWrapperState(wrapper, declared)
				if err != nil {
					return agentcfg.Outcome{Runtime: runtime, Path: path}, "", err
				}
				outcome, warning, err := installZcodeHandoffHook(path, wrapper, declared)
				if err != nil && rollback != nil {
					err = errors.Join(err, rollback())
				}
				return outcome, warning, err
			}
			return installClaudeAuthorshipAndSessionHooks(
				env, path, declared, force, pills, handoff)
		})
	cmd.Flags().StringVar(&executable, "executable", "",
		"the binary the hook launches (default: this executable; override with "+EnvExecutable+")")
	cmd.Flags().BoolVar(&force, "force", false, "replace an edited SYSTEM fragment")
	cmd.Flags().BoolVar(&pills, "pills", false, "add a SessionStart hook that runs `roca pill`")
	cmd.Flags().BoolVar(&handoff, "handoff", false, "add a SessionStart hook that runs `roca handoff latest`")
	return cmd
}

func hooksUninstallCommand(env *cliEnv) *cobra.Command {
	var pills, handoff bool
	cmd := hooksEditCommand(env, "uninstall [runtime]",
		"Withdraw a runtime's La Roca hook, leaving its other settings in place",
		"withdrawn", func(runtime, path string) (agentcfg.Outcome, string, error) {
			if runtime == agentcfg.RuntimeZcode {
				wrapper, err := zcodeHookWrapperPath()
				if err != nil {
					return agentcfg.Outcome{Runtime: runtime, Path: path}, "", err
				}
				expected, entry, found, err := env.zcodeWrapperExpected(wrapper)
				if err != nil {
					return agentcfg.Outcome{Runtime: runtime, Path: path}, "", err
				}
				outcome, warning, err := uninstallZcodeHandoffHook(path, wrapper, expected)
				if err == nil && found {
					err = env.unregisterArtifactEntry(entry)
				}
				return outcome, warning, err
			}
			return uninstallClaudeAuthorshipAndSessionHooks(env, path, pills, handoff)
		})
	cmd.Flags().BoolVar(&pills, "pills", false, "withdraw the SessionStart `roca pill` hook")
	cmd.Flags().BoolVar(&handoff, "handoff", false, "withdraw the SessionStart `roca handoff latest` hook")
	return cmd
}

func zcodeHookPlatformError(goos string, stat func(string) (os.FileInfo, error)) error {
	if goos != "darwin" && goos != "linux" {
		return fmt.Errorf("ZCode hooks are unsupported on %s because the installed wrapper requires /bin/bash", goos)
	}
	info, err := stat("/bin/bash")
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("ZCode hooks require executable /bin/bash")
	}
	return nil
}

func supportedHookRuntime(name string) error {
	if name != agentcfg.RuntimeClaude && name != agentcfg.RuntimeZcode {
		return fmt.Errorf("unsupported hook runtime %q (want claude, zcode)", name)
	}
	return nil
}

func hooksRunCommand(env *cliEnv) *cobra.Command {
	command := &cobra.Command{
		Use:    "run [hook]",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			switch args[0] {
			case agentcfg.RuntimeZcode:
				return env.printJSON(map[string]string{
					"additionalContext": zcodeHandoffContext(cmd.Context(), env),
				})
			case agentcfg.RuntimeClaude:
				input, err := io.ReadAll(cmd.InOrStdin())
				if err != nil {
					return fmt.Errorf("read Claude hook input: %w", err)
				}
				output, err := runClaudeAuthorshipHook(input)
				if err != nil {
					return err
				}
				if len(output) > 0 {
					fmt.Fprintln(env.out, string(output))
				}
				return nil
			case "claude-pills":
				return runPillList(cmd.Context(), env, "")
			case "claude-handoff":
				return runLatestHandoffs(cmd.Context(), env, "")
			default:
				return fmt.Errorf("unsupported hook %q", args[0])
			}
		},
	}
	return command
}

func claudeSettingsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("I do not know where your HOME is")
	}
	root := os.Getenv("CLAUDE_CONFIG_DIR")
	if root == "" {
		root = filepath.Join(home, ".claude")
	}
	return filepath.Join(root, "settings.json"), nil
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

const (
	zcodeSessionStartMarker = "roca_session_start_marker"
	zcodeWrapperStateFormat = "zcode-wrapper-v1"
)

func (env *cliEnv) recordZcodeWrapperState(path, executable string) (func() error, error) {
	paths, err := env.resolvePaths()
	if err != nil {
		return nil, err
	}
	expected := zcodeWrapper(executable)
	transaction := artifact.Entry{
		Kind: artifactKindHook, Runtime: agentcfg.RuntimeZcode, Path: path,
		InstalledVersion: env.build.Version, AvailableVersion: env.build.Version,
		SystemSHA256: artifact.Checksum(expected), Format: zcodeWrapperStateFormat,
		Executable: executable,
	}
	var prior artifact.Entry
	var priorFound bool
	_, err = mutateArtifactRegistry(paths.Artifacts, func(registry *artifact.Registry) (bool, error) {
		prior, priorFound = registry.Find(artifactKindHook, agentcfg.RuntimeZcode, path)
		registry.Upsert(transaction)
		return true, nil
	})
	if err != nil {
		return nil, err
	}
	return func() error {
		_, err := mutateArtifactRegistry(paths.Artifacts, func(registry *artifact.Registry) (bool, error) {
			current, found := registry.Find(artifactKindHook, agentcfg.RuntimeZcode, path)
			if !found || current != transaction {
				return false, nil
			}
			removeArtifactEntry(registry, transaction.Key())
			if priorFound {
				registry.Upsert(prior)
			}
			return true, nil
		})
		return err
	}, nil
}

func (env *cliEnv) zcodeWrapperExpected(path string) ([]byte, artifact.Entry, bool, error) {
	entry, found, err := env.registeredArtifact(artifactKindHook, agentcfg.RuntimeZcode, path)
	if err != nil || !found {
		return nil, entry, found, err
	}
	expected, err := zcodeWrapperExpectedFromEntry(entry)
	return expected, entry, true, err
}

func zcodeWrapperExpectedFromEntry(entry artifact.Entry) ([]byte, error) {
	if entry.Format != zcodeWrapperStateFormat || !filepath.IsAbs(entry.Executable) {
		return nil, fmt.Errorf("ZCode wrapper ownership state for %s is invalid", entry.Path)
	}
	expected := []byte(zcodeWrapper(entry.Executable))
	if artifact.Checksum(string(expected)) != entry.SystemSHA256 {
		return nil, fmt.Errorf("ZCode wrapper ownership checksum for %s is invalid", entry.Path)
	}
	return expected, nil
}

func installZcodeHandoffHook(configPath, wrapperPath, executable string) (outcome agentcfg.Outcome, warning string, err error) {
	release, err := lockZcodeHookLifecycle(configPath, wrapperPath, true)
	if err != nil {
		return agentcfg.Outcome{Runtime: agentcfg.RuntimeZcode, Path: configPath}, "", err
	}
	defer func() { err = errors.Join(err, release()) }()
	return installZcodeHandoffHookUnlocked(configPath, wrapperPath, executable)
}

func installZcodeHandoffHookUnlocked(configPath, wrapperPath, executable string) (agentcfg.Outcome, string, error) {
	state, err := readZcodeWrapperState(wrapperPath)
	if err != nil {
		return agentcfg.Outcome{Runtime: agentcfg.RuntimeZcode, Path: configPath}, "", err
	}
	wrapper := zcodeWrapper(executable)
	if state.exists && string(state.body) != wrapper {
		return agentcfg.Outcome{Runtime: agentcfg.RuntimeZcode, Path: configPath}, "",
			fmt.Errorf("refuse to replace operator-modified ZCode hook wrapper %s; move or remove it, then retry", wrapperPath)
	}
	if err := writeZcodeWrapper(wrapperPath, wrapper, state); err != nil {
		return agentcfg.Outcome{Runtime: agentcfg.RuntimeZcode, Path: configPath}, "", err
	}
	command := zcodeOwnedHookCommand(wrapperPath)
	outcome, err := agentcfg.Edit(agentcfg.RuntimeZcode, configPath, func(previous string) (string, error) {
		return agentcfg.DeclareZcodeSessionStartHook(previous, zcodeSessionStartMarker, command, 15000)
	}, true)
	if err != nil {
		if restoreErr := restoreZcodeWrapper(wrapperPath, state, []byte(wrapper)); restoreErr != nil {
			err = errors.Join(err, restoreErr)
		}
		return outcome, "", err
	}
	body, readErr := os.ReadFile(configPath)
	if readErr == nil {
		declared, enabled, enabledErr := agentcfg.ZcodeHooksEnabled(string(body))
		if enabledErr == nil && (!declared || !enabled) {
			return outcome, fmt.Sprintf("warning: ZCode hook installed but inactive because hooks.enabled is false or absent in %s; set it to true to enable SessionStart", configPath), nil
		}
	}
	return outcome, "", nil
}

func uninstallZcodeHandoffHook(configPath, wrapperPath string, expected ...[]byte) (outcome agentcfg.Outcome, warning string, err error) {
	release, err := lockZcodeHookLifecycle(configPath, wrapperPath, false)
	if err != nil {
		return agentcfg.Outcome{Runtime: agentcfg.RuntimeZcode, Path: configPath}, "", err
	}
	defer func() { err = errors.Join(err, release()) }()
	var expectedBytes []byte
	if len(expected) > 0 {
		expectedBytes = expected[0]
	}
	return uninstallZcodeHandoffHookUnlocked(configPath, wrapperPath, expectedBytes)
}

func uninstallZcodeHandoffHookUnlocked(configPath, wrapperPath string, expected []byte) (agentcfg.Outcome, string, error) {
	var warning string
	keepWrapper := false
	command := zcodeOwnedHookCommand(wrapperPath)
	outcome, err := agentcfg.Edit(agentcfg.RuntimeZcode, configPath, func(previous string) (string, error) {
		next, editErr := agentcfg.RemoveZcodeSessionStartHook(previous, zcodeSessionStartMarker)
		if editErr != nil {
			keepWrapper = true
			warning = fmt.Sprintf("warning: %s is not readable as zcode settings; remove the nested hooks.events.SessionStart entry carrying La Roca marker %q and command %s by hand", configPath, zcodeSessionStartMarker, command)
			return previous, nil
		}
		commands, commandsErr := agentcfg.ZcodeHookCommands(next)
		if commandsErr != nil {
			keepWrapper = true
			warning = fmt.Sprintf("warning: could not verify remaining ZCode hooks in %s: %v", configPath, commandsErr)
			return next, nil
		}
		referenced, referenceErr := zcodeHookCommandsReferenceWrapper(commands, wrapperPath)
		if referenceErr != nil {
			keepWrapper = true
			warning = fmt.Sprintf("warning: could not compare remaining ZCode hook paths in %s: %v", configPath, referenceErr)
			return next, nil
		}
		keepWrapper = referenced
		return next, nil
	}, false)
	if err != nil {
		return outcome, warning, err
	}
	if keepWrapper {
		retained := fmt.Sprintf("kept %s because a remaining operator-owned hook references it", wrapperPath)
		if warning == "" {
			warning = "warning: " + retained
		} else {
			warning += "; " + retained
		}
		return outcome, warning, nil
	}
	retained, removeErr := removeZcodeWrapper(wrapperPath, expected)
	if removeErr != nil {
		return outcome, warning, removeErr
	}
	if retained {
		reason := fmt.Sprintf("kept operator-modified ZCode hook wrapper %s", wrapperPath)
		if warning == "" {
			warning = "warning: " + reason
		} else {
			warning += "; " + reason
		}
	}
	return outcome, warning, nil
}

func lockZcodeHookLifecycle(configPath, wrapperPath string, create bool) (func() error, error) {
	root := filepath.Dir(filepath.Dir(wrapperPath))
	lockPath := filepath.Join(root, ".roca-hooks.lock")
	if !create {
		_, configErr := os.Stat(configPath)
		_, wrapperErr := os.Stat(wrapperPath)
		_, lockErr := os.Stat(lockPath)
		if os.IsNotExist(configErr) && os.IsNotExist(wrapperErr) && os.IsNotExist(lockErr) {
			return func() error { return nil }, nil
		}
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	return securefile.Lock(lockPath)
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
		return zcodeWrapperState{}, fmt.Errorf("stat %s: %w", path, err)
	}
	return zcodeWrapperState{body: body, mode: info.Mode(), exists: true}, nil
}

func restoreZcodeWrapper(path string, state zcodeWrapperState, installed []byte) error {
	if !state.exists {
		current, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if string(current) != string(installed) {
			return fmt.Errorf("roll back %s: wrapper changed after installation", path)
		}
		return os.Remove(path)
	}
	if err := securefile.Replace(path, state.body, installed); err != nil {
		return fmt.Errorf("roll back %s: %w", path, err)
	}
	return os.Chmod(path, state.mode)
}

func zcodeHandoffContext(ctx context.Context, env *cliEnv) string {
	svc, _, err := env.openService()
	if err != nil {
		return ""
	}
	defer svc.Close()
	result, err := svc.Exec(ctx, service.ExecRequest{
		SQL:      "SELECT content FROM plugin_roca_ops.memories WHERE layer='handoff' AND status='active' ORDER BY created_at DESC, id DESC LIMIT 1",
		MaxChars: 8000,
	})
	if err != nil || len(result.Rows) == 0 {
		return ""
	}
	content, _ := result.Rows[0]["content"].(string)
	return content
}

func zcodeWrapper(executable string) string {
	return `#!/bin/bash
# Managed by roca hooks install zcode.
set -euo pipefail

if ! ` + shellQuote(executable) + ` hooks run zcode 2>/dev/null; then
  printf '{"additionalContext":""}\n'
fi
`
}

func writeZcodeWrapper(path, content string, state zcodeWrapperState) error {
	if state.exists {
		if string(state.body) != content {
			return fmt.Errorf("refuse to replace operator-modified ZCode hook wrapper %s", path)
		}
		if state.mode == 0o700 {
			return nil
		}
		return os.Chmod(path, 0o700)
	}
	return securefile.CreatePreservingParentMode(path, []byte(content), 0o700, 0o700)
}

func removeZcodeWrapper(path string, expected []byte) (bool, error) {
	body, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read %s: %w", path, err)
	}
	if len(expected) == 0 || string(body) != string(expected) {
		return true, nil
	}
	current, err := os.ReadFile(path)
	if err != nil {
		return false, fmt.Errorf("verify %s before removal: %w", path, err)
	}
	if string(current) != string(expected) {
		return true, nil
	}
	if err := os.Remove(path); err != nil {
		return false, fmt.Errorf("remove %s: %w", path, err)
	}
	return false, nil
}

func zcodeOwnedHookCommand(path string) string {
	return shellQuote(path) + " # Managed by roca hooks install zcode"
}

// The hook is intentionally one hard-coded Claude artifact. Its command object
// is the registered SYSTEM fragment; every neighbouring setting stays USER.
//
// The entry launches the absolute path of this executable, the way `roca mcp
// install` declares the server: Claude runs a PreToolUse hook in a
// non-interactive shell, where a bare `roca` is whatever PATH happens to hold.
func installClaudeAuthorshipHook(path, executable string) (agentcfg.Outcome, error) {
	return installClaudeHook(path, executable, claudeHookSpec{
		event: "PreToolUse", invocation: claudeHookInvocation,
		command: claudeHookCommand, entry: claudeAuthorshipHookEntry,
	})
}

func claudeAuthorshipHookEntry(command string) map[string]any {
	return map[string]any{
		"matcher": "Bash",
		"hooks":   []any{claudeAuthorshipCommandHook(command)},
	}
}

func claudeAuthorshipCommandHook(command string) map[string]any {
	return map[string]any{"type": "command", "command": command}
}

// uninstallClaudeAuthorshipHook takes the PreToolUse entry back out and leaves
// every other setting, and every hook that is not La Roca's, exactly as it was.
// A settings file that is not there is not created and not an error.
//
// Settings this product cannot read are not a reason to refuse a withdrawal: an
// operator removing La Roca must not be held hostage by a file La Roca never
// wrote. The edit is skipped, the command succeeds, and the returned warning
// names the file and the entry to take out by hand. The caller prints it once.
func uninstallClaudeAuthorshipHook(path string) (agentcfg.Outcome, string, error) {
	return uninstallClaudeHook(path, claudeHookSpec{
		event: "PreToolUse", invocation: claudeHookInvocation,
	}, foreignClaudeSettingsWarning(path))
}

// foreignClaudeSettingsWarning is the single line an operator gets when their
// Claude settings are not the shape La Roca can edit: the file, and the exact
// entry to delete so no hook survives calling a binary that is gone.
func foreignClaudeSettingsWarning(path string) string {
	return fmt.Sprintf("warning: %s is not readable as Claude settings, "+
		"so nothing there was changed; remove the hooks.PreToolUse entry whose "+
		"command ends in `hooks run claude` by hand", path)
}

// claudeHookSettings is the one reader of Claude's settings document: it decodes
// the file and hands back the hooks table and its PreToolUse entries. The two
// edits share this refusal and part company over what it means, so the reader
// states the shape once and each caller decides whether to stop.
func claudeHookSettings(previous string) (settings, hooks map[string]any, entries []any, err error) {
	return claudeEventHookSettings(previous, "PreToolUse")
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

func encodeClaudeSettings(settings map[string]any) (string, error) {
	encoded, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode Claude settings: %w", err)
	}
	return string(append(encoded, '\n')), nil
}

func commandHooksOf(entry any) []map[string]any {
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

func commandOf(hook map[string]any) string {
	command, _ := hook["command"].(string)
	return command
}

func runClaudeAuthorshipHook(input []byte) ([]byte, error) {
	var event struct {
		HookEventName string         `json:"hook_event_name"`
		ToolName      string         `json:"tool_name"`
		Transcript    string         `json:"transcript_path"`
		ToolInput     map[string]any `json:"tool_input"`
	}
	if err := json.Unmarshal(input, &event); err != nil {
		return nil, fmt.Errorf("decode Claude hook input: %w", err)
	}
	command, _ := event.ToolInput["command"].(string)
	if event.HookEventName != "PreToolUse" || event.ToolName != "Bash" ||
		!rocaStoreInvocation.MatchString(command) {
		return nil, nil
	}
	model := claudeTranscriptModel(event.Transcript)
	if model == "" {
		model = service.UnknownAuthor
	}
	signed := signRocaStoreCommand(command, model)
	if signed == command {
		return nil, nil
	}
	event.ToolInput["command"] = signed
	return json.Marshal(map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName": "PreToolUse", "updatedInput": event.ToolInput,
		},
	})
}

func signRocaStoreCommand(command, model string) string {
	location := rocaStoreInvocation.FindStringIndex(command)
	if location == nil {
		return command
	}
	// The segment ends at the first separator the shell would honour, found by a
	// scan that starts at the beginning of the line: a `||` inside a quoted
	// value is text, and cutting the segment there hid the flags behind it.
	end := scanUnquoted(command, func(index int) bool {
		return index >= location[1] && shellSeparatorAt(command[index:])
	})
	if end < 0 {
		end = len(command)
	}
	segment := command[location[0]:end]
	flags := ""
	if !hasUnquotedFlag(segment, "--agent") {
		flags += " --agent claude"
	}
	if !hasUnquotedFlag(segment, "--model") {
		flags += " --model " + shellQuote(model)
	}
	return command[:location[1]] + flags + command[location[1]:]
}

func shellSeparatorAt(rest string) bool {
	for _, separator := range []string{"&&", "||", ";", "\n", "|"} {
		if strings.HasPrefix(rest, separator) {
			return true
		}
	}
	return false
}

func hasUnquotedFlag(command, flag string) bool {
	return scanUnquoted(command, func(index int) bool {
		if index > 0 && command[index-1] != ' ' && command[index-1] != '\t' {
			return false
		}
		if !strings.HasPrefix(command[index:], flag) {
			return false
		}
		after := index + len(flag)
		return after == len(command) || command[after] == '=' ||
			command[after] == ' ' || command[after] == '\t'
	}) >= 0
}

// scanUnquoted walks the command the way a shell reads it and returns the first
// offset outside quoting that `found` accepts, or -1. One scan owns the quoting
// rules for every question asked about a command line.
func scanUnquoted(command string, found func(index int) bool) int {
	var quote byte
	for i := 0; i < len(command); i++ {
		current := command[i]
		if quote != 0 {
			if current == quote {
				quote = 0
			} else if quote == '"' && current == '\\' {
				i++
			}
			continue
		}
		switch current {
		case '\'', '"':
			quote = current
		case '\\':
			i++
		default:
			if found(i) {
				return i
			}
		}
	}
	return -1
}

func claudeTranscriptModel(path string) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()
	reader := bufio.NewReader(file)
	var model string
	for {
		line, readErr := reader.ReadBytes('\n')
		var record struct {
			Message struct {
				Model string `json:"model"`
			} `json:"message"`
		}
		if json.Unmarshal(line, &record) == nil && strings.TrimSpace(record.Message.Model) != "" {
			model = strings.TrimSpace(record.Message.Model)
		}
		if readErr != nil {
			break
		}
	}
	return model
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}
