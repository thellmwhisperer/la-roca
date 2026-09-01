package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/thellmwhisperer/la-roca-vector/internal/engine"
	"github.com/thellmwhisperer/la-roca-vector/internal/llamacpp"
	"github.com/thellmwhisperer/la-roca-vector/internal/model"
	"github.com/thellmwhisperer/la-roca-vector/internal/telemetry"
	"github.com/thellmwhisperer/la-roca-vector/internal/vector"
)

// writerGPUEnv carries the operator lever into the detached worker, which is
// its own process and never sees the flag the parent was given.
const writerGPUEnv = "ROCA_VECTOR_WRITER_GPU"

// accelerateFlag is the operator lever: the flag is tri-state on purpose, so
// "not passed" keeps the occasion's default and `--accelerate=false` can force
// a bulk build back onto the CPU.
const accelerateFlag = "accelerate"

const accelerateUsage = "run this indexing pass on the accelerator " +
	"(--accelerate=false forces the cpu)"

var (
	version           = "dev"
	commit            = "none"
	date              = "unknown"
	launchWorker      = vector.Launch
	currentExecutable = os.Executable
	newEmbedder       = defaultEmbedder
)

type environment struct {
	json       bool
	dbPath     string
	stateDir   string
	progressFD int
	// writer is the backend policy the running command decided on. Commands
	// that never index leave it zero and take the conservative delta default.
	writer           llamacpp.Policy
	nativeTrapAction func(string) error
}

// writerPolicy resolves the occasion against the operator lever. The flag is a
// pointer because only a flag the operator actually typed overrides the
// environment; an untouched flag is not a decision.
func writerPolicy(occasion llamacpp.Occasion, flag *bool) llamacpp.Policy {
	lever := llamacpp.LeverFrom(os.Getenv(writerGPUEnv))
	if flag != nil {
		lever = llamacpp.LeverFor(*flag)
	}
	return llamacpp.Policy{Occasion: occasion, Lever: lever}
}

// acceleratorLever reports the flag only when the operator set it.
func acceleratorLever(command *cobra.Command, value bool) *bool {
	if !command.Flags().Changed(accelerateFlag) {
		return nil
	}
	return &value
}

// workerLeverEnvironment hands an explicit lever down to the spawned worker.
// Without one the worker keeps its own default for the occasion it runs.
func workerLeverEnvironment(flag *bool) []string {
	if flag == nil {
		return nil
	}
	if *flag {
		return []string{writerGPUEnv + "=1"}
	}
	return []string{writerGPUEnv + "=0"}
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
	root.PersistentFlags().IntVar(&env.progressFD, "progress-fd", 0, "live progress output")
	_ = root.PersistentFlags().MarkHidden("state-dir")
	_ = root.PersistentFlags().MarkHidden("progress-fd")
	root.AddCommand(installCommand(env), ingestCommand(env), compactCommand(env),
		queryCommand(env), statusCommand(env), workerCommand(env), residentCommand(env))
	return root
}

func installCommand(env *environment) *cobra.Command {
	model := vector.DefaultModel
	streamProgress := false
	accelerate := false
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
			pluginRoot, err := env.resolvePluginRoot()
			if err != nil {
				return err
			}
			arguments := workerArguments(env.dbPath, state, model)
			var progress *os.File
			if !env.json || streamProgress {
				progress = os.Stderr
			}
			workerEnvironment := []string{"ROCA_VECTOR_PLUGIN_ROOT=" + pluginRoot}
			workerEnvironment = append(workerEnvironment,
				workerLeverEnvironment(acceleratorLever(command, accelerate))...)
			result, err := launchWorker(vector.LaunchRequest{
				Executable: executable, Arguments: arguments, DataDir: state, Progress: progress,
				Environment: workerEnvironment,
			})
			if err != nil {
				return err
			}
			if env.json {
				return printJSON(map[string]any{
					"background": true, "model": model,
					"already_running": result.AlreadyRunning,
				})
			}
			if result.AlreadyRunning {
				fmt.Println("semantic search: setup is already running")
			} else {
				fmt.Println("semantic search: setup continues in the background")
			}
			fmt.Println("semantic search: word search keeps answering while history is read")
			return nil
		},
	}
	command.Flags().StringVar(&model, "model", model, "embedding model identifier")
	command.Flags().BoolVar(&streamProgress, "stream-progress", false, "stream setup progress")
	_ = command.Flags().MarkHidden("stream-progress")
	command.Flags().BoolVar(&accelerate, accelerateFlag, true, accelerateUsage)
	return command
}

