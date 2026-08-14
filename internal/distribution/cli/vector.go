package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/thellmwhisperer/la-roca/internal/provider/config"
	"github.com/thellmwhisperer/la-roca/internal/provider/service"
	"github.com/thellmwhisperer/la-roca/internal/vector"
)

var (
	launchVectorWorker = vector.Launch
	vectorExecutable   = os.Executable
	newVectorEmbedder  = func() vector.Embedder {
		return vector.Ollama{BaseURL: os.Getenv("OLLAMA_HOST")}
	}
)

func vectorCommand(env *cliEnv) *cobra.Command {
	command := &cobra.Command{
		Use:   "vector",
		Short: "Install and use optional local semantic search",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	command.AddCommand(vectorInstallCommand(env), vectorIngestCommand(env),
		vectorQueryCommand(env), vectorWorkerCommand(env))
	return command
}

func vectorInstallCommand(env *cliEnv) *cobra.Command {
	model := vector.DefaultModel
	command := &cobra.Command{
		Use:   "install",
		Short: "Download the embedding model and build the index in the background",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			if err := refuseVectorWriteWhenReadOnly("vector install"); err != nil {
				return err
			}
			paths, err := env.resolvePaths()
			if err != nil {
				return err
			}
			if _, err := os.Stat(paths.DB); err != nil {
				return fmt.Errorf("core database is not ready at %s; run `roca init` first", paths.DB)
			}
			executable, err := vectorExecutable()
			if err != nil {
				return fmt.Errorf("locate the roca executable: %w", err)
			}
			directory := vectorDataDir(paths)
			result, err := launchVectorWorker(vector.LaunchRequest{
				Executable: executable, Arguments: vector.WorkerArguments(paths.DB, model), DataDir: directory,
			})
			if err != nil {
				return err
			}
			document := map[string]any{
				"background": true, "model": model, "pid": result.PID,
				"already_running": result.AlreadyRunning, "log_path": result.LogPath,
			}
			if env.json {
				return env.printJSON(document)
			}
			if result.AlreadyRunning {
				env.print("vector install: background indexing is already running")
			} else {
				env.print("vector install: background worker %d started", result.PID)
			}
			env.print("  model: %s", model)
			env.print("  completion: a desktop notification will report exit status and counts")
			env.print("  log: %s", result.LogPath)
			return nil
		},
	}
	command.Flags().StringVar(&model, "model", model, "local Ollama embedding model")
	return command
}

func vectorIngestCommand(env *cliEnv) *cobra.Command {
	var delta bool
	var model string
	command := &cobra.Command{
		Use:   "ingest --delta",
		Short: "Embed only new or changed core chunks",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !delta {
				return fmt.Errorf("vector ingest is incremental; pass --delta")
			}
			if err := refuseVectorWriteWhenReadOnly("vector ingest --delta"); err != nil {
				return err
			}
			paths, err := env.resolvePaths()
			if err != nil {
				return err
			}
			vectorPath := filepath.Join(vectorDataDir(paths), vector.DatabaseFilename)
			if _, err := os.Stat(vectorPath); err != nil {
				return fmt.Errorf("vector search is not installed; run `roca vector install`")
			}
			if model == "" {
				model = vector.ConfiguredModel(vectorPath)
			}
			gate := vectorCalmGate(paths)
			if err := gate.Wait(cmd.Context()); err != nil {
				return err
			}
			started := time.Now()
			report, err := vectorIndex(paths, model).Ingest(cmd.Context())
			if err != nil {
				return err
			}
			document := map[string]any{
				"mode": "delta", "model": model, "counts": report,
				"elapsed_ms": time.Since(started).Milliseconds(),
			}
			if env.json {
				return env.printJSON(document)
			}
			env.print("vector delta: %d added · %d updated · %d removed · %d unchanged",
				report.Added, report.Updated, report.Removed, report.Unchanged)
			env.print("  sources: %d · chunks: %d · model: %s", report.Sources, report.Chunks, model)
			return nil
		},
	}
	command.Flags().BoolVar(&delta, "delta", false, "embed only new or changed chunks")
	command.Flags().StringVar(&model, "model", "", "local Ollama embedding model (default: installed model)")
	return command
}

