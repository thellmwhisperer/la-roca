package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

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
	positionals := make([]string, 0, 3)
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--json" || strings.HasPrefix(arg, "--json="):
			inv.json = true
		case arg == "--db-path":
			i++
			if i >= len(args) {
				return vectorQueryInvocation{}, false
			}
			inv.dbPath = args[i]
		case strings.HasPrefix(arg, "--db-path="):
			inv.dbPath = strings.TrimPrefix(arg, "--db-path=")
		case arg == "--databases":
			i++
			if i >= len(args) {
				return vectorQueryInvocation{}, false
			}
			inv.databases = args[i]
		case strings.HasPrefix(arg, "--databases="):
			inv.databases = strings.TrimPrefix(arg, "--databases=")
		case arg == "--expand-templates" || strings.HasPrefix(arg, "--expand-templates="):
			inv.expandTemplates = true
		case arg == "--min-score":
			i++
			if i >= len(args) {
				return vectorQueryInvocation{}, false
			}
			parsed, err := strconv.ParseFloat(args[i], 64)
			if err != nil {
				return vectorQueryInvocation{}, false
			}
			inv.minScore = parsed
		case strings.HasPrefix(arg, "--min-score="):
			parsed, err := strconv.ParseFloat(strings.TrimPrefix(arg, "--min-score="), 64)
			if err != nil {
				return vectorQueryInvocation{}, false
			}
			inv.minScore = parsed
		case strings.HasPrefix(arg, "-"):
			continue
		default:
			positionals = append(positionals, arg)
		}
	}
	if len(positionals) == 0 || positionals[0] != "query" {
		return vectorQueryInvocation{}, false
	}
	positionals = positionals[1:]
	if len(positionals) == 0 {
		return vectorQueryInvocation{}, false
	}
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

func runVectorQueryResident(env *cliEnv, args []string, companion string, paths config.Paths) (bool, int, error) {
	inv, ok := parseVectorQueryInvocation(args)
	if !ok {
		return false, 0, nil
	}
	dbPath := inv.dbPath
	if dbPath == "" && paths.DB != "" {
		dbPath = paths.DB
	}
	dataDir := filepath.Dir(dbPath)
	if dataDir == "." || dataDir == "" {
		dataDir = paths.Home
		if dataDir != "" {
			dataDir = filepath.Join(dataDir, ".roca")
		}
	}
	opts := vectorresident.Options{
		Binary:     companion,
		DataDir:    dataDir,
		DBPath:     dbPath,
		PluginRoot: pluginRoot(paths),
		Status:     os.Stderr,
	}
	if opts.DataDir != "" {
		opts.StateDir = filepath.Join(opts.DataDir, "plugins", "roca-vector", "state")
	}
	started := time.Now()
	raw, err := vectorresident.QueryOnce(context.Background(), opts, vectorresident.Request{
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