// indexStatus is the whole answer to "how is it going", in the terms the person
// asking has: how much of their history has been read, whether reading is still
// going on, and what stopped it if it stopped.
type indexStatus struct {
	HistoryKnown bool                      `json:"history_known"`
	Running      bool                      `json:"running"`
	Completed    bool                      `json:"completed"`
	Read         int                       `json:"read"`
	Total        int                       `json:"total"`
	Databases    []vector.DatabaseProgress `json:"databases,omitempty"`
	Stopped      string                    `json:"stopped,omitempty"`
}

func statusCommand(env *environment) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Report per-database vectorization without waiting for the model",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			report, err := env.vectorizationStatus(command.Context())
			if err != nil {
				return err
			}
			help := statusHelp(report)
			out := command.OutOrStdout()
			if env.json {
				return printJSONTo(out, map[string]any{
					"worker":    report.Worker,
					"databases": report.Databases,
					"help":      help,
				})
			}
			_, err = fmt.Fprintln(out, renderVectorization(report, help))
			return err
		},
	}
}

func (env *environment) indexStatus(ctx context.Context) (indexStatus, error) {
	state, err := env.resolveStateDir()
	if err != nil {
		return indexStatus{}, err
	}
	status := indexStatus{Running: vector.WorkerRunning(state)}
	if completion, ok := vector.ReadCompletion(state); ok && !status.Running {
		status.Completed = completion.ExitStatus == 0 && strings.TrimSpace(completion.Error) == ""
		if !status.Completed {
			status.Stopped = productStopReason(completion.Error)
			if status.Stopped == "" {
				status.Stopped = "the pass stopped before it finished"
			}
		}
	}
	federation, err := env.federation("")
	if err != nil {
		return status, nil
	}
	progress, err := federation.HistoryProgress(ctx)
	if err != nil {
		return status, nil
	}
	status.HistoryKnown = true
	status.Read, status.Total, status.Databases = progress.Read, progress.Total, progress.Databases
	return status, nil
}

// statusLines keeps the report in product language. A pass that has not finished
// is never the same sentence as a product with nothing in it, and word search is
// named every time reading is not complete, because it is answering right then.
func statusLines(status indexStatus) []string {
	if !status.HistoryKnown {
		if status.Stopped != "" {
			return []string{"deep search: progress unavailable · word search is answering now",
				"  it stopped because " + productStopReason(status.Stopped),
				"  next step: `roca vector install`"}
		}
		return []string{"deep search: progress unavailable · word search is answering now",
			"  next step: `roca vector install`"}
	}
	fraction := fmt.Sprintf("%d of %d read", status.Read, status.Total)
	switch {
	case status.Running:
		return []string{"deep search: reading your history · " + fraction,
			"  word search keeps answering while it runs"}
	case status.Stopped != "":
		return []string{"deep search: stopped · " + fraction,
			"  what it read already answers, and word search answers for the rest",
			"  it stopped because " + productStopReason(status.Stopped),
			"  next step: `roca vector install`"}
	case status.Total == 0:
		return []string{"deep search: nothing to read yet · there is no history on this machine"}
	case status.Completed && status.Read >= status.Total:
		return []string{"deep search: ready · your history is understood, not only searched"}
	case status.Read >= status.Total:
		return []string{"deep search: stopped before it finished · " + fraction,
			"  word search is answering now", "  next step: `roca vector install`"}
	case status.Read > 0:
		lines := []string{"deep search: stopped partway · " + fraction,
			"  what it read already answers, and word search answers for the rest"}
		return append(lines, "  next step: `roca vector install`")
	default:
		return []string{"deep search: not started · word search is answering now",
			"  next step: `roca vector install`"}
	}
}

func productStopReason(raw string) string {
	reason := strings.TrimSpace(raw)
	if reason == "" {
		return ""
	}
	switch reason {
	case "the pass was interrupted",
		"this machine ran out of storage space",
		"the local reading service stopped answering",
		"the history could not be read",
		"the pass stopped before it finished":
		return reason
	}
	lower := strings.ToLower(reason)
	switch {
	case strings.Contains(lower, "interrupt"), strings.Contains(lower, "canceled"),
		strings.Contains(lower, "cancelled"), strings.Contains(lower, "signal"),
		strings.Contains(lower, "deadline"):
		return "the pass was interrupted"
	case strings.Contains(lower, "no space"), strings.Contains(lower, "disk full"):
		return "this machine ran out of storage space"
	case strings.Contains(lower, "ollama"), strings.Contains(lower, "embedding"),
		strings.Contains(lower, "model"), strings.Contains(lower, "runtime"),
		strings.Contains(lower, "connection refused"):
		return "the local reading service stopped answering"
	case strings.Contains(lower, "sqlite"), strings.Contains(lower, "database"),
		strings.Contains(lower, "registry"), strings.Contains(lower, "sidecar"),
		strings.Contains(lower, ".db"), strings.Contains(lower, "no such table"),
		strings.Contains(lower, "permission denied"):
		return "the history could not be read"
	default:
		return "the pass stopped before it finished"
	}
}

