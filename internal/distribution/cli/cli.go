package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/thellmwhisperer/la-roca/internal/distribution/logfile"
	"github.com/thellmwhisperer/la-roca/internal/distribution/plugininstall"
	"github.com/thellmwhisperer/la-roca/internal/distribution/rocaops"
	"github.com/thellmwhisperer/la-roca/internal/ingest"
	"github.com/thellmwhisperer/la-roca/internal/provider"
	"github.com/thellmwhisperer/la-roca/internal/provider/config"
	"github.com/thellmwhisperer/la-roca/internal/provider/service"
)

// Build is what the linker put inside the binary.
type Build struct {
	Version string
	Commit  string
	Date    string
}

// Exit codes. A refusal with a reason is not a program failure, but it is not an
// answer either: it has a code of its own so a script can tell them apart
// without reading prose.
const (
	ExitOK    = 0
	ExitError = 1
)

type cliEnv struct {
	build              Build
	out                io.Writer
	errOut             io.Writer
	dbPath             string
	json               bool
	code               int
	outcome            any
	auditQuery         *service.QueryResult
	correlation        string
	auditCommand       string
	auditArgs          []string
	started            time.Time
	prelogged          bool
	openedDir          string
	liveIngest         *ingestRows
	wantIngestProgress bool
	ingestStarted      time.Time
	modelBackend       modelValidationBackend
	modelPicker        modelPicker
	skipReconciliation bool
	skipInitChooser    bool
	initPromptWait     time.Duration
	initChooserElapsed time.Duration
}

// Execute runs the CLI and returns the process exit code.
func Execute(build Build) (int, error) {
	return executeCommand(build, os.Stdout, os.Stderr, os.Args[1:], true)
}

// execute is Execute over writers and an argument list a test can supply. It
// deliberately leaves plugin dispatch to the production entry point.
func execute(build Build, out, errOut io.Writer, args []string) (int, error) {
	return executeCommand(build, out, errOut, args, false)
}

func executeCommand(build Build, out, errOut io.Writer, args []string, plugins bool) (int, error) {
	started := time.Now()
	env := &cliEnv{build: build, out: out, errOut: errOut, started: started}
	return executeWithOptions(env, args, nil, plugins)
}

func executeWithEnv(env *cliEnv, args []string, in io.Reader) (int, error) {
	return executeWithOptions(env, args, in, false)
}

func executeWithOptions(env *cliEnv, args []string, in io.Reader, plugins bool) (int, error) {
	started := env.started
	if started.IsZero() {
		started = time.Now()
		env.started = started
	}
	root := rootCommand(env)
	if plugins {
		if handled, code, err := dispatchPlugin(root, args); handled {
			env.auditCommand = args[0]
			env.auditArgs = redactPluginArguments(args[1:])
			if err != nil {
				err = logfile.Correlate(err)
			} else if code != ExitOK {
				// The plugin exited non-zero on its own account, and its streams
				// crossed this seam untouched. Naming the log line here would write
				// a line roca invented into output the plugin owns, so the ID is
				// minted for the audit record and read back through `roca doctor`.
				env.correlationID()
			}
			if logErr := env.logExecution(nil, started, code, err); logErr != nil {
				fmt.Fprintf(env.errOut,
					"warning: this run is not in the execution log: %v\n", logErr)
			}
			return code, err
		}
	}
	root.SetArgs(args)
	if in != nil {
		root.SetIn(in)
	}
	executed, err := root.ExecuteC()
	if reconciliationErr := env.reconcileAfterCommand(executed); err == nil {
		err = reconciliationErr
	}
	if err != nil {
		if strings.Contains(err.Error(), "unknown command") {
			err = fmt.Errorf("%w; run `roca --help` to list commands", err)
			if len(args) > 0 && !strings.HasPrefix(args[0], "-") && !builtIn(root, args[0]) {
				err = fmt.Errorf("%w; a `roca-%s` executable on your PATH would handle this", err, args[0])
			}
			err = logfile.Typed(err, logfile.ErrorInvalidUsage)
		}
	}
	code := env.code
	if err != nil {
		code = ExitError
		err = logfile.Correlate(err)
	}
	// The trace is observability, and observability never fails the command.
	//
	// A log that could not be written is said once, on the error stream, and it
	// changes neither the answer nor the exit code. Returning it as the run's
	// error made a query that had already printed its answer exit 1, so a script
	// reading the code concluded the query failed while holding the answer.
	//
	// The ID is surfaced here and not earlier because it may only name a record
	// this run is about to write: a command that logged itself already carries
	// the verdict of what it wrote.
	if !env.prelogged {
		if err == nil && code != ExitOK {
			env.surfaceCorrelation()
		}
		if logErr := env.logExecution(executed, started, code, err); logErr != nil {
			fmt.Fprintf(env.errOut,
				"warning: this run is not in the execution log: %v\n", logErr)
		}
	}
	return code, err
}

