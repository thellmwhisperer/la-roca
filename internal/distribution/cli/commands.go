package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/thellmwhisperer/la-roca/internal/distribution/axi"
	"github.com/thellmwhisperer/la-roca/internal/ingest"
	"github.com/thellmwhisperer/la-roca/internal/provider/config"
	"github.com/thellmwhisperer/la-roca/internal/provider/service"
	"github.com/thellmwhisperer/la-roca/internal/store"
	"golang.org/x/term"
)

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
		Short: "Choose a database and answering model, then bootstrap them",
		Long: "Creates and bootstraps the database. With no home database, init asks new or adopt;\n" +
			"adopt then asks for the source path and copies it, leaving the original untouched.\n" +
			"An existing home database is kept or reinitialized only by explicit answer.\n" +
			"In a terminal, a model-first chooser lists detected CLI defaults and pulled Ollama models,\n" +
			"resolves the harness, confirms the pair, and writes it with a recovery backup.\n" +
			"Non-interactive callers must select a location explicitly with --db-path; they are never prompted.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			commandStarted := time.Now()
			env.wantIngestProgress = true
			paths, err := env.resolvePaths()
			if err != nil {
				return err
			}
			rawInput := cmd.InOrStdin()
			interactive := terminalInput(rawInput) && !env.json
			input := bufio.NewReader(rawInput)
			choice, source, err := env.selectInitDatabase(input, paths, env.dbPath != "", interactive)
			if err != nil {
				return err
			}
			if choice == "reinitialize" {
				for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
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
			defer env.finishIngestProgress()

			result, err := svc.Init(cmd.Context())
			env.finishIngestProgress()
			if err != nil {
				return err
			}
			commandElapsed := time.Since(commandStarted).Milliseconds()
			if outsideService := commandElapsed - result.TotalElapsedMS; outsideService > 0 {
				result.SetupElapsedMS += outsideService
			}
			result.TotalElapsedMS = commandElapsed
			if env.json {
				return env.printJSON(struct {
					service.InitResult
					Version       string `json:"version"`
					SourceSHA     string `json:"source_sha"`
					AdoptedByCopy bool   `json:"adopted_by_copy,omitempty"`
					AdoptedFrom   string `json:"adopted_from,omitempty"`
				}{result, env.build.Version, env.build.Commit, adoptedByCopy, source})
			}
			if interactive && !env.skipInitChooser {
				result, err = env.chooseInitModel(cmd.Context(), input, paths, result)
				if err != nil {
					return err
				}
			}

			env.print("setup: %s", axi.Duration(result.SetupElapsedMS))
			env.print("  data directory: %s", dirOf(paths.DB))
			env.print("  configuration: %s", paths.Config)
			env.print("  agents: checking known sources")
			env.print("  database: inspecting %s", paths.DB)
			switch {
			case adoptedByCopy:
				env.print("  database outcome: adopted by copy; %s -> %s; original untouched",
					source, paths.DB)
			case choice == "new":
				env.print("  database outcome: created a fresh database at %s", paths.DB)
			case choice == "reinitialize":
				env.print("  database outcome: reinitialized a fresh database at %s", paths.DB)
			default:
				env.print("  database outcome: kept the existing home database at %s", paths.DB)
			}
			if result.BackupPath != "" {
				env.print("  backup verified beforehand at %s", result.BackupPath)
			}
			if result.Database == "created" {
				env.print("  schema: %s required structures created", axi.Number(int64(result.Structures)))
			} else if len(result.Repairs) > 0 {
				env.print("  schema: %s required structures verified; repairs applied (%s): %s",
					axi.Number(int64(result.Structures)), axi.Number(int64(len(result.Repairs))),
					strings.Join(result.Repairs, "; "))
			} else {
				env.print("  schema: %s required structures verified", axi.Number(int64(result.Structures)))
			}
			if len(result.Orphans) > 0 {
				env.print("  tables outside v1, kept intact: %s",
					strings.Join(result.Orphans, ", "))
			}
			env.print("  layers synced: %s", axi.Number(int64(result.Layers)))
			env.print("  rows: memories=%s sessions=%s exchanges=%s thinking_blocks=%s tool_uses=%s",
				axi.Number(int64(result.RowsBefore.Memories)), axi.Number(int64(result.RowsBefore.Sessions)),
				axi.Number(int64(result.RowsBefore.Exchanges)), axi.Number(int64(result.RowsBefore.ThinkingBlocks)),
				axi.Number(int64(result.RowsBefore.ToolUses)))
			renderBootstrap(env, result)
			return nil
		},
	}
}