func ingestCommand(env *environment) *cobra.Command {
	var delta bool
	var model string
	var source string
	var reembed bool
	var accelerate bool
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
			// A reembed rebuilds every chunk: it is a bulk job the operator is
			// waiting on, not the background pass that yields to live search.
			env.writer = writerPolicy(llamacpp.WriterOccasion(reembed),
				acceleratorLever(command, accelerate))
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
	command.Flags().BoolVar(&accelerate, accelerateFlag, false, accelerateUsage)
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
			if err := env.startBackgroundSetup(); err != nil {
				return err
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
			federation, federationErr := env.federationForQuery("")
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
			index, err := env.indexForQuery(vector.ConfiguredModel(vectorPath))
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
		"comma list of attached database names to narrow the default federation, or all")
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
			// The worker is the install build: a foreground bulk job from the
			// operator's side, so it accelerates unless the parent said not to.
			env.writer = writerPolicy(llamacpp.OccasionBulk, nil)
			workerContext, cancelWorker := context.WithCancel(command.Context())
			defer cancelWorker()
			workerContext, drainCommands := vector.WithWorkerCommandDrain(workerContext)
			recovery := vector.NewWorkerTrapRecovery(cancelWorker, drainCommands)
			env.nativeTrapAction = recovery.Handle
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
				completion = worker.Run(workerContext)
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
				completion = worker.Run(workerContext)
			}
			if requested, restartErr := recovery.RestartIfRequested(); requested {
				return restartErr
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

func (env *environment) federationForQuery(model string) (vector.Federation, error) {
	embedder, events := env.queryEmbedder()
	return env.federationWithEmbedder(model, embedder, events)
}

func (env *environment) indexForQuery(model string) (vector.Index, error) {
	if federation, err := env.federationForQuery(model); err == nil {
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
	embedder, events := env.queryEmbedder()
	return vector.Index{Corpus: core, VectorPath: filepath.Join(state, vector.DatabaseFilename),
		Model: model, Embedder: embedder, Events: events,
		Notice: func(message string) { fmt.Fprintln(os.Stderr, message) }, Database: "corpus"}, nil
}

func (env *environment) startBackgroundSetup() error {
	if readOnly() {
		return nil
	}
	if _, err := model.Existing(coreDataDir(env.dbPath), model.DefaultManifest()); err == nil {
		return nil
	}
	state, err := env.resolveStateDir()
	if err != nil {
		return err
	}
	executable, err := currentExecutable()
	if err != nil {
		return fmt.Errorf("locate roca-vector: %w", err)
	}
	pluginRoot, err := env.resolvePluginRoot()
	if err != nil {
		return err
	}
	_, err = launchWorker(vector.LaunchRequest{
		Executable: executable, Arguments: workerArguments(env.dbPath, state, vector.DefaultModel),
		DataDir:     state,
		Environment: []string{"ROCA_VECTOR_PLUGIN_ROOT=" + pluginRoot},
	})
	return err
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
	locked := readOnly()
	if !locked {
		if store, err := telemetry.Open(coreDataDir(env.dbPath)); err == nil {
			tel = store
		}
	}
	// A command that did not declare its occasion is not a bulk build; keep the
	// conservative default that leaves the accelerator to live search.
	writer := env.writer
	if writer.Occasion == "" {
		writer = writerPolicy(llamacpp.OccasionDelta, nil)
	}
	return vector.ConfiguredEmbedder(coreDataDir(env.dbPath), env.stateDir, events, tel, locked, writer)
}

func (env *environment) embedder() (vector.Embedder, engine.Sink) {
	embedder := newEmbedder(env)
	if env.nativeTrapAction != nil {
		vector.EnableWorkerRestartOnNativeTrap(embedder, env.nativeTrapAction)
	}
	return embedder, env.events()
}

func (env *environment) queryEmbedder() (vector.Embedder, engine.Sink) {
	events := env.events()
	return vector.ConfiguredEmbedder(coreDataDir(env.dbPath), env.stateDir, events, nil, true,
		llamacpp.ReadPolicy()), events
}

func (env *environment) events() engine.Sink {
	output := io.Writer(os.Stderr)
	if env.progressFD > 2 {
		if progress := os.NewFile(uintptr(env.progressFD), "semantic-progress"); progress != nil {
			output = io.MultiWriter(output, progress)
		}
	}
	return func(event engine.Event) {
		fmt.Fprintln(output, event.Line())
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
