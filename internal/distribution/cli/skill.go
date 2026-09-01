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
				var release func() error
				if runtime == agentcfg.RuntimeZcode {
					release, err = env.lockManagedZcodeLifecycle()
					if err != nil {
						refused = append(refused, err)
						continue
					}
				}
				written, failures := env.installRuntimeSkills(runtime, catalog, force, true, true)
				outcomes = append(outcomes, written...)
				refused = append(refused, failures...)
				if release != nil {
					if releaseErr := release(); releaseErr != nil {
						refused = append(refused, releaseErr)
					}
				}
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
		hooksValidateZcodeOutputCommand(env),
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
				if pills || handoff {
					return agentcfg.Outcome{Runtime: runtime, Path: path}, "",
						fmt.Errorf("not-supported-on-zcode: --pills and --handoff are Claude-only (follow-up: issue #274)")
				}
				if force {
					return agentcfg.Outcome{Runtime: runtime, Path: path}, "",
						fmt.Errorf("--force is not supported for ZCode hooks; move or remove the conflicting wrapper, then retry")
				}
				if err := zcodeHookPlatformError(goruntime.GOOS, os.Stat); err != nil {
					return agentcfg.Outcome{Runtime: runtime, Path: path}, "", err
				}
				wrapper, err := zcodeHookWrapperPath()
				if err != nil {
					return agentcfg.Outcome{Runtime: runtime, Path: path}, "", err
				}
				return env.installManagedZcodeHandoffHook(path, wrapper, declared)
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
				if pills || handoff {
					return agentcfg.Outcome{Runtime: runtime, Path: path}, "",
						fmt.Errorf("not-supported-on-zcode: --pills and --handoff are Claude-only (follow-up: issue #274)")
				}
				wrapper, err := zcodeHookWrapperPath()
				if err != nil {
					return agentcfg.Outcome{Runtime: runtime, Path: path}, "", err
				}
				return env.uninstallManagedZcodeHandoffHook(path, wrapper)
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
				input, err := io.ReadAll(cmd.InOrStdin())
				if err != nil {
					return fmt.Errorf("read ZCode handoff input: %w", err)
				}
				if len(input) > 0 && !json.Valid(input) {
					return fmt.Errorf("read ZCode handoff input: invalid JSON")
				}
				return env.printJSON(map[string]string{"additionalContext": string(input)})
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

const zcodeOutputValidationToken = "roca-zcode-output-valid-v1"

func hooksValidateZcodeOutputCommand(env *cliEnv) *cobra.Command {
	return &cobra.Command{
		Use:    "validate-zcode-output",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			input, err := io.ReadAll(cmd.InOrStdin())
			if err != nil {
				return err
			}
			decoder := json.NewDecoder(strings.NewReader(string(input)))
			opening, err := decoder.Token()
			if err != nil || opening != json.Delim('{') || !decoder.More() {
				return fmt.Errorf("invalid ZCode hook output")
			}
			key, err := decoder.Token()
			if err != nil || key != "additionalContext" {
				return fmt.Errorf("invalid ZCode hook output")
			}
			var context string
			if err := decoder.Decode(&context); err != nil || decoder.More() {
				return fmt.Errorf("invalid ZCode hook output")
			}
			closing, err := decoder.Token()
			if err != nil || closing != json.Delim('}') {
				return fmt.Errorf("invalid ZCode hook output")
			}
			var extra any
			if err := decoder.Decode(&extra); err != io.EOF {
				return fmt.Errorf("invalid ZCode hook output")
			}
			fmt.Fprintln(env.out, zcodeOutputValidationToken)
			return nil
		},
	}
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
	root := filepath.Join(home, ".zcode")
	if declared := os.Getenv("ZCODE_HOME"); declared != "" {
		root = agentcfg.Expand(declared, home)
	}
	return filepath.Abs(root)
}

func zcodeHookWrapperPath() (string, error) {
	root, err := zcodeRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "hooks", "roca-handoff.sh"), nil
}

