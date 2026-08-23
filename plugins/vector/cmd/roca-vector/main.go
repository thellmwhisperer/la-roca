package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/thellmwhisperer/la-roca-vector/internal/engine"
	"github.com/thellmwhisperer/la-roca-vector/internal/telemetry"
	"github.com/thellmwhisperer/la-roca-vector/internal/vector"
)

var (
	version           = "dev"
	commit            = "none"
	date              = "unknown"
	launchWorker      = vector.Launch
	currentExecutable = os.Executable
	newEmbedder       = defaultEmbedder
)

type environment struct {
	json     bool
	dbPath   string
	stateDir string
}

func main() {
	env := &environment{}
	root := rootCommand(env)
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func rootCommand(env *environment) *cobra.Command {
	root := &cobra.Command{
		Use:           "roca-vector",
		Short:         "Optional local vector search for La Roca",
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       fmt.Sprintf("%s (commit %s, built %s)", version, commit, date),
		Args:          cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			return command.Help()
		},
	}
	root.PersistentFlags().BoolVar(&env.json, "json", false, "JSON output")
	root.PersistentFlags().StringVar(&env.dbPath, "db-path", env.dbPath, "La Roca database selected by the core CLI")
	root.PersistentFlags().StringVar(&env.stateDir, "state-dir", env.stateDir, "plugin state directory")
	_ = root.PersistentFlags().MarkHidden("state-dir")
	root.AddCommand(installCommand(env), ingestCommand(env), compactCommand(env),
		queryCommand(env), workerCommand(env), residentCommand(env))
	return root
}

func installCommand(env *environment) *cobra.Command {
	model := vector.DefaultModel
	command := &cobra.Command{
		Use:   "install",
		Short: "Download the embedding model and build declared sidecars in the background",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if readOnly() {
				return fmt.Errorf("vector install is disabled while ROCA_READ_ONLY is enabled")
			}
			state, err := env.resolveStateDir()
			if err != nil {
				return err
			}
			executable, err := currentExecutable()
			if err != nil {
				return fmt.Errorf("locate roca-vector: %w", err)
			}
			arguments := workerArguments(env.dbPath, state, model)
			result, err := launchWorker(vector.LaunchRequest{
				Executable: executable, Arguments: arguments, DataDir: state,
			})
			if err != nil {
				return err
			}
			if env.json {
				return printJSON(map[string]any{
					"background": true, "model": model, "pid": result.PID,
					"already_running": result.AlreadyRunning, "log_path": result.LogPath,
				})
			}
			if result.AlreadyRunning {
				fmt.Println("vector install: background indexing is already running")
			} else {
				fmt.Printf("vector install: background worker %d started\n", result.PID)
			}
			fmt.Printf("  model: %s\n", model)
			fmt.Println("  completion: a desktop notification will report exit status and counts")
			fmt.Printf("  log: %s\n", result.LogPath)
			return nil
		},
	}
	command.Flags().StringVar(&model, "model", model, "embedding model identifier")
	return command
}

