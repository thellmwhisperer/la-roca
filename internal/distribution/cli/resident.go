package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/pflag"
	"github.com/thellmwhisperer/la-roca/internal/distribution/axi"
	"github.com/thellmwhisperer/la-roca/internal/provider/config"
	"github.com/thellmwhisperer/la-roca/internal/provider/service"
	resident "github.com/thellmwhisperer/la-roca/pkg/resident"
	"github.com/thellmwhisperer/la-roca/pkg/vectorresident"
)

// tryResident is deliberately a narrow dispatch seam. Commands that are
// lifecycle or migration operations still use the ordinary local service;
// the hot read/write/context surfaces use the resident whenever it is already
// alive and fall back to the legacy opener when it is not.
func (env *cliEnv) tryResident(ctx context.Context, raw []string) (bool, error) {
	if env.forceReadOnly || config.ReadOnly(os.Getenv(config.EnvReadOnly)) || hasReadOnlyFlag(raw) {
		return false, nil
	}
	command, args, jsonOutput, dbPath := residentCommandArgs(raw)
	if command == "" || (command != "exec" && command != "query" && command != "store" &&
		command != "health") {
		if command != "vector" || !env.features.Vector || len(args) == 0 || args[0] != "query" {
			return false, nil
		}
	}
	if command == "vector" {
		return env.tryResidentVector(ctx, args, jsonOutput, dbPath)
	}
	if command == "" {
		return false, nil
	}
	options, err := env.residentOptions(dbPath)
	if err != nil {
		return false, nil
	}
	call := func(op string, request any, target any) error {
		rawResult, callErr := resident.Call(ctx, options, op, request)
		if callErr != nil {
			if errors.Is(callErr, resident.ErrUnavailable) || strings.Contains(callErr.Error(), "resident socket path is too long") {
				return errResidentUnavailable
			}
			return callErr
		}
		return decodeJSON(string(rawResult), target)
	}

	switch command {
	case "exec":
		return runResidentCommand(env, args, jsonOutput, parseResidentExec, call, command, axi.Exec,
			func(result *service.ExecResult, request service.ExecRequest) {
				result.MaxChars = service.TextBudget(request.MaxChars)
				env.capture(*result)
			})
	case "query":
		return runResidentCommand(env, args, jsonOutput, parseResidentQuery, call, command, axi.Search,
			func(result *service.SearchResult, request service.SearchRequest) {
				result.MaxChars = service.TextBudget(request.MaxChars)
				env.capture(*result)
			})
	case "store":
		return runResidentCommand(env, args, jsonOutput, parseResidentStore, call, command, axi.Store, nil)
	case "health":
		return runResidentCommand(env, args, jsonOutput, parseResidentHealth, call, command, axi.Health, nil)
	}
	return true, nil
}

func runResidentCommand[Request, Result any](env *cliEnv, args []string, jsonOutput bool,
	parse func([]string) (Request, bool), call func(string, any, any) error, command string,
	render func(Result) string, prepare func(*Result, Request)) (bool, error) {
	request, ok := parse(args)
	if !ok {
		return false, nil
	}
	var result Result
	if err := call(command, request, &result); errors.Is(err, errResidentUnavailable) {
		return false, nil
	} else if err != nil {
		return true, err
	}
	if prepare != nil {
		prepare(&result, request)
	}
	if jsonOutput || env.json {
		return true, env.printJSON(result)
	}
	env.print("%s", render(result))
	return true, nil
}

func (env *cliEnv) tryResidentVector(ctx context.Context, args []string, jsonOutput bool, dbPath string) (bool, error) {
	inv, ok := parseVectorQueryInvocation(args)
	if !ok {
		return false, nil
	}
	if inv.dbPath != "" {
		dbPath = inv.dbPath
	}
	options, err := env.residentOptions(dbPath)
	if err != nil {
		return false, nil
	}
	started := time.Now()
	payload, err := resident.Call(ctx, options, "vector_query", vectorresident.Request{
		Query: inv.query, K: inv.k, Databases: inv.databases,
		ExpandTemplates: inv.expandTemplates, MinScore: inv.minScore,
	})
	if err != nil {
		if errors.Is(err, resident.ErrUnavailable) || strings.Contains(err.Error(), "resident socket path is too long") {
			return false, nil
		}
		return true, err
	}
	var result federatedVectorQuery
	if err := json.Unmarshal(payload, &result); err != nil {
		return true, fmt.Errorf("decode semantic search: %w", err)
	}
	return true, printVectorQueryResult(env, inv, result, jsonOutput, started)
}

var errResidentUnavailable = errors.New("resident unavailable")

func (env *cliEnv) residentOptions(dbPath string) (resident.Options, error) {
	if dbPath != "" {
		env.dbPath = dbPath
	}
	paths, err := env.resolvePaths()
	if err != nil {
		return resident.Options{}, err
	}
	binary, err := os.Executable()
	if err != nil {
		return resident.Options{}, err
	}
	return resident.Options{Binary: binary, DataDir: filepathDir(paths.DB), DBPath: paths.DB}, nil
}

func residentCommandArgs(raw []string) (string, []string, bool, string) {
	jsonOutput := false
	dbPath := ""
	for index := 0; index < len(raw); index++ {
		switch {
		case raw[index] == "--json" || strings.HasPrefix(raw[index], "--json="):
			jsonOutput = true
		case raw[index] == "--db-path" && index+1 < len(raw):
			dbPath = raw[index+1]
			index++
		case strings.HasPrefix(raw[index], "--db-path="):
			dbPath = strings.TrimPrefix(raw[index], "--db-path=")
		}
	}
	for index := 0; index < len(raw); index++ {
		value := raw[index]
		if value == "--db-path" {
			index++
			continue
		}
		if strings.HasPrefix(value, "-") {
			continue
		}
		return value, stripResidentGlobals(raw[index+1:]), jsonOutput, dbPath
	}
	return "", nil, jsonOutput, dbPath
}