func rootCommand(env *cliEnv) *cobra.Command {
	root := &cobra.Command{
		Use:   "roca",
		Short: "Local semantic memory for agent fleets",
		Long: "La Roca is the memory your agents leave behind, made searchable.\n" +
			"\n" +
			"It reads what Claude, Codex, OpenCode and the rest write to disk, normalizes\n" +
			"that into one local SQLite database, and answers natural-language questions\n" +
			"about it. The database and search index stay on this machine. Natural-language\n" +
			"questions, and up to ten result rows for --full, go to the selected model provider.",
		SilenceUsage:  true,
		SilenceErrors: true,
		// `roca --version` is the health check `install.sh` and `roca update` run
		// before they trust a
		// binary. It answers exactly what `roca version` answers: the same
		// question spelled two ways may not have two answers.
		Version:           env.build.Version,
		CompletionOptions: cobra.CompletionOptions{DisableDefaultCmd: true},
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	root.SetVersionTemplate(versionLine(env.build) + "\n")
	root.PersistentFlags().StringVar(&env.dbPath, "db-path", "", "database to use")
	root.PersistentFlags().BoolVar(&env.json, "json", false, "JSON output")
	root.AddCommand(
		versionCommand(env), initCommand(env), queryCommand(env), exploreCommand(env),
		execCommand(env), schemaCommand(env),
		indexCommand(env), doctorCommand(env),
		ingestCommand(env), storeCommand(env), healthCommand(env),
		mcpCommand(env), skillCommand(env), hooksCommand(env),
		loginCommand(env), modelCommand(env),
		updateCommand(env), uninstallCommand(env),
		modelsCommand(env), pluginCommand(env), pluginsCommand(env),
		cronCommand(env),
		opsCommand(env), installBundledPluginsCommand(env),
		capabilitiesCommand(env), artifactsCommand(env),
	)
	root.InitDefaultHelpCmd()
	for _, command := range root.Commands() {
		if command.Name() == "help" {
			command.Use = "_help [command]"
			command.Aliases = []string{"help"}
			root.SetHelpCommand(command)
		}
		command.Hidden = !publicCommand(command.Name())
	}
	return root
}

func publicCommand(name string) bool {
	switch name {
	case "init", "query", "explore", "store", "ingest", "model", "doctor", "update", "uninstall", "plugin", "plugins", "hooks", "cron":
		return true
	default:
		return false
	}
}

type plugin struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

func dispatchPlugin(root *cobra.Command, args []string) (bool, int, error) {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") || builtIn(root, args[0]) {
		return false, 0, nil
	}
	path, found := findPlugin(args[0])
	if !found {
		return false, 0, nil
	}
	command := exec.Command(path, args[1:]...)
	command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr
	err := command.Run()
	if err == nil {
		return true, ExitOK, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return true, exit.ExitCode(), nil
	}
	return true, ExitError, fmt.Errorf("execute plugin %s: %w", path, err)
}

func builtIn(root *cobra.Command, name string) bool {
	for _, command := range root.Commands() {
		if command.Name() == name || slices.Contains(command.Aliases, name) {
			return true
		}
	}
	return false
}

