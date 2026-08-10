// Package cli is La Roca's primary surface. Commands parse, call the service,
// and render; the MCP plug reaches the same kernel.
package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
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
	ExitOK      = 0
	ExitError   = 1
	ExitRefused = 2
)

type cliEnv struct {
	build              Build
	out                io.Writer
	errOut             io.Writer
	dbPath             string
	json               bool
	code               int
	outcome            any
	started            time.Time
	prelogged          bool
	openedDir          string
	liveIngest         *ingestRows
	wantIngestProgress bool
	ingestStarted      time.Time
}

// Execute runs the CLI and returns the process exit code.
func Execute(build Build) (int, error) {
	return execute(build, os.Stdout, os.Stderr, nil)
}

// execute is Execute over writers and an argument list a test can supply. A nil
// args leaves cobra reading the process arguments, which is the production path.
func execute(build Build, out, errOut io.Writer, args []string) (int, error) {
	started := time.Now()
	env := &cliEnv{build: build, out: out, errOut: errOut, started: started}
	root := rootCommand(env)
	if args != nil {
		root.SetArgs(args)
	}
	executed, err := root.ExecuteC()
	if err != nil {
		if strings.Contains(err.Error(), "unknown command") {
			err = fmt.Errorf("%w; run `roca --help` to list commands", err)
		}
	}
	code := env.code
	if err != nil {
		code = ExitError
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
			"about it. Nothing leaves this machine: the database and its search index\n" +
			"stay beside the binary on the operator's computer.",
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
		mcpCommand(env), skillCommand(env),
		loginCommand(env), logoutCommand(env), modelCommand(env),
		updateCommand(env), uninstallCommand(env),
		modelsCommand(env),
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
	case "init", "query", "store", "ingest", "login", "doctor", "update", "uninstall":
		return true
	default:
		return false
	}
}

// resolvePaths decides where everything of this installation lives, without
// touching the database. Commands that only need a path (a login, a logout) pay
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
		return nil, paths, fmt.Errorf(
			"no Roca database exists at %s; run `roca init` before this command", paths.DB)
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
	svc, err := service.Open(service.Options{
		DBPath:         paths.DB,
		BackupDir:      paths.Backups,
		DataDir:        filepath.Dir(paths.DB),
		Version:        env.build.Version,
		Commit:         env.build.Commit,
		Providers:      buildProviders(file, paths),
		ConfigPath:     paths.Config,
		ConfigExists:   file.Exists,
		Sources:        ingestSources(file, home),
		ReadOnly:       config.ReadOnly(os.Getenv(config.EnvReadOnly)),
		IngestProgress: ingestProgress,
	})
	if err != nil {
		env.finishIngestProgress()
		return nil, err
	}
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

// buildProviders turns the configuration into the model cascade.
//
// Whatever it has to say travels as data inside the cascade and comes out
// through the answer and through `roca doctor`, which is where an operator
// reads it. It is not also printed to the error stream: a copy on every single
// command is noise, and noise on stderr is what makes an operator stop reading
// it.
func buildProviders(file config.File, paths config.Paths) provider.Cascade {
	cascade, err := provider.BuildCascade(provider.Settings{
		File:        file,
		Credentials: paths.Credentials,
		Env:         os.Getenv,
	})
	if err != nil {
		// No providers, but not "turned off": the operator did not turn the
		// model off, they wrote an order this build cannot resolve, and doctor
		// has to say which of the two it is looking at.
		return provider.Cascade{Warnings: []string{err.Error()}}
	}
	return cascade
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