func stripResidentGlobals(args []string) []string {
	filtered := make([]string, 0, len(args))
	for index := 0; index < len(args); index++ {
		switch {
		case args[index] == "--json" || strings.HasPrefix(args[index], "--json="):
			continue
		case args[index] == "--db-path":
			index++
			continue
		case strings.HasPrefix(args[index], "--db-path="):
			continue
		default:
			filtered = append(filtered, args[index])
		}
	}
	return filtered
}

func hasReadOnlyFlag(args []string) bool {
	for _, arg := range args {
		if arg == "--read-only" || arg == "--read-only=true" {
			return true
		}
	}
	return false
}

func parseResidentExec(args []string) (service.ExecRequest, bool) {
	flags := pflag.NewFlagSet("exec", pflag.ContinueOnError)
	flags.SetOutput(nil)
	maxChars := service.DefaultMaxChars
	timeout := -1
	flags.IntVar(&maxChars, "max-chars", maxChars, "")
	flags.IntVar(&timeout, "timeout-ms", -1, "")
	if err := flags.Parse(args); err != nil || len(flags.Args()) == 0 {
		return service.ExecRequest{}, false
	}
	request := service.ExecRequest{SQL: strings.Join(flags.Args(), " "), MaxChars: maxChars}
	if timeout >= 0 {
		request.TimeoutSet = true
		request.Timeout = time.Duration(timeout) * time.Millisecond
	}
	return request, true
}

func parseResidentQuery(args []string) (service.SearchRequest, bool) {
	flags := pflag.NewFlagSet("query", pflag.ContinueOnError)
	flags.SetOutput(nil)
	top, maxChars, oversample := 10, service.DefaultMaxChars, 0
	databases := ""
	requireBoth, noTemplates := false, false
	flags.IntVar(&top, "top", top, "")
	flags.IntVar(&maxChars, "max-chars", maxChars, "")
	flags.IntVar(&oversample, "oversample", 0, "")
	flags.StringVar(&databases, "databases", "", "")
	flags.BoolVar(&requireBoth, "require-both", false, "")
	flags.BoolVar(&noTemplates, "no-templates", false, "")
	if err := flags.Parse(args); err != nil || len(flags.Args()) == 0 {
		return service.SearchRequest{}, false
	}
	names, err := service.ParseDatabaseList(databases)
	if err != nil {
		return service.SearchRequest{}, false
	}
	request := service.SearchRequest{Question: strings.Join(flags.Args(), " "), Databases: names, Top: top, RequireBoth: requireBoth, MaxChars: maxChars}
	if oversample > 0 {
		request.Overlay.Oversample = &oversample
	}
	request.Overlay.NoTemplates = noTemplates
	return request, true
}

func parseResidentStore(args []string) (service.StoreRequest, bool) {
	flags := pflag.NewFlagSet("store", pflag.ContinueOnError)
	flags.SetOutput(nil)
	var request service.StoreRequest
	var metadata, agent, model string
	flags.StringVar(&request.Layer, "layer", "", "")
	flags.StringVar(&request.Content, "content", "", "")
	flags.StringVar(&request.Origin, "origin", "", "")
	flags.StringVar(&request.Project, "project", "", "")
	flags.StringVar(&request.Status, "status", "", "")
	flags.Int64Var(&request.Supersedes, "supersedes", 0, "")
	flags.StringVar(&metadata, "metadata", "", "")
	flags.StringVar(&agent, "agent", "", "")
	flags.StringVar(&model, "model", "", "")
	if err := flags.Parse(args); err != nil || request.Layer == "" || request.Content == "" {
		return service.StoreRequest{}, false
	}
	if metadata != "" && (decodeJSON(metadata, &request.Metadata) != nil || request.Metadata == nil) {
		return service.StoreRequest{}, false
	}
	request.Authorship = resolveCLIAuthorship(agent, model, currentAuthorshipEvidence)
	return request, true
}

func parseResidentHealth(args []string) (service.HealthRequest, bool) {
	flags := pflag.NewFlagSet("health", pflag.ContinueOnError)
	flags.SetOutput(nil)
	maxRows := 0
	flags.IntVar(&maxRows, "max-rows", 0, "")
	if err := flags.Parse(args); err != nil || len(flags.Args()) != 0 {
		return service.HealthRequest{}, false
	}
	return service.HealthRequest{MaxRows: maxRows}, true
}

func filepathDir(path string) string {
	index := strings.LastIndexAny(path, "/\\")
	if index < 0 {
		return "."
	}
	return path[:index]
}

func (env *cliEnv) callResident(ctx context.Context, op string, request, target any) (bool, error) {
	if env.forceReadOnly || config.ReadOnly(os.Getenv(config.EnvReadOnly)) {
		return false, nil
	}
	options, err := env.residentOptions("")
	if err != nil {
		return false, nil
	}
	payload, err := resident.Call(ctx, options, op, request)
	if errors.Is(err, resident.ErrUnavailable) || (err != nil && strings.Contains(err.Error(), "resident socket path is too long")) {
		return false, nil
	}
	if err != nil {
		return true, err
	}
	return true, decodeJSON(string(payload), target)
}