func findPlugin(name string) (string, bool) {
	for _, directory := range pluginPathDirectories() {
		for _, filename := range pluginFilenames("roca-" + name) {
			path := filepath.Join(directory, filename)
			if isExecutable(path) {
				return path, true
			}
		}
	}
	return "", false
}

func pluginsCommand(env *cliEnv) *cobra.Command {
	return &cobra.Command{
		Use:   "plugins",
		Short: "List neighbor plugin executables on PATH",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			plugins := listPlugins()
			if env.json {
				return env.printJSON(map[string]any{"plugins": plugins})
			}
			for _, plugin := range plugins {
				env.print("%s\t%s", plugin.Name, plugin.Path)
			}
			return nil
		},
	}
}

func listPlugins() []plugin {
	found := []plugin{}
	for _, directory := range pluginPathDirectories() {
		entries, err := os.ReadDir(directory)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			name, ok := pluginName(entry.Name())
			path := filepath.Join(directory, entry.Name())
			if ok && isExecutable(path) {
				found = append(found, plugin{Name: name, Path: path})
			}
		}
	}
	slices.SortFunc(found, func(a, b plugin) int {
		return strings.Compare(a.Path, b.Path)
	})
	return found
}

func pluginPathDirectories() []string {
	cwd, _ := filepath.EvalSymlinks(mustAbs("."))
	seen := map[string]bool{}
	var directories []string
	for _, item := range filepath.SplitList(os.Getenv("PATH")) {
		if item == "" {
			continue
		}
		directory := mustAbs(item)
		resolved, err := filepath.EvalSymlinks(directory)
		if err != nil {
			resolved = directory
		}
		if resolved == cwd || seen[resolved] {
			continue
		}
		seen[resolved] = true
		directories = append(directories, directory)
	}
	return directories
}

func mustAbs(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	return abs
}

func pluginFilenames(base string) []string {
	if runtime.GOOS == "windows" {
		return []string{base + ".exe", base + ".com", base + ".bat", base + ".cmd"}
	}
	return []string{base}
}

func pluginName(filename string) (string, bool) {
	name := filename
	if runtime.GOOS == "windows" {
		ext := strings.ToLower(filepath.Ext(filename))
		if !slices.Contains([]string{".exe", ".com", ".bat", ".cmd"}, ext) {
			return "", false
		}
		name = strings.TrimSuffix(filename, filepath.Ext(filename))
	}
	name, ok := strings.CutPrefix(name, "roca-")
	return name, ok && name != ""
}

func isExecutable(path string) bool {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	return runtime.GOOS == "windows" || info.Mode().Perm()&0o111 != 0
}

const envRocaPrefix = "ROCA_PREFIX"

func pluginCommand(env *cliEnv) *cobra.Command {
	var consented bool
	command := &cobra.Command{
		Use:   "plugin",
		Short: "Install, update, or uninstall an experimental plugin",
		Long: "Manages verified plugin packages from a local directory, a Git URL, or\n" +
			"an owner/repo source. This experimental surface requires features.plugins=true.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	command.PersistentFlags().BoolVar(&consented, "yes", false, "accept the displayed plugin risk without prompting")
	command.AddCommand(pluginInstallCommand(env, &consented), pluginUpdateCommand(env, &consented),
		pluginUninstallCommand(env, &consented))
	return command
}

func pluginInstallCommand(env *cliEnv, consented *bool) *cobra.Command {
	return &cobra.Command{
		Use:   "install <path|url|owner/repo>",
		Short: "Verify a source and install its plugin",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			manager, scratch, err := env.pluginManager()
			if err != nil {
				return err
			}
			candidate, cleanup, err := resolvePluginCandidate(cmd.Context(), args[0], scratch)
			if err != nil {
				return err
			}
			defer cleanup()
			accepted, err := env.confirmPlugin(cmd.InOrStdin(), "install", candidate, "", *consented)
			if err != nil || !accepted {
				return err
			}
			result, err := manager.Install(candidate)
			if err != nil {
				return err
			}
			return env.reportPlugin("installed", result)
		},
	}
}