var terminalInput = func(in any) bool {
	file, ok := in.(interface{ Fd() uintptr })
	return ok && term.IsTerminal(int(file.Fd()))
}

func (env *cliEnv) selectInitDatabase(reader *bufio.Reader, paths config.Paths,
	explicit, interactive bool) (string, string, error) {
	exists := fileExists(paths.DB)
	if explicit {
		if exists {
			return "keep", "", nil
		}
		return "new", "", nil
	}
	if exists {
		// The file was there a moment ago. If it has gone since, that is a live
		// disk and not a crash: the size is what goes unreported, nothing else.
		if info, err := os.Stat(paths.DB); err == nil {
			env.initSay("database at %s · %d bytes", paths.DB, info.Size())
		} else {
			env.initSay("database at %s", paths.DB)
		}
		env.initSay("keep: use the current database here, then index the agent history found on this machine")
		env.initSay("reinitialize: permanently replace the current database with an empty one, then index the agent history found on this machine")
		if !interactive {
			return "", "", fmt.Errorf("roca init needs an interactive keep or reinitialize answer; run it in a terminal, or pass --db-path explicitly")
		}
		choice, err := readInitAnswer(reader, env.errOut,
			"Choose database [keep/reinitialize] (no default): ", "keep", "reinitialize")
		return choice, "", err
	}

	env.initSay("no database at %s", paths.DB)
	env.initSay("new: create an empty database here, then index the agent history found on this machine")
	env.initSay("adopt: if you already have a La Roca database elsewhere, type its path and a copy is brought here; the original is never touched")
	if !interactive {
		return "", "", fmt.Errorf("roca init needs an interactive new or adopt answer; run it in a terminal, or pass --db-path explicitly")
	}
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
// found and which model is going to answer. Neither phase can fail the command,
// so both have to be readable.
func renderBootstrap(env *cliEnv, result service.InitResult) {
	for _, warning := range result.Warnings {
		env.print("warning: %s", warning)
	}
	renderModelDetection(env, result.DetectedModelBinaries, result.MissingModelBinaries,
		result.FactoryDefault, result.FactoryDefaultProvider)
	if result.Ingest != nil {
		env.print("  agents detected: %s", detectedAgentsLine(result.Ingest.DetectedAgents))
		env.print("  agents not found: %s", missingAgentsLine(result.Ingest.DetectedAgents))
		env.print("ingest: %s read · %s skipped · %s · %s",
			axi.Quantity(int64(result.Ingest.FilesRead), "file"), axi.Number(int64(result.Ingest.FilesSkipped)),
			axi.Quantity(int64(result.Ingest.Errors), "error"), axi.Duration(result.Ingest.ElapsedMS))
		renderIngestSources(env, *result.Ingest)
		renderIngestDelta(env, result.Ingest.Delta)
		renderIngestDetails(env, *result.Ingest)
	}
	if result.Search != nil {
		env.print("index: full-text index ready · %s",
			axi.Duration(result.Search.ElapsedMS))
	}
	if model := result.Model; model != nil {
		switch {
		case model.Disabled:
			env.print("model: turned off in the configuration · %s",
				axi.Duration(result.ModelElapsedMS))
		case model.Ready:
			env.print("model: ready · %s", axi.Duration(result.ModelElapsedMS))
		default:
			env.print("model: none available (%s) · %s", model.Reason,
				axi.Duration(result.ModelElapsedMS))
			if model.Action != "" {
				env.print("  remedy: %s", model.Action)
			}
		}
	}
	renderBedrock(env, result.Bedrock)
	env.print("total: %s", axi.Duration(result.TotalElapsedMS))
	env.print("next steps:")
	if result.Model != nil && result.Model.Ready {
		env.print("  query: `roca query \"what did we decide last time\"`")
	}
	if result.PromptPath != "" {
		env.print("  agent prompt: %s", result.PromptPath)
		env.print("  Paste its contents into the agent instructions you choose.")
	}
	env.print("  skill: available via `roca skill install` (not installed automatically)")
	renderInitAnswer(env, result)
}

func renderInitAnswer(env *cliEnv, result service.InitResult) {
	model := result.Model
	if model == nil || !model.Ready {
		env.print("answering: none · configuration: %s · change with: models.order and models.<provider>.model in that file",
			result.ConfigPath)
		return
	}
	line := fmt.Sprintf("answering: %s/%s (%s) · configuration: %s · change with: %s",
		model.Provider, model.Model, modelChoiceSource(result.ConfigPath, model.Provider, model.Model),
		result.ConfigPath, initModelChange(model.Provider, result.ConfigPath))
	if model.ExternalCredential {
		line += " · uses the existing local CLI session; no roca login required"
	}
	env.print("%s", line)
}

func initModelChange(name, path string) string {
	return fmt.Sprintf("roca model set <id> or models.%s.model in %s", name, path)
}

func renderBedrock(env *cliEnv, bedrock *service.Bedrock) {
	if bedrock == nil {
		env.print("bedrock: your memory has no history yet")
		return
	}
	stamp, err := time.Parse(time.RFC3339, bedrock.Timestamp)
	if err != nil {
		stamp, err = time.Parse("2006-01-02 15:04:05", bedrock.Timestamp)
	}
	date := bedrock.Timestamp
	if err == nil {
		date = stamp.Format("02 Jan 2006")
	}
	if bedrock.Project != "" {
		env.print("bedrock: your memory reaches back to %s (first session: %s)", date, bedrock.Project)
		return
	}
	env.print("bedrock: your memory reaches back to %s", date)
}

func detectedAgentsLine(agents []string) string {
	if len(agents) == 0 {
		return "none"
	}
	return strings.Join(agents, ", ")
}

func missingAgentsLine(detected []string) string {
	return detectedAgentsLine(ingest.MissingAgentFamilies(detected))
}

func schemaCommand(env *cliEnv) *cobra.Command {
	schema := &cobra.Command{
		Use:   "schema",
		Short: "State of the database schema",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
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

func indexCommand(env *cliEnv) *cobra.Command {
	return &cobra.Command{
		Use:   "index",
		Short: "Build or refresh the search index",
		Long: "Builds the full-text index.\n" +
			"It is incremental: on an already indexed database it costs nothing, and\n" +
			"`roca init` calls it on its own. Run it by hand to pick up memory that\n" +
			"another process wrote.",
		RunE: env.serviceRunE(func(cmd *cobra.Command, _ []string, svc *service.Service) error {
			report, err := svc.Index(cmd.Context())
			if err != nil {
				return err
			}
			if env.json {
				return env.printJSON(report)
			}
			if report.LexicalBuilt {
				env.print("full-text index built")
			}
			env.print("%s", axi.Duration(report.ElapsedMS))
			return nil
		}),
	}
}

func queryCommand(env *cliEnv) *cobra.Command {
	var req service.QueryRequest
	var full bool
	cmd := &cobra.Command{
		Use:   "query <question>",
		Short: "Answer a natural-language question about the memory",
		Long:  "Data: query; human reading: query --full; raw SQL: exec.",
		Args:  cobra.MinimumNArgs(1),
		RunE: env.serviceRunE(func(cmd *cobra.Command, args []string, svc *service.Service) error {
			req.Question = strings.Join(args, " ")
			// The query may round-trip a model, and a model takes long enough to read
			// as frozen. The spinner says it is running on the error stream of an
			// interactive terminal only, so a piped call and a --json call see nothing.
			spin := startSpinner(env, spinnerShaping)
			live := newLiveInterpretation(env, spin, full, svc.DB().Path())
			req.Progress = func(phase service.QueryPhase) {
				switch phase {
				case service.QueryPhaseExecution:
					spin.phase(spinnerSearching)
				case service.QueryPhaseInterpretation:
					spin.phase(spinnerComposing)
				default:
					spin.phase(spinnerShaping)
				}
			}
			req.InterpretationStart = live.start
			req.InterpretationDelta = live.append
			answer, err := answerQuery(cmd.Context(), svc, req, full)
			spin.finish()
			if err != nil {
				return err
			}
			result := answer.result
			env.capture(result)
			// A question that needed a model on a machine with no model
			// available is not an answer, even when the keyword rescue found
			// rows. The rows are a courtesy; the exit code tells the truth, so
			// a script does not read "it worked" from a machine that has
			// nothing to answer with.
			if service.IsDegradedFailure(result.Degraded) {
				env.code = ExitError
			}
			if env.json {
				return env.printJSON(struct {
					service.QueryResult
					DatabasePath string `json:"database_path"`
				}{result, svc.DB().Path()})
			}
			if live.finish(answer) {
				return nil
			}
			env.print("database: %s", svc.DB().Path())
			if answer.interpretErr != nil {
				env.print("%s", interpretationFallback(answer.interpretErr))
			}
			answer.prose = formatInterpretation(answer.prose, termAware(env.out),
				terminalWidth(env.out), colorOn(env.out))
			env.print("%s", axiQuery(answer))
			return nil
		}),
	}
	cmd.Flags().StringVar(&req.Layer, "layer", "", "restrict the answer to one layer")
	cmd.Flags().IntVar(&req.MaxChars, "max-chars", service.DefaultMaxChars, "character budget per text field")
	cmd.Flags().BoolVar(&req.SQLOnly, "sql-only", false, "return the SQL without running it")
	cmd.Flags().BoolVar(&full, "full", false, "add a prose interpretation for human reading")
	return cmd
}

// queryAnswer keeps the rows and the optional second inference together. The
// interpretation error is presentation context, not a query failure: callers
// still render the first inference's evidence when the second one fails.
type queryAnswer struct {
	result       service.QueryResult
	prose        string
	interpretErr error
}

// answerQuery always performs the natural-language-to-SQL query once. Full
// mode adds one interpretation call only when that query returned model-backed
// rows; default mode stops at the data, matching the MCP surface.
func answerQuery(ctx context.Context, svc *service.Service, req service.QueryRequest,
	full bool) (queryAnswer, error) {
	result, err := svc.Query(ctx, req)
	answer := queryAnswer{result: result}
	if err != nil || !full || result.Engine == "" || result.RowCount == 0 {
		return answer, err
	}
	started := time.Now()
	if req.Progress != nil {
		req.Progress(service.QueryPhaseInterpretation)
	}
	var onStart func(bool)
	if req.InterpretationStart != nil {
		onStart = func(native bool) { req.InterpretationStart(native, result) }
	}
	interpretation, err := svc.InterpretStream(
		ctx, result.Question, result.Columns, result.Rows,
		time.Duration(result.SQLInferenceMS)*time.Millisecond,
		result.Engine, onStart, req.InterpretationDelta)
	answer.prose, answer.interpretErr = interpretation.Text, err
	answer.result.InterpretationMS = time.Since(started).Milliseconds()
	answer.result.LatencyMS += answer.result.InterpretationMS
	answer.result.Interpretation = interpretation.Text
	// Who read the rows travels in the envelope beside who wrote the SQL: on an
	// installation that splits the two inferences they are different providers,
	// and that difference is the whole point of splitting them.
	answer.result.InterpretEngine = interpretation.Engine
	answer.result.InterpretModel = interpretation.Model
	answer.result.InterpretNote = interpretation.Note
	if answer.interpretErr != nil {
		answer.result.ProviderError = answer.interpretErr.Error()
	}
	return answer, nil
}

func interpretationFallback(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "summary timed out; showing rows instead."
	}
	return "summary unavailable; showing rows instead."
}

func axiQuery(answer queryAnswer) string {
	return axi.Query(answer.result, answer.prose)
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
			env.capture(result)
			if env.json {
				return env.printJSON(result)
			}
			env.print("%s", axi.Exec(result))
			return nil
		}),
	}
	cmd.Flags().IntVar(&req.MaxChars, "max-chars", service.DefaultMaxChars, "character budget per text field")
	return cmd
}

// render is the readable output. The same answer --json hands over whole,
// summarized here for a human at a terminal.
//
// prose is the model's natural-language rendering of the rows, when full mode's
// second inference call answered. Empty means default row mode or a failed call.
func render(env *cliEnv, res service.QueryResult, prose string) {
	// The AXI text — route preamble, optional prose, rows and contextual help —
	// has one owner in the axi package.
	env.print("%s", axi.Query(res, prose))
}

func dirOf(path string) string { return filepath.Dir(path) }
