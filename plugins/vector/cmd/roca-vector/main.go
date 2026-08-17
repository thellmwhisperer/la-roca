package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/thellmwhisperer/la-roca-vector/internal/vector"
)

var (
	version           = "dev"
	commit            = "none"
	date              = "unknown"
	launchWorker      = vector.Launch
	currentExecutable = os.Executable
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
	root.AddCommand(installCommand(env), ingestCommand(env), queryCommand(env), workerCommand(env))
	return root
}

func installCommand(env *environment) *cobra.Command {
	model := vector.DefaultModel
	command := &cobra.Command{
		Use:   "install",
		Short: "Download the embedding model and build the index in the background",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
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
	command.Flags().StringVar(&model, "model", model, "local Ollama embedding model")
	return command
}

func ingestCommand(env *environment) *cobra.Command {
	var delta bool
	var model string
	var source string
	command := &cobra.Command{
		Use:   "ingest --delta",
		Short: "Embed only new or changed corpus chunks",
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
			vectorPath := filepath.Join(state, vector.DatabaseFilename)
			if _, err := os.Stat(vectorPath); err != nil {
				if os.IsNotExist(err) {
					return fmt.Errorf("vector search is not initialized; run `roca vector install`")
				}
				return fmt.Errorf("inspect vector index: %w", err)
			}
			if model == "" {
				model = vector.ConfiguredModel(vectorPath)
			}
			if err := env.calmGate().Wait(command.Context()); err != nil {
				return err
			}
			index, err := env.index(model)
			if err != nil {
				return err
			}
			started := time.Now()
			var report vector.Delta
			if source == "" {
				report, err = index.Ingest(command.Context())
			} else {
				report, err = index.IngestSource(command.Context(), source)
			}
			if err != nil {
				return err
			}
			if env.json {
				return printJSON(map[string]any{"mode": "delta", "model": model,
					"source": source, "counts": report, "elapsed_ms": time.Since(started).Milliseconds()})
			}
			label := "vector delta"
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
	command.Flags().StringVar(&model, "model", "", "local Ollama embedding model (default: indexed model)")
	command.Flags().StringVar(&source, "source", "", "limit the delta to one corpus source kind")
	return command
}

func queryCommand(env *environment) *cobra.Command {
	return &cobra.Command{
		Use:   "query <text> [k]",
		Short: "Search the local corpus by semantic similarity",
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
			vectorPath := filepath.Join(state, vector.DatabaseFilename)
			index, err := env.index(vector.ConfiguredModel(vectorPath))
			if err != nil {
				return err
			}
			started := time.Now()
			results, err := index.Query(command.Context(), args[0], k)
			if err != nil {
				return err
			}
			if env.json {
				return printJSON(map[string]any{"query": args[0], "k": k,
					"results": results, "elapsed_ms": time.Since(started).Milliseconds()})
			}
			for _, result := range results {
				fmt.Printf("%d. %.3f · %s · %s\n", result.Rank, result.Score, result.Source, result.SourceID)
				fmt.Printf("   %s\n", preview(result.Text, 500))
			}
			return nil
		},
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
			defer vector.ReleaseWorkerClaim(state)
			index, err := env.index(model)
			if err != nil {
				return err
			}
			worker := vector.Worker{Index: index, DataDir: state, PullModel: true,
				Notifier: vector.SystemNotifier{}, WaitForCalm: env.calmGate().Wait}
			completion := worker.Run(command.Context())
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
	command.Flags().StringVar(&model, "model", model, "local Ollama embedding model")
	return command
}

func (env *environment) index(model string) (vector.Index, error) {
	state, err := env.resolveStateDir()
	if err != nil {
		return vector.Index{}, err
	}
	core, err := env.core()
	if err != nil {
		return vector.Index{}, err
	}
	return vector.Index{Corpus: core, VectorPath: filepath.Join(state, vector.DatabaseFilename),
		Model: model, Embedder: vector.Ollama{BaseURL: os.Getenv("OLLAMA_HOST")},
		Notice: func(message string) { fmt.Fprintln(os.Stderr, message) }}, nil
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
	return filepath.Join(home, ".roca", "plugins", "vector", "state"), nil
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