func pluginUpdateCommand(env *cliEnv, consented *bool) *cobra.Command {
	return &cobra.Command{
		Use:   "update <name>",
		Short: "Refresh a plugin from its recorded source",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			manager, scratch, err := env.pluginManager()
			if err != nil {
				return err
			}
			manifest, err := plugininstall.ReadManifest(filepath.Join(manager.PluginRoot, args[0]))
			if err != nil {
				return err
			}
			candidate, cleanup, err := resolvePluginCandidate(cmd.Context(), manifest.Source, scratch)
			if err != nil {
				return err
			}
			defer cleanup()
			if candidate.Name != args[0] {
				return fmt.Errorf("recorded source now names plugin %q, not %q; update refused",
					candidate.Name, args[0])
			}
			accepted, err := env.confirmPlugin(cmd.InOrStdin(), "update", candidate, manifest.Checksum, *consented)
			if err != nil || !accepted {
				return err
			}
			result, err := manager.Update(candidate)
			if err != nil {
				return err
			}
			return env.reportPlugin("updated", result)
		},
	}
}

func pluginUninstallCommand(env *cliEnv, consented *bool) *cobra.Command {
	return &cobra.Command{
		Use:   "uninstall <name>",
		Short: "Remove a plugin, protecting custodial data",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			manager, _, err := env.pluginManager()
			if err != nil {
				return err
			}
			manifest, err := plugininstall.ReadManifest(filepath.Join(manager.PluginRoot, args[0]))
			if err != nil {
				return err
			}
			candidate := plugininstall.Candidate{
				Name: manifest.Name, Version: manifest.Version, Source: manifest.Source,
				Checksum: manifest.Checksum, Risk: manifest.Risk, Custody: manifest.Custody,
			}
			accepted, err := env.confirmPlugin(cmd.InOrStdin(), "uninstall", candidate, "", *consented)
			if err != nil || !accepted {
				return err
			}
			result, err := manager.Uninstall(args[0])
			if err != nil {
				return err
			}
			return env.reportPlugin("uninstalled", result)
		},
	}
}

func (env *cliEnv) pluginManager() (plugininstall.Manager, string, error) {
	paths, err := env.resolvePaths()
	if err != nil {
		return plugininstall.Manager{}, "", err
	}
	file, err := config.LoadFile(paths.Config)
	if err != nil {
		return plugininstall.Manager{}, "", err
	}
	if !file.Features.Plugins {
		return plugininstall.Manager{}, "", fmt.Errorf(
			"the experimental plugin system is disabled; set features.plugins = true in %s",
			paths.Config)
	}
	if paths.Home == "" {
		return plugininstall.Manager{}, "", fmt.Errorf("I do not know where your HOME is; plugin installation requires ~/.roca/plugins")
	}
	return plugininstall.Manager{
		PluginRoot: pluginRoot(paths), BinDir: pluginExecutableDir(paths),
		ArchiveRoot: custodyRoot(paths),
	}, pluginDownloads(paths), nil
}

func resolvePluginCandidate(ctx context.Context, reference, scratch string) (plugininstall.Candidate, func(), error) {
	resolved, cleanup, err := plugininstall.Resolve(ctx, reference, scratch)
	if err != nil {
		return plugininstall.Candidate{}, func() {}, err
	}
	candidate, err := plugininstall.Inspect(resolved.Reference, resolved.Directory)
	if err != nil {
		cleanup()
		return plugininstall.Candidate{}, func() {}, err
	}
	return candidate, cleanup, nil
}

func pluginConsentText(action string, candidate plugininstall.Candidate, trusted string) string {
	var risk string
	switch {
	case candidate.Risk != plugininstall.Executable:
		risk = "DATA-ONLY: near-harmless; its worst case is lying content returned from its database."
	case candidate.Executable == "":
		risk = "EXECUTABLE: FULL TRUST; its cron rides run commands with your user privileges."
	default:
		risk = "EXECUTABLE: FULL TRUST; it runs code with your user privileges."
	}
	custody := ""
	if candidate.Custody {
		custody = "\ncustody: protected; uninstall archives this directory instead of deleting it"
	}
	return fmt.Sprintf("Plugin %s consent\nsource: %s\nversion: %s\nchecksum: sha256:%s%s\nrisk: %s%s\n",
		action, candidate.Source, candidate.Version, candidate.Checksum,
		checksumComparison(candidate.Checksum, trusted), risk, custody)
}