func zcodeHookConfigForWrapper(wrapper string) (string, error) {
	if !filepath.IsAbs(wrapper) || filepath.Base(wrapper) != "roca-handoff.sh" || filepath.Base(filepath.Dir(wrapper)) != "hooks" {
		return "", fmt.Errorf("invalid registered ZCode wrapper path %s", wrapper)
	}
	root := filepath.Dir(filepath.Dir(wrapper))
	return filepath.Join(root, "cli", "config.json"), nil
}

func zcodeManagedHookState(configPath string) (present, verified bool) {
	body, err := os.ReadFile(configPath)
	if os.IsNotExist(err) {
		return false, true
	}
	if err != nil {
		return false, false
	}
	next, err := agentcfg.RemoveZcodeSessionStartHook(string(body), zcodeSessionStartMarker)
	if err != nil {
		return false, false
	}
	return next != string(body), true
}

func zcodeManagedHookDeclared(config string) bool {
	next, err := agentcfg.RemoveZcodeSessionStartHook(config, zcodeSessionStartMarker)
	return err == nil && next != config
}

func zcodeManagedHookPresent(configPath string) bool {
	present, _ := zcodeManagedHookState(configPath)
	return present
}

func zcodeHookSelected(configPath, _ string) (bool, error) {
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	present, verified := zcodeManagedHookState(configPath)
	if !verified {
		return false, fmt.Errorf("could not verify ZCode hook markers in %s", configPath)
	}
	return present, nil
}

const (
	zcodeSessionStartMarker     = "roca_session_start_marker"
	zcodeWrapperStateFormat     = "zcode-wrapper-v1"
	zcodeRetainedEnabledWarning = "left operator-owned hooks.enabled unchanged"
)

type zcodeHookPathState struct {
	createdRoot, createdConfigDir, createdHooksDir, createdConfig, createdLock bool
	createdHooksEnabled                                                        bool
	rootIdentity                                                               string
}

func zcodeHookPathPreimage(configPath, wrapperPath string) (zcodeHookPathState, error) {
	root := filepath.Dir(filepath.Dir(wrapperPath))
	paths := []string{root, filepath.Dir(configPath), filepath.Dir(wrapperPath), configPath, filepath.Join(root, ".roca-hooks.lock")}
	missing := make([]bool, len(paths))
	for index, path := range paths {
		_, err := os.Lstat(path)
		switch {
		case os.IsNotExist(err):
			missing[index] = true
		case err != nil:
			return zcodeHookPathState{}, err
		}
	}
	return zcodeHookPathState{
		createdRoot: missing[0], createdConfigDir: missing[1], createdHooksDir: missing[2],
		createdConfig: missing[3], createdLock: missing[4],
	}, nil
}