func ingestCommand(env *environment) *cobra.Command {
	var delta bool
	var model string
	var source string
	var reembed bool
	command := &cobra.Command{
		Use:   "ingest --delta",
		Short: "Embed only new or changed chunks from declared databases",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if !delta {
				return fmt.Errorf("vector ingest is incremental; pass --delta")
			}
			if readOnly() {
				return fmt.Errorf("vector ingest --delta is disabled while ROCA_READ_ONLY is enabled")
			}
			state, err := env.resolveStateDir()
			if err != nil {
				return err
			}
			release, err := vector.LockStateUsage(state)
			if err != nil {
				return err
			}
			defer release()
			federation, federationErr := env.federation(model)
			federated := federationErr == nil
			if federationErr != nil && !errors.Is(federationErr, os.ErrNotExist) {
				return federationErr
			}
			vectorPath := filepath.Join(state, vector.DatabaseFilename)
			if federated {
				if !federation.HasSidecars() {
					return fmt.Errorf("vector search is not initialized; run `roca vector install`")
				}
				if model == "" {
					model = federation.ConfiguredModel()
					federation.Model = model
				}
			} else {
				if _, err := os.Stat(vectorPath); err != nil {
					if os.IsNotExist(err) {
						return fmt.Errorf("vector search is not initialized; run `roca vector install`")
					}
					return fmt.Errorf("inspect vector index: %w", err)
				}
				if model == "" {
					model = vector.ConfiguredModel(vectorPath)
				}
			}
			if err := env.calmGate().Wait(command.Context()); err != nil {
				return err
			}
			started := time.Now()
			var report vector.Delta
			var databases []vector.DatabaseDelta
			progress := func(update vector.IngestProgress) {
				if env.json {
					return
				}
				line := formatIngestProgress(update)
				fmt.Fprintf(command.ErrOrStderr(), "\r%s", line)
			}
			if federated {
				federation.Reembed = reembed
				federation.Progress = progress
				federationReport, ingestErr := federation.Ingest(command.Context(), source)
				err = ingestErr
				report, databases = federationReport.Delta, federationReport.Databases
			} else {
				index, indexErr := env.index(model)
				if indexErr != nil {
					return indexErr
				}
				index.Reembed = reembed
				index.Progress = progress
				if source == "" {
					report, err = index.Ingest(command.Context())
				} else {
					report, err = index.IngestSource(command.Context(), source)
				}
			}
			if err != nil {
				return err
			}
			if !env.json {
				fmt.Fprintln(command.ErrOrStderr())
			}
			if env.json {
				mode := "delta"
				if reembed {
					mode = "reembed"
				}
				return printJSON(map[string]any{"mode": mode, "model": model,
					"source": source, "counts": report, "databases": databases,
					"elapsed_ms": time.Since(started).Milliseconds()})
			}
			label := "vector delta"
			if reembed {
				label = "vector reembed"
			}
			if source != "" {
				label += " (" + source + ")"
			}
			fmt.Printf("%s: %d added · %d updated · %d removed · %d unchanged\n", label,
				report.Added, report.Updated, report.Removed, report.Unchanged)
			fmt.Printf("  sources: %d · chunks: %d · model: %s\n", report.Sources, report.Chunks, model)
			return nil
		},
	}
	command.Flags().BoolVar(&delta, "delta", false, "embed only new or changed chunks")
	command.Flags().BoolVar(&reembed, "reembed", false, "rebuild sidecar chunks under the current generation policy")
	command.Flags().StringVar(&model, "model", "", "embedding model identifier (default: indexed model)")
	command.Flags().StringVar(&source, "source", "", "limit the delta to one declared table")
	return command
}

func formatIngestProgress(progress vector.IngestProgress) string {
	line := fmt.Sprintf("vector ingest: %d sources · %d chunks", progress.Sources, progress.Chunks)
	if progress.Total > 0 {
		line = fmt.Sprintf("vector ingest: %d/%d sources · %d chunks",
			progress.Sources, progress.Total, progress.Chunks)
	}
	if progress.Rate > 0 {
		line += fmt.Sprintf(" · %.1f/s", progress.Rate)
	}
	if progress.ETAMS > 0 {
		line += " · ETA " + time.Duration(progress.ETAMS*int64(time.Millisecond)).Round(time.Second).String()
	}
	if progress.Range != "" {
		line += " · " + progress.Range
	}
	return line
}

func compactCommand(env *environment) *cobra.Command {
	return &cobra.Command{
		Use:   "compact",
		Short: "Repack live embeddings into dense storage pages",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if readOnly() {
				return fmt.Errorf("vector compact is disabled while ROCA_READ_ONLY is enabled")
			}
			state, err := env.resolveStateDir()
			if err != nil {
				return err
			}
			release, err := vector.LockStateUsage(state)
			if err != nil {
				return err
			}
			defer release()
			federation, federationErr := env.federation("")
			if federationErr == nil {
				report, err := federation.Compact(command.Context())
				if err != nil {
					return err
				}
				if env.json {
					return printJSON(report)
				}
				fmt.Printf("vector compact: %d -> %d pages · %d live chunks · %d databases\n",
					report.PagesBefore, report.PagesAfter, report.LiveChunks, report.Databases)
				fmt.Printf("  bytes: %d -> %d · %d reclaimed\n",
					report.BytesBefore, report.BytesAfter, report.BytesReclaimed)
				return nil
			}
			if !errors.Is(federationErr, os.ErrNotExist) {
				return federationErr
			}
			report, err := vector.Compact(command.Context(), filepath.Join(state, vector.DatabaseFilename))
			if err != nil {
				return err
			}
			if env.json {
				return printJSON(report)
			}
			fmt.Printf("vector compact: %d -> %d pages · %d live chunks\n",
				report.PagesBefore, report.PagesAfter, report.LiveChunks)
			fmt.Printf("  bytes: %d -> %d · %d reclaimed\n",
				report.BytesBefore, report.BytesAfter, report.BytesReclaimed)
			return nil
		},
	}
}