// checksumComparison names what is being replaced. Without the recorded value a
// source takeover and an ordinary version bump look identical on this screen:
// both show one unfamiliar checksum.
func checksumComparison(current, trusted string) string {
	switch {
	case trusted == "":
		return ""
	case trusted == current:
		return " (unchanged since the recorded install)"
	default:
		return fmt.Sprintf(" (replaces the recorded sha256:%s)", trusted)
	}
}

func (env *cliEnv) confirmPlugin(input io.Reader, action string, candidate plugininstall.Candidate,
	trusted string, consented bool) (bool, error) {

	text := pluginConsentText(action, candidate, trusted)
	if consented {
		if !env.json {
			fmt.Fprint(env.errOut, text)
			fmt.Fprintln(env.errOut, "consent: accepted by --yes")
		}
		return true, nil
	}
	if env.json {
		return false, fmt.Errorf("plugin %s needs consent; inspect it without --json or pass --yes", action)
	}
	fmt.Fprint(env.errOut, text)
	fmt.Fprintf(env.errOut, "Proceed with plugin %s? [y/N] ", action)
	line, err := bufio.NewReader(input).ReadString('\n')
	if err != nil && line == "" {
		return false, fmt.Errorf("read plugin consent: %w", err)
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true, nil
	case "", "n", "no":
		if !env.json {
			env.print("plugin %s canceled", action)
		}
		return false, nil
	default:
		return false, fmt.Errorf("plugin consent %q is not valid; answer yes or no", strings.TrimSpace(line))
	}
}

func (env *cliEnv) reportPlugin(action string, result plugininstall.Result) error {
	document := map[string]any{
		"action": action, "name": result.Name, "version": result.Version,
		"checksum": result.Checksum, "risk": result.Risk, "directory": result.Directory,
	}
	if result.Executable != "" {
		document["executable"] = result.Executable
	}
	if result.ArchivedTo != "" {
		document["archived_to"] = result.ArchivedTo
	}
	if env.json {
		return env.printJSON(document)
	}
	if result.ArchivedTo != "" {
		env.print("plugin %s %s; custodial data archived at %s", result.Name, action, result.ArchivedTo)
		return nil
	}
	env.print("plugin %s %s at %s", result.Name, action, result.Directory)
	return nil
}

// resolvePaths decides where everything of this installation lives, without
// touching the database. Commands that only need a path (such as model) pay
// nothing for it.
func (env *cliEnv) resolvePaths() (config.Paths, error) {
	home, _ := os.UserHomeDir()
	return config.Resolve(config.Input{
		Flag:      env.dbPath,
		Env:       os.Getenv(config.EnvDBPath),
		Home:      home,
		ConfigEnv: os.Getenv(config.EnvConfig),
	})
}

// openService resolves the paths, reads the configuration and opens the
// database. It neither creates nor adopts it: that is what init asks for
// explicitly.
func (env *cliEnv) openService() (*service.Service, config.Paths, error) {
	paths, err := env.resolvePaths()
	if err != nil {
		return nil, paths, err
	}
	if !fileExists(paths.DB) {
		return nil, paths, logfile.Typed(fmt.Errorf(
			"no Roca database exists at %s; run `roca init` before this command", paths.DB),
			logfile.ErrorNotInitialized)
	}
	svc, err := env.openServiceWith(paths)
	return svc, paths, err
}

