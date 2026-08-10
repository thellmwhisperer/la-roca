/*
@overview Core CLI command assembly and human query rendering. ~380 lines, no public symbols.

	READING GUIDE
	-------------
	1. Start at queryCommand and render
	2. Read initCommand for bootstrap behavior
	3. Read the remaining command factories on demand

	MAIN FLOW
	---------
	Cobra command -> service call -> JSON or honest human rendering

	PUBLIC API
	----------
	None; command factories are package-private.

	INTERNALS
	---------
	versionCommand, initCommand, schemaCommand, queryCommand, execCommand, teachCommand, render

@exports
@deps Cobra, internal config/human/query/service/store
*/
package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spf13/cobra"
	"github.com/thellmwhisperer/la-roca/internal/distribution/axi"
	"github.com/thellmwhisperer/la-roca/internal/distribution/human"
	"github.com/thellmwhisperer/la-roca/internal/provider/config"
	"github.com/thellmwhisperer/la-roca/internal/provider/service"
	"github.com/thellmwhisperer/la-roca/internal/store"
	"golang.org/x/term"
)

// -- 1/3 HELPER · version and bootstrap commands --

func versionCommand(env *cliEnv) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Version, source SHA and platform",
		RunE: func(*cobra.Command, []string) error {
			return env.report(map[string]any{
				"version":    env.build.Version,
				"source_sha": env.build.Commit,
				"built_at":   env.build.Date,
				"platform":   runtime.GOOS + "/" + runtime.GOARCH,
			}, "%s", versionLine(env.build))
		},
	}
}

// versionLine is the one answer to "which roca is this", used by the `version`
// subcommand and by the `--version` flag alike. It starts with the product's
// name because `install.sh` reads that prefix to tell a roca binary from a file
// of the operator's that happens to share its name.
func versionLine(build Build) string {
	return fmt.Sprintf("roca %s (%s) %s/%s",
		build.Version, build.Commit, runtime.GOOS, runtime.GOARCH)
}

func initCommand(env *cliEnv) *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Choose a new database or import one by path, then bootstrap it",
		Long: "Creates and bootstraps the database. With no home database, init asks new or adopt;\n" +
			"adopt then asks for the source path and copies it, leaving the original untouched.\n" +
			"An existing home database is kept or reinitialized only by explicit answer.\n" +
			"Non-interactive callers must select a location explicitly with --db-path.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			paths, err := env.resolvePaths()
			if err != nil {
				return err
			}
			choice, source, err := env.selectInitDatabase(cmd.InOrStdin(), paths, env.dbPath != "")
			if err != nil {
				return err
			}
			if !env.json {
				env.print("data directory: %s", dirOf(paths.DB))
				env.print("configuration: %s", paths.Config)
			}

			if choice == "reinitialize" {
				for _, suffix := range []string{"", "-wal", "-shm"} {
					if err := os.Remove(paths.DB + suffix); err != nil && !os.IsNotExist(err) {
						return fmt.Errorf("replace the existing database %s: %w", paths.DB, err)
					}
				}
			}
			if err := os.MkdirAll(dirOf(paths.DB), 0o700); err != nil {
				return fmt.Errorf("create the database directory: %w", err)
			}

			adoptedByCopy := false
			if choice == "adopt" {
				if err := store.CopyDatabase(cmd.Context(), source, paths.DB); err != nil {
					return err
				}
				adoptedByCopy = true
			}

			svc, err := env.openServiceWith(paths)
			if err != nil {
				return err
			}
			defer svc.Close()

			result, err := svc.Init(cmd.Context())
			if err != nil {
				return err
			}
			if env.json {
				return env.printJSON(struct {
					service.InitResult
					Version       string `json:"version"`
					SourceSHA     string `json:"source_sha"`
					AdoptedByCopy bool   `json:"adopted_by_copy,omitempty"`
					AdoptedFrom   string `json:"adopted_from,omitempty"`
				}{result, env.build.Version, env.build.Commit, adoptedByCopy, source})
			}

			switch {
			case adoptedByCopy:
				env.print("database outcome: adopted by copy; %s -> %s; original untouched",
					source, paths.DB)
			case choice == "new":
				env.print("database outcome: created a fresh database at %s", paths.DB)
			case choice == "reinitialize":
				env.print("database outcome: reinitialized a fresh database at %s", paths.DB)
			default:
				env.print("database outcome: kept the existing home database at %s", paths.DB)
			}
			if result.BackupPath != "" {
				env.print("backup verified beforehand at %s", result.BackupPath)
			}
			if result.Database == "created" {
				env.print("schema: %d required structures created", result.Structures)
			} else if len(result.Repairs) > 0 {
				env.print("schema: %d required structures verified; repairs applied (%d): %s",
					result.Structures, len(result.Repairs), strings.Join(result.Repairs, "; "))
			} else {
				env.print("schema: %d required structures verified", result.Structures)
			}
			if len(result.Orphans) > 0 {
				env.print("tables outside v1, kept intact: %s",
					strings.Join(result.Orphans, ", "))
				env.print("archive them when you want to, with: roca schema archive-orphans --yes")
			}
			env.print("layers synced: %d", result.Layers)
			renderBootstrap(env, result)
			return nil
		},
	}
}