func vectorQueryCommand(env *cliEnv) *cobra.Command {
	return &cobra.Command{
		Use:   "query <text> [k]",
		Short: "Search the local corpus by semantic similarity",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			k := 10
			if len(args) == 2 {
				parsed, err := strconv.Atoi(args[1])
				if err != nil {
					return fmt.Errorf("k must be an integer: %w", err)
				}
				k = parsed
			}
			paths, err := env.resolvePaths()
			if err != nil {
				return err
			}
			started := time.Now()
			results, err := vectorIndex(paths, vector.DefaultModel).Query(cmd.Context(), args[0], k)
			if err != nil {
				return err
			}
			document := map[string]any{"query": args[0], "k": k, "results": results,
				"elapsed_ms": time.Since(started).Milliseconds()}
			if env.json {
				return env.printJSON(document)
			}
			for _, result := range results {
				env.print("%d. %.3f · %s · %s", result.Rank, result.Score, result.Source, result.SourceID)
				env.print("   %s", vectorPreview(result.Text, 500))
			}
			return nil
		},
	}
}

func vectorWorkerCommand(env *cliEnv) *cobra.Command {
	model := vector.DefaultModel
	command := &cobra.Command{
		Use:    "_worker",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			paths, err := env.resolvePaths()
			if err != nil {
				return err
			}
			directory := vectorDataDir(paths)
			defer vector.ReleaseWorkerClaim(directory)
			worker := vector.Worker{
				Index: vectorIndex(paths, model), DataDir: directory, PullModel: true,
				Notifier: vector.SystemNotifier{}, WaitForCalm: vectorCalmGate(paths).Wait,
			}
			completion := worker.Run(cmd.Context())
			env.code = completion.ExitStatus
			if env.json {
				return env.printJSON(completion)
			}
			env.print("vector worker: exit %d · %d added · %d updated · %d removed · %d chunks",
				completion.ExitStatus, completion.Delta.Added, completion.Delta.Updated,
				completion.Delta.Removed, completion.Delta.Chunks)
			if completion.Error != "" {
				env.print("  error: %s", completion.Error)
			}
			return nil
		},
	}
	command.Flags().StringVar(&model, "model", model, "local Ollama embedding model")
	return command
}

// refuseVectorWriteWhenReadOnly holds the operator's boundary over the vector
// store, which is a database of its own outside the shared service, so the
// service's refusal never sees these calls. The check has to happen here,
// before a background worker is launched or a single embedding is written, or
// read-only mode would mean one thing for the corpus and another for the index
// built from it.
func refuseVectorWriteWhenReadOnly(operation string) error {
	if !config.ReadOnly(os.Getenv(config.EnvReadOnly)) {
		return nil
	}
	return service.RefuseReadOnly(operation)
}

func vectorDataDir(paths config.Paths) string {
	return filepath.Join(filepath.Dir(paths.DB), "vector")
}

func vectorIndex(paths config.Paths, model string) vector.Index {
	return vector.Index{CorePath: paths.DB,
		VectorPath: filepath.Join(vectorDataDir(paths), vector.DatabaseFilename),
		Model:      model, Embedder: newVectorEmbedder()}
}

func vectorCalmGate(paths config.Paths) vector.CalmGate {
	return vector.CalmGate{DataDir: filepath.Dir(paths.DB),
		JourneyPaths: vector.DefaultJourneyPaths(filepath.Dir(paths.DB), paths.Home,
			os.Getenv("ROCA_CRON_JOURNEY_DB"))}
}

func vectorPreview(text string, limit int) string {
	text = strings.Join(strings.Fields(text), " ")
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "…"
}