// openServiceWith opens the service from already-resolved paths. Init calls it
// after its own setup (adoption by copy, migration) so that the paths are
// already known when the database is opened.
func (env *cliEnv) openServiceWith(paths config.Paths) (*service.Service, error) {
	if err := os.MkdirAll(dirOf(paths.DB), 0o700); err != nil {
		return nil, fmt.Errorf("create the database directory: %w", err)
	}

	file, err := config.LoadFile(paths.Config)
	if err != nil {
		return nil, err
	}
	home, _ := os.UserHomeDir()
	pluginDir := ""
	if home != "" {
		pluginDir = filepath.Join(home, config.DirOwn, "plugins")
	}
	readOnly := config.ReadOnly(os.Getenv(config.EnvReadOnly))
	// Placing the bundled plugin writes a directory, a manifest and a schema.
	// Read-only refuses writes before any of that, so an audit of a machine
	// leaves it exactly as it found it.
	if file.Features.RocaOps && !readOnly {
		if pluginDir == "" {
			return nil, fmt.Errorf("features.roca_ops needs a HOME for the bundled plugin")
		}
		if _, err := rocaops.Ensure(pluginDir, pluginExecutableDir(paths), env.build.Version); err != nil {
			return nil, fmt.Errorf("install bundled roca-ops plugin: %w", err)
		}
	}
	var ingestProgress func(ingest.SourceProgress)
	if env.wantIngestProgress && !env.json && termAware(env.errOut) {
		env.liveIngest = newIngestRows(env.errOut, true)
		ingestProgress = env.liveIngest.update
	}
	providers, interpreters, explorers := buildProviders(file, paths)
	svc, err := service.Open(service.Options{
		DBPath:                    paths.DB,
		BackupDir:                 paths.Backups,
		DataDir:                   filepath.Dir(paths.DB),
		Version:                   env.build.Version,
		Commit:                    env.build.Commit,
		QueryTimeout:              time.Duration(file.Query.TimeoutMS) * time.Millisecond,
		QueryTimeoutSet:           file.Query.TimeoutSet,
		DisableStrictInput:        !file.Features.StrictInput,
		DisableMissingReferentAsk: !file.Features.AskMissingReferent,
		PluginDir:                 pluginDir,
		PluginsEnabled:            file.Features.Plugins,
		RocaOpsEnabled:            file.Features.RocaOps,
		Providers:                 providers,
		Interpreters:              interpreters,
		Explorers:                 explorers,
		ConfigPath:                paths.Config,
		ConfigExists:              file.Exists,
		Sources:                   ingestSources(file, home, paths.Runner),
		ReadOnly:                  readOnly,
		Progress: func(line string) {
			if !env.json && strings.HasPrefix(line, "index: rebuilding") {
				env.initSay("%s", line)
			}
		},
		IngestProgress: ingestProgress,
	})
	if err != nil {
		env.finishIngestProgress()
		return nil, err
	}
	_ = logfile.New(filepath.Dir(paths.DB)).Prepare()
	env.openedDir = filepath.Dir(paths.DB)
	return svc, nil
}

func (env *cliEnv) finishIngestProgress() {
	if env.liveIngest == nil {
		env.wantIngestProgress = false
		return
	}
	env.liveIngest.finish()
	env.liveIngest = nil
	env.wantIngestProgress = false
}

// serviceRunE is the RunE of every command that needs the database open: it
// opens the service, guarantees it is closed on the way out, and hands it to the
// command's own work.
//
// Eight commands were carrying those six lines each. That is not only repetition:
// the one that forgets its `defer svc.Close()` leaks a handle, and a leaked
// handle over SQLite in WAL is a failure that shows up somewhere else, in another
// process, much later.
//
// The commands that also need the resolved paths (init and model commands that
// never opens a database at all) call openService or resolvePaths themselves.
func (env *cliEnv) serviceRunE(
	run func(cmd *cobra.Command, args []string, svc *service.Service) error,
) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		svc, _, err := env.openService()
		if err != nil {
			return err
		}
		defer svc.Close()
		return run(cmd, args, svc)
	}
}