var terminalInput = func(in any) bool {
	file, ok := in.(interface{ Fd() uintptr })
	return ok && term.IsTerminal(int(file.Fd()))
}

func (env *cliEnv) selectInitDatabase(in io.Reader, paths config.Paths, explicit bool) (string, string, error) {
	exists := fileExists(paths.DB)
	if explicit {
		if exists {
			return "keep", "", nil
		}
		return "new", "", nil
	}
	if exists {
		info, _ := os.Stat(paths.DB)
		env.initSay("database at %s · %d bytes", paths.DB, info.Size())
		env.initSay("keep: use the current database here, then index the agent history found on this machine")
		env.initSay("reinitialize: permanently replace the current database with an empty one, then index the agent history found on this machine")
		if !terminalInput(in) {
			return "", "", fmt.Errorf("roca init needs an interactive keep or reinitialize answer; run it in a terminal, or pass --db-path explicitly")
		}
		choice, err := readInitAnswer(bufio.NewReader(in), env.errOut,
			"Choose database [keep/reinitialize] (no default): ", "keep", "reinitialize")
		return choice, "", err
	}

	env.initSay("no database at %s", paths.DB)
	env.initSay("new: create an empty database here, then index the agent history found on this machine")
	env.initSay("adopt: if you already have a La Roca database elsewhere, type its path and a copy is brought here; the original is never touched")
	if !terminalInput(in) {
		return "", "", fmt.Errorf("roca init needs an interactive new or adopt answer; run it in a terminal, or pass --db-path explicitly")
	}
	reader := bufio.NewReader(in)
	choice, err := readInitAnswer(reader, env.errOut,
		"Choose database [new/adopt] (no default): ", "new", "adopt")
	if err != nil || choice == "new" {
		return choice, "", err
	}
	fmt.Fprint(env.errOut, "Path to the database to adopt: ")
	raw, readErr := reader.ReadString('\n')
	if readErr != nil && strings.TrimSpace(raw) == "" {
		return "", "", fmt.Errorf("roca init received no database path")
	}
	source, err := filepath.Abs(strings.TrimSpace(raw))
	if err != nil {
		return "", "", fmt.Errorf("resolve the database path: %w", err)
	}
	info, err := os.Stat(source)
	if err != nil || info.IsDir() {
		return "", "", fmt.Errorf("the database to adopt is not a readable file at %s", source)
	}
	if source == paths.DB {
		return "", "", fmt.Errorf("the adoption source and destination are the same file: %s", source)
	}
	env.initSay("database to adopt: %s · %d bytes", source, info.Size())
	env.initSay("summary: existing SQLite database; row counts will be verified after the copy")
	env.initSay("adopting copies it to %s; the original stays untouched", paths.DB)
	return "adopt", source, nil
}