func (env *cliEnv) recordZcodeWrapperState(path, mutationPath, executable string, preimage zcodeHookPathState,
	managedDeclarationContinuous bool) (func() error, error) {
	paths, err := env.resolvePaths()
	if err != nil {
		return nil, err
	}
	expected := zcodeWrapper(executable)
	transaction := artifact.Entry{
		Kind: artifactKindHook, Runtime: agentcfg.RuntimeZcode, Path: path, MutationPath: mutationPath,
		InstalledVersion: env.build.Version, AvailableVersion: env.build.Version,
		SystemSHA256: artifact.Checksum(expected), Format: zcodeWrapperStateFormat,
		Executable:  executable,
		CreatedRoot: preimage.createdRoot, RootIdentity: preimage.rootIdentity,
		CreatedConfigDir: preimage.createdConfigDir, CreatedHooksDir: preimage.createdHooksDir,
		CreatedConfig: preimage.createdConfig, CreatedLock: preimage.createdLock,
		CreatedHooksEnabled: preimage.createdHooksEnabled,
	}
	var prior artifact.Entry
	var priorFound bool
	_, err = env.mutateArtifactRegistry(paths.Artifacts, func(registry *artifact.Registry) (bool, error) {
		prior, priorFound = registry.Find(artifactKindHook, agentcfg.RuntimeZcode, path)
		if carryForwardZcodeConfigOwnership(prior, priorFound, &transaction) {
			transaction.CreatedHooksDir = transaction.CreatedHooksDir || prior.CreatedHooksDir
			transaction.CreatedLock = transaction.CreatedLock || prior.CreatedLock
			transaction.CreatedHooksEnabled = transaction.CreatedHooksEnabled ||
				(prior.CreatedHooksEnabled && managedDeclarationContinuous)
		}
		registry.Upsert(transaction)
		return true, nil
	})
	if err != nil {
		current, found, readErr := env.registeredArtifact(artifactKindHook, agentcfg.RuntimeZcode, path)
		if readErr != nil || !found || current != transaction {
			return nil, errors.Join(err, readErr)
		}
	}
	return env.artifactRegistryRollback(paths.Artifacts, transaction, prior, priorFound), nil
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

func (env *cliEnv) withLockedManagedZcodeHook(configPath, wrapperPath string,
	operation func(string, string) (agentcfg.Outcome, string, error)) (outcome agentcfg.Outcome, warning string, err error) {
	configPath, err = filepath.Abs(configPath)
	if err != nil {
		return agentcfg.Outcome{Runtime: agentcfg.RuntimeZcode, Path: configPath}, "", err
	}
	wrapperPath, err = filepath.Abs(wrapperPath)
	if err != nil {
		return agentcfg.Outcome{Runtime: agentcfg.RuntimeZcode, Path: configPath}, "", err
	}
	release, err := env.lockManagedZcodeLifecycle()
	if err != nil {
		return agentcfg.Outcome{Runtime: agentcfg.RuntimeZcode, Path: configPath}, "", err
	}
	defer func() { err = errors.Join(err, release()) }()
	return operation(configPath, wrapperPath)
}

func (env *cliEnv) installManagedZcodeHandoffHook(configPath, wrapperPath, executable string) (agentcfg.Outcome, string, error) {
	return env.withLockedManagedZcodeHook(configPath, wrapperPath,
		func(configPath, wrapperPath string) (agentcfg.Outcome, string, error) {
			return env.installManagedZcodeHandoffHookLocked(configPath, wrapperPath, executable)
		})
}

func (env *cliEnv) installManagedZcodeHandoffHookLocked(configPath, wrapperPath, executable string) (outcome agentcfg.Outcome, warning string, err error) {
	pathPreimage := zcodeHookPathState{}
	root := filepath.Dir(filepath.Dir(wrapperPath))
	pathPreimage.createdRoot, err = ensureZcodeDirectory(root)
	if err == nil {
		pathPreimage.rootIdentity, err = zcodeRootIdentity(root)
	}
	if err != nil {
		return agentcfg.Outcome{Runtime: agentcfg.RuntimeZcode, Path: configPath}, "", err
	}
	release, createdLock, err := lockZcodeHookLifecycle(configPath, wrapperPath, true)
	if err != nil {
		err = errors.Join(err, rollbackCreatedZcodeHookPaths(pathPreimage, configPath, wrapperPath))
		return agentcfg.Outcome{Runtime: agentcfg.RuntimeZcode, Path: configPath}, "", err
	}
	pathPreimage.createdLock = createdLock
	if pathPreimage.createdConfigDir, err = ensureZcodeDirectory(filepath.Dir(configPath)); err == nil {
		pathPreimage.createdHooksDir, err = ensureZcodeDirectory(filepath.Dir(wrapperPath))
	}
	if err != nil {
		err = errors.Join(err, release(), rollbackCreatedZcodeHookPaths(pathPreimage, configPath, wrapperPath))
		return agentcfg.Outcome{Runtime: agentcfg.RuntimeZcode, Path: configPath}, "", err
	}
	published := false
	defer func() {
		err = errors.Join(err, release())
		if err != nil && !published {
			cleanup := pathPreimage
			cleanup.createdConfig = false
			err = errors.Join(err, rollbackCreatedZcodeHookPaths(cleanup, configPath, wrapperPath))
		}
	}()
	previous, _, _, err := env.zcodeWrapperExpected(wrapperPath)
	if err != nil {
		return agentcfg.Outcome{Runtime: agentcfg.RuntimeZcode, Path: configPath}, "", err
	}
	var rollback func() error
	outcome, warning, err = installZcodeHandoffHookWithPrevious(configPath, wrapperPath, executable, previous,
		func(configBefore string, createdEnabled, existed bool, mutationPath string) error {
			pathPreimage.createdConfig = !existed
			pathPreimage.createdHooksEnabled = createdEnabled
			rollback, err = env.recordZcodeWrapperState(wrapperPath, mutationPath, executable, pathPreimage,
				zcodeManagedHookDeclared(configBefore))
			return err
		})
	published = err == nil
	if err != nil && rollback != nil {
		err = errors.Join(err, rollback())
	}
	if err == nil && warning != "" {
		err = fmt.Errorf("ZCode hook installed but inactive in %s; set hooks.enabled to true to enable SessionStart", configPath)
		warning = ""
	} else if err == nil && pathPreimage.createdHooksEnabled {
		warning = fmt.Sprintf("enabled ZCode hooks by adding hooks.enabled: true in %s", configPath)
	}
	return outcome, warning, err
}

func (env *cliEnv) uninstallManagedZcodeHandoffHook(configPath, wrapperPath string, finalize ...func(artifact.Entry, os.FileInfo) error) (agentcfg.Outcome, string, error) {
	return env.withLockedManagedZcodeHook(configPath, wrapperPath,
		func(configPath, wrapperPath string) (agentcfg.Outcome, string, error) {
			return env.uninstallManagedZcodeHandoffHookLocked(configPath, wrapperPath, finalize...)
		})
}

func (env *cliEnv) uninstallManagedZcodeHandoffHookLocked(configPath, wrapperPath string, finalize ...func(artifact.Entry, os.FileInfo) error) (outcome agentcfg.Outcome, warning string, err error) {
	expected, entry, found, err := env.zcodeWrapperExpected(wrapperPath)
	if err != nil {
		return agentcfg.Outcome{Runtime: agentcfg.RuntimeZcode, Path: configPath}, "", err
	}
	if !found {
		warning = fmt.Sprintf("warning: no managed ZCode hook ownership for %s; left operator configuration unchanged", configPath)
		return agentcfg.Outcome{Runtime: agentcfg.RuntimeZcode, Path: configPath}, warning, nil
	}
	rootContinuous, rootExists, err := zcodeRootContinuity(configPath, entry)
	if err != nil {
		return agentcfg.Outcome{Runtime: agentcfg.RuntimeZcode, Path: configPath}, "", err
	}
	if !rootContinuous && (!rootExists || !zcodeRootClaimAllowsWithdrawal(entry)) {
		err = env.unregisterArtifactEntry(entry)
		return agentcfg.Outcome{Runtime: agentcfg.RuntimeZcode, Path: configPath}, "", err
	}
	release, _, err := lockZcodeHookLifecycle(configPath, wrapperPath, false)
	if err != nil {
		return agentcfg.Outcome{Runtime: agentcfg.RuntimeZcode, Path: configPath}, "", err
	}
	localReleased := false
	defer func() {
		if !localReleased {
			err = errors.Join(err, release())
		}
	}()
	removeEnabled := entry.CreatedHooksEnabled
	outcome, warning, err = uninstallZcodeHandoffHookUnlocked(configPath, wrapperPath, expected, removeEnabled)
	present, verified := zcodeManagedHookState(configPath)
	if err == nil && len(finalize) > 0 {
		switch {
		case !verified || present:
			err = fmt.Errorf("ZCode hook withdrawal from %s could not be verified", configPath)
		default:
			err = release()
			localReleased = true
			if err == nil {
				err = finalize[0](entry, outcome.FileIdentity)
			}
		}
	} else if err == nil && verified && !present {
		err = env.unregisterArtifactEntry(entry)
	}
	return outcome, warning, err
}

func installZcodeHandoffHook(configPath, wrapperPath, executable string) (outcome agentcfg.Outcome, warning string, err error) {
	release, _, err := lockZcodeHookLifecycle(configPath, wrapperPath, true)
	if err != nil {
		return agentcfg.Outcome{Runtime: agentcfg.RuntimeZcode, Path: configPath}, "", err
	}
	defer func() { err = errors.Join(err, release()) }()
	return installZcodeHandoffHookUnlocked(configPath, wrapperPath, executable)
}

func installZcodeHandoffHookUnlocked(configPath, wrapperPath, executable string) (agentcfg.Outcome, string, error) {
	return installZcodeHandoffHookWithPrevious(configPath, wrapperPath, executable, nil)
}

func installZcodeHandoffHookWithPrevious(configPath, wrapperPath, executable string, previous []byte,
	record ...func(string, bool, bool, string) error) (agentcfg.Outcome, string, error) {
	state, err := readZcodeWrapperState(wrapperPath)
	if err != nil {
		return agentcfg.Outcome{Runtime: agentcfg.RuntimeZcode, Path: configPath}, "", err
	}
	wrapper := zcodeWrapper(executable)
	if state.exists && string(state.body) != wrapper && string(state.body) != string(previous) {
		return agentcfg.Outcome{Runtime: agentcfg.RuntimeZcode, Path: configPath}, "",
			fmt.Errorf("refuse to replace operator-modified ZCode hook wrapper %s; move or remove it, then retry", wrapperPath)
	}
	if err := writeZcodeWrapper(wrapperPath, wrapper, state, previous); err != nil {
		return agentcfg.Outcome{Runtime: agentcfg.RuntimeZcode, Path: configPath}, "", err
	}
	command := zcodeOwnedHookCommand(wrapperPath)
	var recordState func(string, bool, bool, string) error
	if len(record) > 0 {
		recordState = record[0]
	}
	outcome, err := agentcfg.InstallZcodeSessionStartHook(
		configPath, zcodeSessionStartMarker, command, 15000, recordState)
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
			return outcome, fmt.Sprintf("warning: ZCode hook installed but inactive because hooks.enabled is false in %s; set it to true to enable SessionStart", configPath), nil
		}
	}
	return outcome, "", nil
}

