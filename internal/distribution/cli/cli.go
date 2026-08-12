package cli

import (
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
	if !env.prelogged {
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
		versionCommand(env), initCommand(env), queryCommand(env),
		execCommand(env), schemaCommand(env),
		indexCommand(env), doctorCommand(env),
		ingestCommand(env), storeCommand(env), healthCommand(env),
		mcpCommand(env), skillCommand(env), hooksCommand(env),
		loginCommand(env), modelCommand(env),
		updateCommand(env), uninstallCommand(env),
		modelsCommand(env), pluginsCommand(env),
		capabilitiesCommand(env),
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
	case "init", "query", "store", "ingest", "login", "doctor", "update", "uninstall", "plugins", "hooks":
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

// resolvePaths decides where everything of this installation lives, without
// touching the database. Commands that only need a path (such as login) pay
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
	var ingestProgress func(ingest.SourceProgress)
	if env.wantIngestProgress && !env.json && termAware(env.errOut) {
		env.liveIngest = newIngestRows(env.errOut, true)
		ingestProgress = env.liveIngest.update
	}
	providers, interpreters := buildProviders(file, paths)
	svc, err := service.Open(service.Options{
		DBPath:       paths.DB,
		BackupDir:    paths.Backups,
		DataDir:      filepath.Dir(paths.DB),
		Version:      env.build.Version,
		Commit:       env.build.Commit,
		QueryTimeout: time.Duration(file.Query.TimeoutMS) * time.Millisecond,
		Providers:    providers,
		Interpreters: interpreters,
		ConfigPath:   paths.Config,
		ConfigExists: file.Exists,
		Sources:      ingestSources(file, home, paths.Runner),
		ReadOnly:     config.ReadOnly(os.Getenv(config.EnvReadOnly)),
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
// The commands that also need the resolved paths (init, and the login pair that
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

// buildProviders turns the configuration into the two model cascades: the one
// that answers questions, and the one the result rows are handed to when the
// operator declared an interpretation order of its own.
//
// Whatever it has to say travels as data inside the cascade and comes out
// through the answer and through `roca doctor`, which is where an operator
// reads it. It is not also printed to the error stream: a copy on every single
// command is noise, and noise on stderr is what makes an operator stop reading
// it.
func buildProviders(file config.File, paths config.Paths) (provider.Cascade, provider.Cascade) {
	settings := provider.Settings{
		File: file, RunnerDir: paths.Runner, Env: os.Getenv,
	}
	cascade, err := provider.BuildCascade(settings)
	if err != nil {
		// No providers, but not "turned off": the operator did not turn the
		// model off, they wrote an order this build cannot resolve, and doctor
		// has to say which of the two it is looking at.
		return provider.Cascade{Warnings: []string{err.Error()}}, provider.Cascade{}
	}
	interpreters, err := provider.BuildInterpretCascade(settings)
	if err != nil {
		// An interpretation order this build cannot resolve leaves the two
		// inferences together and says why. It never takes the query down.
		cascade.Warnings = append(cascade.Warnings, err.Error())
		return cascade, provider.Cascade{}
	}
	// What resolving that order had to say is about the same file, so it reaches
	// the operator in the same place as the rest of the configuration.
	cascade.Warnings = append(cascade.Warnings, interpreters.Warnings...)
	interpreters.Warnings = nil
	return cascade, interpreters
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

// printCorrelation names the log line of a run that failed without an error to
// print. The shell's degraded answer is one: it exits non-zero and says why,
// and an operator reading that has nothing else to match the audit record with.
func (env *cliEnv) printCorrelation() {
	if env.correlation != "" {
		env.print("correlation_id: %s", env.correlation)
	}
}