func readInitAnswer(reader *bufio.Reader, out io.Writer, prompt string, allowed ...string) (string, error) {
	fmt.Fprint(out, prompt)
	line, err := reader.ReadString('\n')
	answer := strings.ToLower(strings.TrimSpace(line))
	if err != nil && answer == "" {
		return "", fmt.Errorf("roca init received no answer")
	}
	for _, candidate := range allowed {
		if answer == candidate {
			return answer, nil
		}
	}
	return "", fmt.Errorf("database choice %q is not valid; answer %s", answer, strings.Join(allowed, " or "))
}

func (env *cliEnv) initSay(format string, args ...any) {
	fmt.Fprintf(env.errOut, format+"\n", args...)
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// renderBootstrap is the rest of what init did: what the first read of the disk
// found, which model is going to answer, and whether this installation has a
// bench to measure itself against. None of the three can fail the command, so
// all three have to be readable.
func renderBootstrap(env *cliEnv, result service.InitResult) {
	if result.Ingest != nil {
		env.print("agents detected: %s", detectedAgentsLine(result.Ingest.DetectedAgents))
		env.print("agents not found: %s", missingAgentsLine(result.Ingest.DetectedAgents))
		env.print("ingest: %d files read, %d skipped, %d errors",
			result.Ingest.FilesRead, result.Ingest.FilesSkipped, result.Ingest.Errors)
	}
	env.print("rows: memories=%d sessions=%d exchanges=%d thinking_blocks=%d tool_uses=%d",
		result.Rows.Memories, result.Rows.Sessions, result.Rows.Exchanges,
		result.Rows.ThinkingBlocks, result.Rows.ToolUses)
	if result.Search != nil {
		env.print("index: full-text index ready · %s",
			human.Duration(result.Search.ElapsedMS))
	}
	if model := result.Model; model != nil {
		switch {
		case model.Disabled:
			env.print("model: turned off in the configuration")
		case model.Ready:
			env.print("%s", modelChoiceLine(model.Provider, "ready", model.Model, result.ConfigPath))
		default:
			env.print("model: none available (%s)", model.Reason)
			if model.Action != "" {
				env.print("  remedy: %s", model.Action)
			}
		}
	}
	env.print("skill: available via `roca skill install` (not installed automatically)")
	if result.PromptPath != "" {
		env.print("agent prompt: %s (paste this block into agent instructions)", result.PromptPath)
		env.print("%s", strings.TrimSuffix(result.Prompt, "\n"))
	}
}

func detectedAgentsLine(agents []string) string {
	if len(agents) == 0 {
		return "none"
	}
	return strings.Join(agents, ", ")
}

func missingAgentsLine(detected []string) string {
	present := make(map[string]bool, len(detected))
	for _, name := range detected {
		present[name] = true
	}
	var missing []string
	for _, name := range []string{"claude", "claude-desktop", "cowork", "codex", "opencode", "pi", "hermes"} {
		if !present[name] {
			missing = append(missing, name)
		}
	}
	return detectedAgentsLine(missing)
}

// -/ 1/3

// -- 2/3 CORE · query and data commands -- <- START HERE

func schemaCommand(env *cliEnv) *cobra.Command {
	schema := &cobra.Command{
		Use:   "schema",
		Short: "State of the database schema",
	}
	schema.AddCommand(&cobra.Command{
		Use:   "status",
		Short: "Classify the database by structure without touching it",
		RunE: env.serviceRunE(func(cmd *cobra.Command, _ []string, svc *service.Service) error {
			report, err := svc.SchemaStatus(cmd.Context())
			if err != nil {
				return err
			}
			if env.json {
				return env.printJSON(report)
			}
			env.print("verdict: %s", report.Verdict)
			env.print("reason: %s", report.Reason)
			for _, d := range report.Differences {
				env.print("  difference: %s", d.Detail)
			}
			if len(report.Orphans) > 0 {
				env.print("orphans (reported, not blocking): %s",
					strings.Join(report.Orphans, ", "))
			}
			return nil
		}),
	})
	return schema
}

func queryCommand(env *cliEnv) *cobra.Command {
	var req service.QueryRequest
	cmd := &cobra.Command{
		Use:   "query <question>",
		Short: "Answer a natural-language question about the memory",
		Args:  cobra.MinimumNArgs(1),
		RunE: env.serviceRunE(func(cmd *cobra.Command, args []string, svc *service.Service) error {
			req.Question = strings.Join(args, " ")
			// The query may round-trip a model, and a model takes long enough to read
			// as frozen. The spinner says it is running on the error stream of an
			// interactive terminal only, so a piped call and a --json call see nothing.
			spin := startSpinner(env, spinnerLabel)
			result, err := svc.Query(cmd.Context(), req)
			spin.finish()
			if err != nil {
				return err
			}
			// A question that needed a model on a machine with no model
			// available is not an answer, even when the keyword rescue found
			// rows. The rows are a courtesy; the exit code tells the truth, so
			// a script does not read "it worked" from a machine that has
			// nothing to answer with (F07-04).
			if result.Degraded == service.DegradedUnavailable {
				env.code = ExitError
			}
			if env.json {
				return env.printJSON(result)
			}
			// The second inference call: the rows the query returned go back to the
			// same provider that answered, to be rendered as prose in the question's
			// language. It is attempted only when a model served (res.Engine is set),
			// so a machine with no model is not probed again for nothing; and a
			// failure leaves the row renderer as the floor, because the fragility of
			// a provider never takes a query's answer away.
			var prose string
			if result.Engine != "" && result.RowCount > 0 {
				if answer, err := svc.Interpret(cmd.Context(), result.Question, result.Columns, result.Rows); err == nil {
					prose = answer
				} else {
					env.print("(the model could not interpret: %v)", err)
				}
			}
			render(env, result, prose)
			return nil
		}),
	}
	cmd.Flags().StringVar(&req.Layer, "layer", "", "restrict the answer to one layer")
	cmd.Flags().IntVar(&req.MaxChars, "max-chars", 0, "character budget per text field")
	cmd.Flags().BoolVar(&req.SQLOnly, "sql-only", false, "return the SQL without running it")
	return cmd
}

func execCommand(env *cliEnv) *cobra.Command {
	var req service.ExecRequest
	cmd := &cobra.Command{
		Use:   "exec <SELECT>",
		Short: "Run a SELECT under the read-only gate",
		Long: "Natural companion of `query --sql-only`: runs the SQL that command\n" +
			"printed, under the same gate. What does not pass the gate does not touch the database.",
		Args: cobra.MinimumNArgs(1),
		RunE: env.serviceRunE(func(cmd *cobra.Command, args []string, svc *service.Service) error {
			req.SQL = strings.Join(args, " ")
			result, err := svc.Exec(cmd.Context(), req)
			if err != nil {
				return err
			}
			if env.json {
				return env.printJSON(result)
			}
			env.print("%s", axi.Exec(result))
			return nil
		}),
	}
	cmd.Flags().IntVar(&req.MaxChars, "max-chars", 0, "character budget per text field")
	return cmd
}

// -/ 2/3

// -- 3/3 CORE · render --

// render is the readable output. The same answer --json hands over whole,
// summarized here for a human at a terminal.
//
// prose is the model's natural-language rendering of the rows, when the
// second inference call answered. Empty means it did not (no model served, or
// the call failed), and the rows fall back to their tabular form: a table is
// the floor the prose sits on, never an answer the prose hides.
func render(env *cliEnv, res service.QueryResult, prose string) {
	// The AXI text — route preamble, the rows or the prose that stands in for
	// them, and the contextual help — has one owner in the axi package. The
	// shell's only part here is the second inference call's prose: empty when no
	// model served or the call failed, in which case axi.Query falls to the rows.
	env.print("%s", axi.Query(res, prose))
}

func dirOf(path string) string { return filepath.Dir(path) }
