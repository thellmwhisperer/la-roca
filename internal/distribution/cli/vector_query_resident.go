package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/pflag"
	"github.com/thellmwhisperer/la-roca/internal/provider/config"
	"github.com/thellmwhisperer/la-roca/pkg/vectorresident"
)

type vectorQueryInvocation struct {
	query           string
	k               int
	databases       string
	json            bool
	expandTemplates bool
	minScore        float64
	dbPath          string
}

func parseVectorQueryInvocation(args []string) (vectorQueryInvocation, bool) {
	inv := vectorQueryInvocation{k: 10}
	flags := pflag.NewFlagSet("vector query", pflag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.BoolVar(&inv.json, "json", false, "")
	flags.StringVar(&inv.dbPath, "db-path", "", "")
	flags.StringVar(&inv.databases, "databases", "", "")
	flags.BoolVar(&inv.expandTemplates, "expand-templates", false, "")
	flags.Float64Var(&inv.minScore, "min-score", 0, "")
	if err := flags.Parse(args); err != nil || flags.ArgsLenAtDash() == 0 {
		return vectorQueryInvocation{}, false
	}
	positionals := flags.Args()
	if len(positionals) < 2 || len(positionals) > 3 || positionals[0] != "query" {
		return vectorQueryInvocation{}, false
	}
	positionals = positionals[1:]
	inv.query = positionals[0]
	if len(positionals) > 1 {
		parsed, err := strconv.Atoi(positionals[1])
		if err != nil {
			return vectorQueryInvocation{}, false
		}
		inv.k = parsed
	}
	return inv, true
}

func vectorQueryResidentOptions(inv vectorQueryInvocation, companion string, paths config.Paths) (vectorresident.Options, error) {
	resolved, err := config.Resolve(config.Input{Flag: inv.dbPath, Env: paths.DB, Home: paths.Home})
	if err != nil {
		return vectorresident.Options{}, err
	}
	root := pluginRoot(paths)
	if override := strings.TrimSpace(os.Getenv("ROCA_VECTOR_PLUGIN_ROOT")); override != "" {
		root, err = filepath.Abs(override)
		if err != nil {
			return vectorresident.Options{}, err
		}
	}
	state := filepath.Join(paths.Home, ".roca", "plugins", "roca-vector", "state")
	if override := strings.TrimSpace(os.Getenv("ROCA_VECTOR_STATE_DIR")); override != "" {
		state, err = filepath.Abs(override)
		if err != nil {
			return vectorresident.Options{}, err
		}
	}
	host, err := os.Executable()
	if err != nil {
		return vectorresident.Options{}, err
	}
	return vectorresident.Options{
		Binary: companion, HostBinary: host,
		DataDir: filepath.Dir(resolved.DB), DBPath: resolved.DB,
		PluginRoot: root, StateDir: state, Status: os.Stderr,
	}, nil
}

func runVectorQueryResident(env *cliEnv, args []string, companion string, paths config.Paths) (bool, int, error) {
	inv, ok := parseVectorQueryInvocation(args)
	if !ok {
		return false, 0, nil
	}
	opts, err := vectorQueryResidentOptions(inv, companion, paths)
	if err != nil || opts.PluginRoot == "" {
		return false, 0, nil
	}
	if info, err := os.Stat(filepath.Join(opts.PluginRoot, "vector-registry.json")); err != nil || !info.Mode().IsRegular() {
		return false, 0, nil
	}
	started := time.Now()
	client, err := vectorresident.ConnectCurrent(context.Background(), opts)
	if err != nil {
		return false, 0, nil
	}
	defer client.Close()
	raw, err := client.Query(context.Background(), vectorresident.Request{
		Query: inv.query, K: inv.k, Databases: inv.databases,
		ExpandTemplates: inv.expandTemplates, MinScore: inv.minScore,
	})
	if err != nil {
		return true, ExitError, err
	}
	var result federatedVectorQuery
	if err := json.Unmarshal(raw, &result); err != nil {
		return true, ExitError, fmt.Errorf("decode semantic search: %w", err)
	}
	if inv.json || env.json {
		if err := env.printJSON(map[string]any{
			"query": inv.query, "k": inv.k, "databases": result.Databases, "model": result.Model,
			"mixed_models": result.MixedModels, "results": result.Results,
			"database_results": result.DatabaseResults, "notices": result.Notices,
			"vector_executed": result.VectorExecuted,
			"elapsed_ms":      time.Since(started).Milliseconds(),
		}); err != nil {
			return true, ExitError, err
		}
		return true, ExitOK, nil
	}
	for _, notice := range result.Notices {
		fmt.Fprintln(env.errOut, "notice:", notice)
	}
	if result.MixedModels {
		for _, database := range result.DatabaseResults {
			fmt.Fprintf(env.out, "database %s · model %s\n", database.Database, database.Model)
			printVectorHits(env, database.Results)
		}
		return true, ExitOK, nil
	}
	printVectorHits(env, result.Results)
	return true, ExitOK, nil
}

type federatedVectorQuery struct {
	Databases       []string       `json:"databases"`
	Model           string         `json:"model,omitempty"`
	MixedModels     bool           `json:"mixed_models"`
	VectorExecuted  bool           `json:"vector_executed"`
	Results         []vectorHit    `json:"results"`
	DatabaseResults []databaseHits `json:"database_results,omitempty"`
	Notices         []string       `json:"notices"`
}

type databaseHits struct {
	Database string      `json:"database"`
	Model    string      `json:"model"`
	Results  []vectorHit `json:"results"`
}

type vectorHit struct {
	Rank     int     `json:"rank"`
	Score    float64 `json:"score"`
	Database string  `json:"database,omitempty"`
	Table    string  `json:"table,omitempty"`
	ID       string  `json:"id,omitempty"`
	Source   string  `json:"source"`
	SourceID string  `json:"source_id"`
	Text     string  `json:"text"`
}

func printVectorHits(env *cliEnv, results []vectorHit) {
	out := env.out
	if out == nil {
		out = os.Stdout
	}
	for _, result := range results {
		if result.Database != "" {
			fmt.Fprintf(out, "%d. %.3f · database=%s · table=%s · id=%s\n",
				result.Rank, result.Score, result.Database, result.Table, result.ID)
		} else {
			fmt.Fprintf(out, "%d. %.3f · %s · %s\n", result.Rank, result.Score, result.Source, result.SourceID)
		}
		fmt.Fprintf(out, "   %s\n", previewVectorText(result.Text, 500))
	}
}

func previewVectorText(text string, limit int) string {
	text = strings.Join(strings.Fields(text), " ")
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "…"
}