func queryCommand(env *environment) *cobra.Command {
	var databases string
	var expandTemplates bool
	var minScore float64
	command := &cobra.Command{
		Use:   "query <text> [k]",
		Short: "Search routed database sidecars by semantic similarity",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(command *cobra.Command, args []string) error {
			k := 10
			if len(args) == 2 {
				parsed, err := strconv.Atoi(args[1])
				if err != nil {
					return fmt.Errorf("k must be an integer: %w", err)
				}
				k = parsed
			}
			state, err := env.resolveStateDir()
			if err != nil {
				return err
			}
			release, err := vector.LockStateUsage(state)
			if err != nil {
				return err
			}
			defer release()
			started := time.Now()
			federation, federationErr := env.federation("")
			if federationErr == nil {
				var result vector.FederatedQuery
				if expandTemplates {
					result, err = federation.QueryExpanded(command.Context(), args[0], k, databases, minScore)
				} else {
					result, err = federation.Query(command.Context(), args[0], k, databases)
				}
				if err != nil {
					return err
				}
				if env.json {
					return printJSON(map[string]any{"query": args[0], "k": k,
						"databases": result.Databases, "model": result.Model,
						"mixed_models": result.MixedModels, "results": result.Results,
						"database_results": result.DatabaseResults, "notices": result.Notices,
						"vector_executed": result.VectorExecuted,
						"elapsed_ms":      time.Since(started).Milliseconds()})
				}
				for _, notice := range result.Notices {
					fmt.Fprintln(os.Stderr, "notice:", notice)
				}
				if result.MixedModels {
					for _, database := range result.DatabaseResults {
						fmt.Printf("database %s · model %s\n", database.Database, database.Model)
						printResults(database.Results)
					}
				} else {
					printResults(result.Results)
				}
				return nil
			}
			if !errors.Is(federationErr, os.ErrNotExist) {
				return federationErr
			}
			if strings.TrimSpace(databases) != "" {
				return fmt.Errorf("--databases needs federated sidecars; run `roca vector install`")
			}
			vectorPath := filepath.Join(state, vector.DatabaseFilename)
			index, err := env.index(vector.ConfiguredModel(vectorPath))
			if err != nil {
				return err
			}
			var results []vector.Result
			if expandTemplates {
				results, err = index.QueryExpanded(command.Context(), args[0], k, minScore)
			} else {
				results, err = index.Query(command.Context(), args[0], k)
			}
			if err != nil {
				return err
			}
			if env.json {
				return printJSON(map[string]any{"query": args[0], "k": k,
					"results": results, "vector_executed": true,
					"elapsed_ms": time.Since(started).Milliseconds()})
			}
			printResults(results)
			return nil
		},
	}
	command.Flags().StringVar(&databases, "databases", "",
		"comma list of attached database names (corpus,ops), or all")
	command.Flags().BoolVar(&expandTemplates, "expand-templates", false,
		"embed the query plus static question templates and union the neighbors")
	command.Flags().Float64Var(&minScore, "min-score", 0,
		"drop vector hits below this cosine when expanding templates")
	return command
}

func printResults(results []vector.Result) {
	for _, result := range results {
		if result.Database != "" {
			fmt.Printf("%d. %.3f · database=%s · table=%s · id=%s\n",
				result.Rank, result.Score, result.Database, result.Table, result.ID)
		} else {
			fmt.Printf("%d. %.3f · %s · %s\n", result.Rank, result.Score, result.Source, result.SourceID)
		}
		fmt.Printf("   %s\n", preview(result.Text, 500))
	}
}

func workerCommand(env *environment) *cobra.Command {
	model := vector.DefaultModel
	command := &cobra.Command{
		Use:    "_worker",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			state, err := env.resolveStateDir()
			if err != nil {
				return err
			}
			release, err := vector.LockStateUsage(state)
			if err != nil {
				return err
			}
			defer release()
			defer vector.ReleaseWorkerClaim(state)
			federation, federationErr := env.federation(model)
			var completion vector.Completion
			if federationErr == nil {
				worker := vector.FederatedWorker{Federation: federation, DataDir: state, PullModel: true,
					Notifier: vector.SystemNotifier{}, WaitForCalm: env.calmGate().Wait}
				completion = worker.Run(command.Context())
			} else {
				if !errors.Is(federationErr, os.ErrNotExist) {
					return federationErr
				}
				index, err := env.index(model)
				if err != nil {
					return err
				}
				worker := vector.Worker{Index: index, DataDir: state, PullModel: true,
					Notifier: vector.SystemNotifier{}, WaitForCalm: env.calmGate().Wait}
				completion = worker.Run(command.Context())
			}
			if env.json {
				if err := printJSON(completion); err != nil {
					return err
				}
			} else {
				fmt.Printf("vector worker: exit %d · %d added · %d updated · %d removed · %d chunks\n",
					completion.ExitStatus, completion.Delta.Added, completion.Delta.Updated,
					completion.Delta.Removed, completion.Delta.Chunks)
				if completion.Error != "" {
					fmt.Printf("  error: %s\n", completion.Error)
				}
			}
			if completion.ExitStatus != 0 {
				return fmt.Errorf("vector worker failed: %s", completion.Error)
			}
			return nil
		},
	}
	command.Flags().StringVar(&model, "model", model, "embedding model identifier")
	return command
}