func uninstallZcodeHandoffHook(configPath, wrapperPath string, expected ...[]byte) (outcome agentcfg.Outcome, warning string, err error) {
	release, _, err := lockZcodeHookLifecycle(configPath, wrapperPath, false)
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

func uninstallZcodeHandoffHookUnlocked(configPath, wrapperPath string, expected []byte, removeEnabled ...bool) (agentcfg.Outcome, string, error) {
	var warning string
	keepWrapper := false
	command := zcodeOwnedHookCommand(wrapperPath)
	outcome, err := agentcfg.Edit(agentcfg.RuntimeZcode, configPath, func(previous string) (string, error) {
		withoutManaged, editErr := agentcfg.RemoveZcodeSessionStartHook(previous, zcodeSessionStartMarker)
		if editErr != nil {
			keepWrapper = true
			warning = fmt.Sprintf("warning: %s is not readable as zcode settings; remove the nested hooks.events.SessionStart entry carrying La Roca marker %q and command %s by hand", configPath, zcodeSessionStartMarker, command)
			return previous, nil
		}
		commands, commandsErr := agentcfg.ZcodeHookCommands(withoutManaged)
		if commandsErr != nil {
			keepWrapper = true
			warning = fmt.Sprintf("warning: could not verify remaining ZCode hooks in %s: %v", configPath, commandsErr)
			return withoutManaged, nil
		}
		next := withoutManaged
		removeOwnedEnabled := len(removeEnabled) > 0 && removeEnabled[0] && zcodeManagedHookDeclared(previous)
		enabledDeclared, enabled, enabledErr := agentcfg.ZcodeHooksEnabled(previous)
		if enabledErr != nil {
			return previous, enabledErr
		}
		if removeOwnedEnabled && enabledDeclared && enabled && len(commands) == 0 {
			next, editErr = agentcfg.RemoveCreatedZcodeHooksEnabled(previous)
			if editErr == nil {
				next, editErr = agentcfg.RemoveZcodeSessionStartHook(next, zcodeSessionStartMarker)
			}
		} else if enabledDeclared && len(commands) == 0 && (!removeOwnedEnabled || !enabled) {
			warning = fmt.Sprintf("warning: %s in %s", zcodeRetainedEnabledWarning, configPath)
		}
		if editErr != nil {
			return previous, editErr
		}
		referenced, referenceErr := zcodeHookCommandsReferenceWrapper(commands, wrapperPath)
		if referenceErr != nil {
			keepWrapper = true
			warning = fmt.Sprintf("warning: conserved %s; possible reference in a neighboring hook in %s: %v",
				wrapperPath, configPath, referenceErr)
			return next, nil
		}
		keepWrapper = referenced
		return next, nil
	}, false)
	if err != nil {
		return outcome, warning, err
	}
	if keepWrapper {
		if warning == "" {
			warning = fmt.Sprintf("warning: kept %s because a remaining operator-owned hook references it", wrapperPath)
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

func lockZcodeHookLifecycle(configPath, wrapperPath string, create bool) (func() error, bool, error) {
	return lockZcodeHookLifecycleWith(configPath, wrapperPath, create, lockExistingZcodeLifecycle)
}

func lockZcodeHookLifecycleWith(configPath, wrapperPath string, create bool,
	acquire func(string, os.FileInfo) (func() error, error)) (func() error, bool, error) {
	root := filepath.Dir(filepath.Dir(wrapperPath))
	lockPath := filepath.Join(root, ".roca-hooks.lock")
	if !create {
		_, configErr := os.Lstat(configPath)
		_, wrapperErr := os.Lstat(wrapperPath)
		_, lockErr := os.Lstat(lockPath)
		if os.IsNotExist(configErr) && os.IsNotExist(wrapperErr) && os.IsNotExist(lockErr) {
			return func() error { return nil }, false, nil
		}
	}
	if _, err := ensureZcodeDirectory(root); err != nil {
		return nil, false, err
	}
	for {
		info, err := os.Lstat(lockPath)
		if err == nil {
			if !info.Mode().IsRegular() {
				return nil, false, fmt.Errorf("refuse non-regular ZCode lifecycle lock %s", lockPath)
			}
			release, err := acquire(lockPath, info)
			return release, false, err
		}
		if !os.IsNotExist(err) {
			return nil, false, err
		}
		file, createErr := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
		if os.IsExist(createErr) {
			continue
		}
		if createErr != nil {
			return nil, false, createErr
		}
		info, statErr := file.Stat()
		closeErr := file.Close()
		if statErr != nil || closeErr != nil {
			if statErr == nil {
				closeErr = errors.Join(closeErr, cleanupCreatedZcodeLifecycleLock(lockPath, info))
			}
			return nil, false, errors.Join(statErr, closeErr)
		}
		release, err := acquire(lockPath, info)
		if err != nil {
			err = errors.Join(err, cleanupCreatedZcodeLifecycleLock(lockPath, info))
			return nil, false, err
		}
		return release, true, nil
	}
}

func cleanupCreatedZcodeLifecycleLock(path string, expected os.FileInfo) error {
	_, err := removeOwnedZcodeArtifact(path, func(_ string, info os.FileInfo) (bool, error) {
		return info.Mode().IsRegular() && info.Size() == 0 && os.SameFile(expected, info), nil
	}, nil, os.Remove)
	return err
}

func lockExistingZcodeLifecycle(path string, expected os.FileInfo) (func() error, error) {
	release, err := securefile.LockExisting(path)
	if err != nil {
		return nil, err
	}
	current, currentErr := os.Lstat(path)
	if currentErr != nil || !current.Mode().IsRegular() || !os.SameFile(expected, current) {
		return nil, errors.Join(currentErr, release(),
			fmt.Errorf("ZCode lifecycle lock changed while acquiring %s", path))
	}
	return release, nil
}

type zcodeWrapperState struct {
	body   []byte
	mode   os.FileMode
	exists bool
}

func readZcodeWrapperState(path string) (zcodeWrapperState, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return zcodeWrapperState{}, nil
	}
	if err != nil {
		return zcodeWrapperState{}, fmt.Errorf("stat %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return zcodeWrapperState{}, fmt.Errorf("refuse non-regular ZCode hook wrapper %s", path)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return zcodeWrapperState{}, fmt.Errorf("read %s: %w", path, err)
	}
	return zcodeWrapperState{body: body, mode: info.Mode(), exists: true}, nil
}

func restoreZcodeWrapper(path string, state zcodeWrapperState, installed []byte) error {
	if !state.exists {
		retained, err := removeZcodeWrapper(path, installed)
		if err != nil {
			return err
		}
		if retained {
			return fmt.Errorf("roll back %s: wrapper changed after installation", path)
		}
		return nil
	}
	if err := securefile.Replace(path, state.body, installed); err != nil {
		return fmt.Errorf("roll back %s: %w", path, err)
	}
	return os.Chmod(path, state.mode)
}

func zcodeWrapper(executable string) string {
	return `#!/bin/bash
# Managed by roca hooks install zcode.
set -euo pipefail

if handoff=$(` + shellQuote(executable) + ` handoff latest --json 2>/dev/null) &&
  output=$(printf '%s' "$handoff" | ` + shellQuote(executable) + ` hooks run zcode 2>/dev/null) &&
  validation=$(printf '%s' "$output" | ` + shellQuote(executable) + ` hooks validate-zcode-output 2>/dev/null) &&
  [ "$validation" = ` + shellQuote(zcodeOutputValidationToken) + ` ]; then
  printf '%s\n' "$output"
else
  printf '{"additionalContext":""}\n'
fi
`
}

func writeZcodeWrapper(path, content string, state zcodeWrapperState, previous ...[]byte) error {
	if state.exists {
		if string(state.body) != content {
			var managed []byte
			if len(previous) > 0 {
				managed = previous[0]
			}
			if len(managed) == 0 || string(state.body) != string(managed) {
				return fmt.Errorf("refuse to replace operator-modified ZCode hook wrapper %s", path)
			}
			if err := securefile.Replace(path, []byte(content), state.body); err != nil {
				return err
			}
			return os.Chmod(path, 0o700)
		}
		if state.mode == 0o700 {
			return nil
		}
		return os.Chmod(path, 0o700)
	}
	return securefile.CreatePreservingParentMode(path, []byte(content), 0o700, 0o700)
}

func removeZcodeWrapper(path string, expected []byte) (bool, error) {
	return removeZcodeWrapperAfterQuarantine(path, expected, nil)
}

func removeZcodeWrapperAfterQuarantine(path string, expected []byte, afterRename func()) (bool, error) {
	return removeZcodeWrapperQuarantine(path, expected, afterRename, os.Remove)
}

func removeZcodeWrapperQuarantine(path string, expected []byte, afterRename func(), removeQuarantine func(string) error) (bool, error) {
	if len(expected) == 0 {
		return removeOwnedZcodeArtifact(path, nil, afterRename, removeQuarantine)
	}
	return removeOwnedZcodeArtifact(path, zcodeRegularFileVerifier(func(body []byte) bool {
		return string(body) == string(expected)
	}), afterRename, removeQuarantine)
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
