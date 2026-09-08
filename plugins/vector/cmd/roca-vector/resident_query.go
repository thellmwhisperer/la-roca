package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/thellmwhisperer/la-roca-vector/internal/vector"
	"github.com/thellmwhisperer/la-roca/pkg/vectorresident"
)

func (env *environment) queryThroughResident(ctx context.Context, text string, k int,
	databases string, expandTemplates bool, minScore float64) (vector.FederatedQuery, bool, error) {
	opts := env.residentQueryOptions()
	client, err := vectorresident.ConnectCurrent(ctx, opts)
	if err != nil {
		return vector.FederatedQuery{}, false, nil
	}
	defer client.Close()
	raw, err := client.Query(ctx, vectorresident.Request{
		Query: text, K: k, Databases: databases,
		ExpandTemplates: expandTemplates, MinScore: minScore,
	})
	if err != nil {
		return vector.FederatedQuery{}, true, err
	}
	var result vector.FederatedQuery
	if err := json.Unmarshal(raw, &result); err != nil {
		return vector.FederatedQuery{}, true, fmt.Errorf("decode semantic search: %w", err)
	}
	return result, true, nil
}

func (env *environment) residentQueryOptions() vectorresident.Options {
	binary := strings.TrimSpace(os.Getenv("ROCA_VECTOR_RESIDENT_BINARY"))
	if binary == "" {
		if exe, err := currentExecutable(); err == nil && vectorresident.NamedPluginBinary(exe) {
			binary = exe
		}
	}
	pluginRoot, err := env.resolvePluginRoot()
	if err != nil {
		pluginRoot = ""
	}
	state, err := env.resolveStateDir()
	if err != nil {
		state = ""
	}
	return vectorresident.Options{
		Binary:     binary,
		DataDir:    coreDataDir(env.dbPath),
		DBPath:     env.dbPath,
		PluginRoot: pluginRoot,
		StateDir:   state,
		Status:     os.Stderr,
	}
}

func printFederatedQuery(env *environment, query string, k int, started time.Time, result vector.FederatedQuery) error {
	if env.json {
		return printJSON(map[string]any{"query": query, "k": k,
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
		return nil
	}
	printResults(result.Results)
	return nil
}