func (env *environment) index(model string) (vector.Index, error) {
	if federation, err := env.federation(model); err == nil {
		return federation.CorpusIndex()
	} else if !errors.Is(err, os.ErrNotExist) {
		return vector.Index{}, err
	}
	state, err := env.resolveStateDir()
	if err != nil {
		return vector.Index{}, err
	}
	core, err := env.core()
	if err != nil {
		return vector.Index{}, err
	}
	embedder, events := env.embedder()
	return vector.Index{Corpus: core, VectorPath: filepath.Join(state, vector.DatabaseFilename),
		Model: model, Embedder: embedder, Events: events,
		Notice: func(message string) { fmt.Fprintln(os.Stderr, message) }, Database: "corpus"}, nil
}

func (env *environment) federation(model string) (vector.Federation, error) {
	embedder, events := env.embedder()
	return env.federationWithEmbedder(model, embedder, events)
}

func (env *environment) federationWithEmbedder(model string, embedder vector.Embedder,
	events engine.Sink) (vector.Federation, error) {
	core, err := env.core()
	if err != nil {
		return vector.Federation{}, err
	}
	pluginRoot, err := env.resolvePluginRoot()
	if err != nil {
		return vector.Federation{}, err
	}
	loaded, err := vector.LoadFederation(core, pluginRoot, model, version,
		embedder, func(message string) { fmt.Fprintln(os.Stderr, message) })
	if err != nil {
		return vector.Federation{}, err
	}
	loaded.Events = events
	return loaded, nil
}

func defaultEmbedder(env *environment) vector.Embedder {
	events := env.events()
	var tel *telemetry.Store
	if store, err := telemetry.Open(coreDataDir(env.dbPath)); err == nil {
		tel = store
	}
	return vector.ConfiguredEmbedder(coreDataDir(env.dbPath), env.stateDir, events, tel)
}

func (env *environment) embedder() (vector.Embedder, engine.Sink) {
	return newEmbedder(env), env.events()
}

func (env *environment) events() engine.Sink {
	return func(event engine.Event) {
		fmt.Fprintln(os.Stderr, event.Line())
	}
}

func (env *environment) resolvePluginRoot() (string, error) {
	if override := strings.TrimSpace(os.Getenv("ROCA_VECTOR_PLUGIN_ROOT")); override != "" {
		return filepath.Abs(override)
	}
	// An explicit state directory is the test and standalone-development seat;
	// keep its registry scoped beside the selected database instead of reading
	// an operator's installed home plugins by accident.
	if env.stateDir != "" {
		return filepath.Join(coreDataDir(env.dbPath), "plugins"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", fmt.Errorf("find HOME for the vector registry")
	}
	return filepath.Join(home, ".roca", "plugins"), nil
}

func (env *environment) core() (vector.CoreCLI, error) {
	executable := strings.TrimSpace(os.Getenv("ROCA_VECTOR_ROCA_BINARY"))
	if executable == "" {
		var err error
		executable, err = exec.LookPath("roca")
		if err != nil {
			return vector.CoreCLI{}, fmt.Errorf("find the roca core executable on PATH: %w", err)
		}
	}
	return vector.CoreCLI{Executable: executable, DBPath: env.dbPath}, nil
}

func (env *environment) resolveStateDir() (string, error) {
	if env.stateDir != "" {
		return filepath.Abs(env.stateDir)
	}
	if override := strings.TrimSpace(os.Getenv("ROCA_VECTOR_STATE_DIR")); override != "" {
		return filepath.Abs(override)
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", fmt.Errorf("find HOME for the vector plugin state directory")
	}
	return filepath.Join(home, ".roca", "plugins", "roca-vector", "state"), nil
}

func (env *environment) calmGate() vector.CalmGate {
	data := coreDataDir(env.dbPath)
	home, _ := os.UserHomeDir()
	return vector.CalmGate{DataDir: data,
		JourneyPaths: vector.DefaultJourneyPaths(data, home, os.Getenv("ROCA_CRON_JOURNEY_DB"))}
}

func coreDataDir(flag string) string {
	path := strings.TrimSpace(flag)
	if path == "" {
		path = strings.TrimSpace(os.Getenv("ROCA_DB_PATH"))
	}
	if path != "" {
		absolute, err := filepath.Abs(path)
		if err == nil {
			return filepath.Dir(absolute)
		}
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".roca")
}

func workerArguments(dbPath, state, model string) []string {
	arguments := []string{"--state-dir", state}
	if dbPath != "" {
		arguments = append(arguments, "--db-path", dbPath)
	}
	return append(arguments, "_worker", "--model", model)
}

func printJSON(value any) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func preview(text string, limit int) string {
	text = strings.Join(strings.Fields(text), " ")
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "…"
}

func readOnly() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("ROCA_READ_ONLY"))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