// runtimeStatus is the body of the two status commands over the runtime
// catalogue and `roca mcp status`.
//
// Both answer the same question about a different declaration: which runtimes
// were asked about (the argument, or all of them), one report each, and the
// answer as the same JSON document or the same TOON row per runtime.
// What a report *is* is the only difference, and that is what the type parameter
// carries: the report type stays each package's own, and neither one has to be
// flattened into a shape the other can read.
func runtimeStatus[R any](
	env *cliEnv,
	args, catalogue []string,
	status func(runtime string) (R, error),
	columns []string,
	row func(R) map[string]any,
	help ...string,
) error {
	runtimes := catalogue
	if len(args) == 1 {
		runtimes = args
	}
	reports := make([]R, 0, len(runtimes))
	for _, runtime := range runtimes {
		report, err := status(runtime)
		if err != nil {
			return err
		}
		reports = append(reports, report)
	}
	if env.json {
		return env.printJSON(map[string]any{"runtimes": reports})
	}
	rows := make([]map[string]any, 0, len(reports))
	for _, report := range reports {
		rows = append(rows, row(report))
	}
	env.print("%s", rowOutput(columns, rows))
	if len(help) > 0 {
		env.print("%s", renderHelp(help...))
	}
	return nil
}

// buildProviders turns the configuration into the main, interpretation, and
// deep-exploration cascades.
//
// Whatever it has to say travels as data inside the cascade and comes out
// through the answer and through `roca doctor`, which is where an operator
// reads it. It is not also printed to the error stream: a copy on every single
// command is noise, and noise on stderr is what makes an operator stop reading
// it.
func buildProviders(file config.File, paths config.Paths) (provider.Cascade, provider.Cascade, provider.Cascade) {
	settings := provider.Settings{
		File: file, RunnerDir: paths.Runner, Env: os.Getenv,
	}
	cascade, err := provider.BuildCascade(settings)
	if err != nil {
		// No providers, but not "turned off": the operator did not turn the
		// model off, they wrote an order this build cannot resolve, and doctor
		// has to say which of the two it is looking at.
		return provider.Cascade{Warnings: []string{err.Error()}}, provider.Cascade{}, provider.Cascade{}
	}
	interpreters, err := provider.BuildInterpretCascade(settings)
	if err != nil {
		// An interpretation order this build cannot resolve leaves the two
		// inferences together and says why. It never takes the query down.
		cascade.Warnings = append(cascade.Warnings, err.Error())
		interpreters = provider.Cascade{}
	}
	explorers, err := provider.BuildExploreCascade(settings)
	if err != nil {
		// A deep order this build cannot resolve leaves deep investigation on
		// the interpretation/main fallback and says why.
		cascade.Warnings = append(cascade.Warnings, err.Error())
		explorers = provider.Cascade{}
	}
	// What resolving that order had to say is about the same file, so it reaches
	// the operator in the same place as the rest of the configuration.
	cascade.Warnings = append(cascade.Warnings, interpreters.Warnings...)
	cascade.Warnings = append(cascade.Warnings, explorers.Warnings...)
	interpreters.Warnings = nil
	explorers.Warnings = nil
	return cascade, interpreters, explorers
}

func (env *cliEnv) printJSON(value any) error {
	env.capture(value)
	encoder := json.NewEncoder(env.out)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func (env *cliEnv) capture(value any) { env.outcome = value }

func (env *cliEnv) print(format string, args ...any) {
	fmt.Fprintf(env.out, format+"\n", args...)
}

// correlationID names this run's log line, minted once and reused, so what the
// operator reads and what the audit record says are the same ID.
func (env *cliEnv) correlationID() string {
	if env.correlation == "" {
		env.correlation = logfile.NewCorrelationID()
	}
	return env.correlation
}

// surfaceCorrelation is what a run that failed without an error value has
// instead of the suffix Correlate writes inside an error message. It goes to the
// error stream, which is the one stream no answer is parsed from, so a --json
// envelope stays valid. It is only ever roca's own failure: a plugin owns both
// of its streams, so the plugin seam mints the ID without printing it.
func (env *cliEnv) surfaceCorrelation() {
	fmt.Fprintf(env.errOut, "correlation_id: %s\n", env.correlationID())
}
