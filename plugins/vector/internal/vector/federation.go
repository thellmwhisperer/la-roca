package vector

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/thellmwhisperer/la-roca-vector/internal/engine"
	"github.com/thellmwhisperer/la-roca-vector/internal/telemetry"
	"github.com/thellmwhisperer/la-roca/pkg/incrementality"
)

const (
	vectorRegistryFilename = "vector-registry.json"
	vectorRegistrySchema   = 2
	declaredReaderVersion  = "declared-surfaces-v2"
)

var (
	manifestPluginName = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
	sqlIdentifier      = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

type vectorRegistry struct {
	Schema    int              `json:"schema"`
	Databases []vectorDatabase `json:"databases"`
	Routes    []vectorRoute    `json:"routes"`
}

type vectorRoute struct {
	Plugin   string `json:"plugin"`
	Database string `json:"database"`
	Alias    string `json:"alias"`
	Source   string `json:"source"`
}

type vectorDatabase struct {
	Plugin   string        `json:"plugin"`
	Database string        `json:"database"`
	Path     string        `json:"path"`
	Alias    string        `json:"alias"`
	Tables   []vectorTable `json:"tables"`
}

type vectorTable struct {
	Name        string          `json:"name"`
	IDColumn    string          `json:"id_column"`
	TextColumns []string        `json:"text_columns"`
	TimeColumns []string        `json:"time_columns,omitempty"`
	TimeJoin    *vectorTimeJoin `json:"time_join,omitempty"`
	Columns     []string        `json:"columns,omitempty"`
	Chunking    *chunkingHints  `json:"chunking,omitempty"`
}

type vectorTimeJoin struct {
	Table         string   `json:"table"`
	LocalColumn   string   `json:"local_column"`
	ForeignColumn string   `json:"foreign_column"`
	TimeColumns   []string `json:"time_columns"`
}

type chunkingHints struct {
	MaxChars     *int `json:"max_chars,omitempty"`
	OverlapChars *int `json:"overlap_chars,omitempty"`
}

type Federation struct {
	Core         CoreCLI
	PluginRoot   string
	Model        string
	BuildVersion string
	Embedder     Embedder
	Notice       func(string)
	Progress     func(IngestProgress)
	Reembed      bool
	Events       engine.Sink
	databases    []vectorDatabase
	routes       []vectorRoute
}

func LoadFederation(core CoreCLI, pluginRoot, model, buildVersion string,
	embedder Embedder, notice func(string)) (Federation, error) {
	absolute, err := filepath.Abs(pluginRoot)
	if err != nil {
		return Federation{}, fmt.Errorf("resolve plugin root: %w", err)
	}
	registry, err := loadVectorRegistry(filepath.Join(absolute, vectorRegistryFilename))
	if err != nil {
		return Federation{}, err
	}
	if len(registry.Databases) == 0 {
		return Federation{}, fmt.Errorf("vector registry declares no databases")
	}
	if len(registry.Routes) == 0 {
		registry.Routes = make([]vectorRoute, 0, len(registry.Databases))
		for _, database := range registry.Databases {
			registry.Routes = append(registry.Routes, vectorRoute{Plugin: database.Plugin,
				Database: database.Database, Alias: database.Alias, Source: "plugin:" + database.owner()})
		}
	}
	if buildVersion == "" {
		buildVersion = "dev"
	}
	return Federation{Core: core, PluginRoot: absolute, Model: model,
		BuildVersion: buildVersion, Embedder: embedder, Notice: notice,
		databases: registry.Databases, routes: registry.Routes}, nil
}

func loadVectorRegistry(path string) (vectorRegistry, error) {
	file, err := os.Open(path)
	if err != nil {
		return vectorRegistry{}, fmt.Errorf("read vector registry: %w", err)
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	var registry vectorRegistry
	if err := decoder.Decode(&registry); err != nil {
		return vectorRegistry{}, fmt.Errorf("read vector registry: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return vectorRegistry{}, fmt.Errorf("read vector registry: contains more than one JSON value")
		}
		return vectorRegistry{}, fmt.Errorf("read vector registry: %w", err)
	}
	if err := validateRegistry(registry); err != nil {
		return vectorRegistry{}, err
	}
	return registry, nil
}

func validateRegistry(registry vectorRegistry) error {
	if registry.Schema != 1 && registry.Schema != vectorRegistrySchema {
		return fmt.Errorf("vector registry schema is %d, want 1 or %d", registry.Schema, vectorRegistrySchema)
	}
	seenDatabases := map[string]bool{}
	seenRoutes := map[string]bool{}
	for _, route := range registry.Routes {
		owner := route.Plugin + "/" + route.Database
		if !manifestPluginName.MatchString(route.Plugin) || strings.TrimSpace(route.Database) == "" ||
			!validIdentifier(route.Alias) || strings.TrimSpace(route.Source) == "" || seenRoutes[owner] {
			return fmt.Errorf("vector registry has invalid or repeated route %q", owner)
		}
		seenRoutes[owner] = true
	}
	databasePaths := map[string]string{}
	for _, database := range registry.Databases {
		owner := database.owner()
		if !manifestPluginName.MatchString(database.Plugin) || !validIdentifier(database.Database) ||
			seenDatabases[owner] {
			return fmt.Errorf("vector registry has invalid or repeated database %q", owner)
		}
		seenDatabases[owner] = true
		if filepath.Base(database.Path) != database.Path || filepath.Clean(database.Path) != database.Path {
			return fmt.Errorf("vector registry database %s has unsafe path %q", owner, database.Path)
		}
		switch strings.ToLower(filepath.Ext(database.Path)) {
		case ".db", ".sqlite", ".sqlite3":
		default:
			return fmt.Errorf("vector registry database %s has invalid SQLite path %q", owner, database.Path)
		}
		packagePath := filepath.Join(database.Plugin, database.Path)
		if previous := databasePaths[packagePath]; previous != "" {
			return fmt.Errorf("vector registry databases %s and %s share path %q", previous, owner, packagePath)
		}
		databasePaths[packagePath] = owner
		if !validIdentifier(database.Alias) || len(database.Tables) == 0 {
			return fmt.Errorf("vector registry database %s needs an alias and tables", owner)
		}
		seenTables := map[string]bool{}
		for _, table := range database.Tables {
			if !validIdentifier(table.Name) || !validIdentifier(table.IDColumn) ||
				seenTables[table.Name] || len(table.TextColumns) == 0 {
				return fmt.Errorf("vector registry database %s has invalid table %q", owner, table.Name)
			}
			seenTables[table.Name] = true
			seenColumns := map[string]bool{}
			for _, column := range table.TextColumns {
				if !validIdentifier(column) || seenColumns[column] {
					return fmt.Errorf("vector registry table %s/%s has invalid text column %q",
						owner, table.Name, column)
				}
				seenColumns[column] = true
			}
			catalogColumns := map[string]bool{}
			for _, column := range table.Columns {
				if !validIdentifier(column) || catalogColumns[column] {
					return fmt.Errorf("vector registry table %s/%s has invalid catalog column %q",
						owner, table.Name, column)
				}
				catalogColumns[column] = true
			}
			if len(catalogColumns) > 0 {
				if !catalogColumns[table.IDColumn] {
					return fmt.Errorf("vector registry table %s/%s catalog omits id column %q",
						owner, table.Name, table.IDColumn)
				}
				for _, column := range table.TextColumns {
					if !catalogColumns[column] {
						return fmt.Errorf("vector registry table %s/%s catalog omits text column %q",
							owner, table.Name, column)
					}
				}
			}
			for _, column := range table.TimeColumns {
				if !validIdentifier(column) {
					return fmt.Errorf("vector registry table %s/%s has invalid time column %q",
						owner, table.Name, column)
				}
				if len(catalogColumns) > 0 && !catalogColumns[column] {
					return fmt.Errorf("vector registry table %s/%s catalog omits time column %q",
						owner, table.Name, column)
				}
			}
			if join := table.TimeJoin; join != nil {
				if !validIdentifier(join.Table) || !validIdentifier(join.LocalColumn) ||
					!validIdentifier(join.ForeignColumn) || len(join.TimeColumns) == 0 {
					return fmt.Errorf("vector registry table %s/%s has an invalid chronological join",
						owner, table.Name)
				}
				for _, column := range join.TimeColumns {
					if !validIdentifier(column) {
						return fmt.Errorf("vector registry table %s/%s has invalid joined time column %q",
							owner, table.Name, column)
					}
				}
			}
			if err := validateChunking(table.Chunking); err != nil {
				return fmt.Errorf("vector registry table %s/%s: %w", owner, table.Name, err)
			}
		}
	}
	for _, database := range registry.Databases {
		sidecar := filepath.Join(database.Plugin, SidecarPath(database.Path))
		if sourceOwner := databasePaths[sidecar]; sourceOwner != "" {
			return fmt.Errorf("vector registry sidecar for %s collides with database %s at %q",
				database.owner(), sourceOwner, sidecar)
		}
	}
	return nil
}

func validateChunking(hints *chunkingHints) error {
	if hints == nil {
		return nil
	}
	if hints.MaxChars != nil && *hints.MaxChars <= 0 {
		return fmt.Errorf("max_chars must be positive")
	}
	if hints.OverlapChars != nil && *hints.OverlapChars < 0 {
		return fmt.Errorf("overlap_chars must not be negative")
	}
	if hints.MaxChars != nil && hints.OverlapChars != nil && *hints.OverlapChars >= *hints.MaxChars {
		return fmt.Errorf("overlap_chars must be smaller than max_chars")
	}
	return nil
}

type FederationDelta struct {
	Delta
	Databases []DatabaseDelta `json:"databases"`
}

type DatabaseDelta struct {
	Owner  string `json:"owner"`
	Counts Delta  `json:"counts"`
}

type FederatedQuery struct {
	Databases       []string              `json:"databases"`
	Model           string                `json:"model,omitempty"`
	MixedModels     bool                  `json:"mixed_models"`
	VectorExecuted  bool                  `json:"vector_executed"`
	Results         []Result              `json:"results"`
	DatabaseResults []DatabaseQueryResult `json:"database_results,omitempty"`
	Notices         []string              `json:"notices"`
}

type DatabaseQueryResult struct {
	Database string   `json:"database"`
	Model    string   `json:"model"`
	Results  []Result `json:"results"`
}

type queryTarget struct {
	database   vectorDatabase
	path       string
	model      string
	dimensions int
}

func (f Federation) Query(ctx context.Context, text string, k int, databaseList string) (FederatedQuery, error) {
	return f.queryTexts(ctx, []string{text}, k, databaseList, 0, true)
}

// QueryExpanded embeds the raw query and the static question templates,
// unions the KNN lists, applies minScore, and dedupes by stable source.
func (f Federation) QueryExpanded(ctx context.Context, text string, k int,
	databaseList string, minScore float64) (FederatedQuery, error) {
	return f.queryTexts(ctx, ExpandedQueries(text), k, databaseList, minScore, false)
}

func (f Federation) queryTexts(ctx context.Context, texts []string, k int, databaseList string,
	minScore float64, trimToK bool) (FederatedQuery, error) {
	result := FederatedQuery{Databases: []string{}, Results: []Result{}, Notices: []string{}}
	cleaned := make([]string, 0, len(texts))
	seenText := map[string]bool{}
	for _, text := range texts {
		text = strings.TrimSpace(text)
		if text == "" || seenText[text] {
			continue
		}
		seenText[text] = true
		cleaned = append(cleaned, text)
	}
	if len(cleaned) == 0 {
		return result, fmt.Errorf("semantic query is empty")
	}
	if k < 1 || k > 100 {
		return result, fmt.Errorf("k must be between 1 and 100")
	}
	scope, err := f.Core.ResolveDatabaseScope(ctx, databaseList)
	if err != nil {
		return result, err
	}
	selected, notices := f.queryDatabases(scope.Selected)
	result.Databases = append(result.Databases, scope.Databases...)
	result.Notices = append(result.Notices, scope.Warnings...)
	result.Notices = append(result.Notices, notices...)
	targets := make([]queryTarget, 0, len(selected))
	for _, database := range selected {
		path := SidecarPath(f.databasePath(database))
		model, dimensions, err := querySidecarState(path, database.owner())
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, errSidecarNotReady) {
			result.Notices = append(result.Notices, fmt.Sprintf(
				"database %s has no ready vector sidecar; continuing with FTS-only", database.Database))
			continue
		}
		if err != nil {
			return result, err
		}
		targets = append(targets, queryTarget{database: database, path: path,
			model: model, dimensions: dimensions})
	}

	groups := make(map[string][]queryTarget)
	for _, target := range targets {
		groups[target.model] = append(groups[target.model], target)
	}
	models := make([]string, 0, len(groups))
	for model := range groups {
		models = append(models, model)
	}
	sort.Strings(models)
	result.MixedModels = len(models) > 1
	if result.MixedModels {
		result.Notices = append(result.Notices,
			"selected sidecars use mixed embedding models; scores are returned per database and are not merged")
	} else if len(models) == 1 {
		result.Model = models[0]
	}

	prefixed := make([]string, len(cleaned))
	for index, text := range cleaned {
		prefixed[index] = QueryPrefix + text
	}
	for _, model := range models {
		group := groups[model]
		if f.Embedder == nil {
			result.noticeModelUnavailable(model, group, "embedding provider is unavailable")
			continue
		}
		vectors, err := f.Embedder.Embed(telemetry.WithOperation(ctx, telemetry.OperationQuery),
			model, prefixed)
		if err != nil {
			result.noticeModelUnavailable(model, group, err.Error())
			continue
		}
		if len(vectors) != len(prefixed) {
			result.noticeModelUnavailable(model, group, "embedding provider returned no query vector")
			continue
		}
		for _, target := range group {
			if err := f.searchTarget(ctx, &result, target, model, vectors, k, minScore); err != nil {
				return result, err
			}
		}
	}
	if result.MixedModels {
		for index := range result.DatabaseResults {
			result.DatabaseResults[index].Results = finishFederatedHits(
				result.DatabaseResults[index].Results, k, trimToK)
		}
		return result, nil
	}
	result.Results = finishFederatedHits(result.Results, k, trimToK)
	return result, nil
}

func (f Federation) searchTarget(ctx context.Context, result *FederatedQuery, target queryTarget,
	model string, vectors [][]float32, k int, minScore float64) error {
	for _, embedding := range vectors {
		if len(embedding) == 0 {
			result.noticeModelUnavailable(model, []queryTarget{target},
				"embedding provider returned no query vector")
			return nil
		}
		if len(embedding) != target.dimensions {
			result.Notices = append(result.Notices, fmt.Sprintf(
				"database %s expects %d-dimensional model %s; continuing with FTS-only",
				target.database.Database, target.dimensions, model))
			return nil
		}
		store, err := openSQLite(target.path, true)
		if err != nil {
			return fmt.Errorf("open vector sidecar for %s: %w", target.database.owner(), err)
		}
		index := f.index(target.database,
			DeclaredCorpus{Core: f.Core, Database: target.database}, target.path)
		index.Model = model
		hits, queryErr := index.queryVector(ctx, store, embedding, k)
		closeErr := store.Close()
		if queryErr != nil {
			return fmt.Errorf("query vector sidecar %s: %w", target.database.owner(), queryErr)
		}
		if closeErr != nil {
			return fmt.Errorf("close vector sidecar %s: %w", target.database.owner(), closeErr)
		}
		result.VectorExecuted = true
		tagFederatedResults(hits, target.database.Database)
		hits = filterVectorFloor(hits, minScore)
		if result.MixedModels {
			merged := false
			for index := range result.DatabaseResults {
				if result.DatabaseResults[index].Database == target.database.Database {
					result.DatabaseResults[index].Results = unionFederatedHits(
						result.DatabaseResults[index].Results, hits)
					merged = true
					break
				}
			}
			if !merged {
				result.DatabaseResults = append(result.DatabaseResults, DatabaseQueryResult{
					Database: target.database.Database, Model: model, Results: hits,
				})
			}
			continue
		}
		result.Results = unionFederatedHits(result.Results, hits)
	}
	return nil
}

func filterVectorFloor(hits []Result, minScore float64) []Result {
	if minScore <= 0 {
		return hits
	}
	out := make([]Result, 0, len(hits))
	for _, hit := range hits {
		if hit.Score >= minScore {
			out = append(out, hit)
		}
	}
	return out
}

func unionFederatedHits(existing, incoming []Result) []Result {
	best := make(map[string]Result, len(existing)+len(incoming))
	order := make([]string, 0, len(existing)+len(incoming))
	add := func(hit Result) {
		key := hit.Database + "\x00" + hit.Table + "\x00" + hit.ID
		previous, seen := best[key]
		if !seen {
			best[key] = hit
			order = append(order, key)
			return
		}
		if hit.Score > previous.Score {
			best[key] = hit
		}
	}
	for _, hit := range existing {
		add(hit)
	}
	for _, hit := range incoming {
		add(hit)
	}
	out := make([]Result, 0, len(order))
	for _, key := range order {
		out = append(out, best[key])
	}
	return out
}

func finishFederatedHits(hits []Result, k int, trimToK bool) []Result {
	sortFederatedResults(hits)
	if trimToK && len(hits) > k {
		hits = hits[:k]
	}
	for index := range hits {
		hits[index].Rank = index + 1
	}
	return hits
}

func (r *FederatedQuery) noticeModelUnavailable(model string, targets []queryTarget, reason string) {
	for _, target := range targets {
		r.Notices = append(r.Notices, fmt.Sprintf(
			"embedding model %s is unavailable for database %s; continuing with FTS-only: %s",
			model, target.database.Database, reason))
	}
}

func tagFederatedResults(results []Result, database string) {
	for index := range results {
		results[index].Database = database
		if results[index].Table == "" {
			results[index].Table = results[index].Source
		}
	}
}

func sortFederatedResults(results []Result) {
	sort.SliceStable(results, func(i, j int) bool {
		if results[i].Score != results[j].Score {
			return results[i].Score > results[j].Score
		}
		left := results[i].Database + "\x00" + results[i].Table + "\x00" + results[i].ID
		right := results[j].Database + "\x00" + results[j].Table + "\x00" + results[j].ID
		return left < right
	})
}

var errSidecarNotReady = errors.New("vector sidecar is not ready")

func querySidecarState(path, owner string) (string, int, error) {
	if _, err := os.Stat(path); err != nil {
		return "", 0, err
	}
	store, err := openSQLite(path, true)
	if err != nil {
		return "", 0, fmt.Errorf("open vector sidecar for %s: %w", owner, err)
	}
	defer store.Close()
	metadata, err := readMetadata(store, "owner", "model", "dimensions")
	if errors.Is(err, sql.ErrNoRows) || strings.Contains(fmt.Sprint(err), "no such table") {
		return "", 0, errSidecarNotReady
	}
	if err != nil {
		return "", 0, fmt.Errorf("read vector sidecar for %s: %w", owner, err)
	}
	if metadata["owner"] != owner {
		return "", 0, fmt.Errorf("vector sidecar owner is %s, want %s", metadata["owner"], owner)
	}
	dimensions, err := strconv.Atoi(metadata["dimensions"])
	if metadata["model"] == "" || err != nil || dimensions <= 0 {
		return "", 0, errSidecarNotReady
	}
	return metadata["model"], dimensions, nil
}

func (f Federation) queryDatabases(selections []DatabaseSelection) ([]vectorDatabase, []string) {
	selected := make([]vectorDatabase, 0, len(selections))
	var notices []string
	seen := map[string]bool{}
	for _, selection := range selections {
		if selection.Source == "core" {
			notices = append(notices, "database core has no vector declaration; continuing with FTS-only")
			continue
		}
		var matched *vectorRoute
		for index := range f.routes {
			if !routeMatchesSelection(f.routes[index], selection) {
				continue
			}
			matched = &f.routes[index]
			break
		}
		if matched == nil {
			notices = append(notices, fmt.Sprintf(
				"database %s has no vector declaration; continuing with FTS-only", selection.Database))
			continue
		}
		database, ok := f.vectorDatabaseForRoute(*matched)
		if !ok {
			notices = append(notices, fmt.Sprintf(
				"database %s has no vector declaration; continuing with FTS-only", matched.Database))
			continue
		}
		if !seen[database.owner()] {
			selected = append(selected, database)
			seen[database.owner()] = true
		}
	}
	return selected, notices
}

func routeMatchesSelection(route vectorRoute, selection DatabaseSelection) bool {
	if route.Source == selection.Source && route.Database == selection.Database {
		return true
	}
	if route.Database != selection.Database {
		return false
	}
	return selection.Source == "plugin:"+route.Plugin ||
		selection.Source == "plugin:"+route.Plugin+"/"+route.Database
}

func (f Federation) vectorDatabaseForRoute(route vectorRoute) (vectorDatabase, bool) {
	for _, database := range f.databases {
		if database.Plugin == route.Plugin && database.Database == route.Database {
			return database, true
		}
	}
	return vectorDatabase{}, false
}

func containsString(values []string, candidate string) bool {
	for _, value := range values {
		if value == candidate {
			return true
		}
	}
	return false
}

func (f Federation) Ingest(ctx context.Context, sourceKind string) (FederationDelta, error) {
	if f.Model == "" || f.Embedder == nil {
		return FederationDelta{}, fmt.Errorf("embedding model and provider are required")
	}
	result := FederationDelta{Databases: []DatabaseDelta{}}
	matched := sourceKind == ""
	var preparationErr error
	type ingestJob struct {
		database                       vectorDatabase
		reader                         DeclaredCorpus
		sidecar, contract, fingerprint string
		delta                          Delta
		err                            error
	}
	jobs := []*ingestJob{}
	for _, database := range f.databases {
		reader := DeclaredCorpus{Core: f.Core, Database: database}
		if sourceKind != "" && !reader.hasTable(sourceKind) {
			continue
		}
		matched = true
		databasePath := f.databasePath(database)
		sidecar := SidecarPath(databasePath)
		if err := assertSidecarOwner(sidecar, database.owner()); err != nil {
			return FederationDelta{}, err
		}
		contract := database.contractFingerprint()
		fingerprint, err := databaseFingerprint(databasePath, contract)
		if err != nil {
			if preparationErr == nil {
				preparationErr = fmt.Errorf("fingerprint vector source %s: %w", database.owner(), err)
			}
			continue
		}
		if sourceKind == "" && !f.Reembed {
			delta, unchangedErr := unchangedSidecar(sidecar, database.owner(), f.Model, contract, fingerprint)
			if unchangedErr == nil {
				if err := sealSidecar(sidecar, database.owner(), f.Model, f.BuildVersion, contract, fingerprint); err != nil {
					return FederationDelta{}, err
				}
				result.add(database.owner(), delta)
				continue
			}
			if !errors.Is(unchangedErr, errSidecarChanged) {
				return FederationDelta{}, unchangedErr
			}
		}
		if err := claimSidecar(sidecar, database.owner(), f.BuildVersion, contract, sourceKind == ""); err != nil {
			return FederationDelta{}, err
		}
		jobs = append(jobs, &ingestJob{database: database, reader: reader, sidecar: sidecar,
			contract: contract, fingerprint: fingerprint})
	}
	if !matched {
		return FederationDelta{}, fmt.Errorf("unknown vector source %q", sourceKind)
	}
	if len(jobs) == 0 {
		return result, preparationErr
	}
	orderedCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	scheduler := newEmbeddingScheduler(orderedCtx, f.Embedder, len(jobs))
	var workers sync.WaitGroup
	for id, job := range jobs {
		workers.Add(1)
		go func(id int, job *ingestJob) {
			defer workers.Done()
			index := f.index(job.database, job.reader, job.sidecar)
			index.BatchSize = 1
			index.Embedder = scheduledEmbedder{base: f.Embedder, id: id, scheduler: scheduler}
			if sourceKind == "" {
				job.delta, job.err = index.Ingest(orderedCtx)
			} else {
				job.delta, job.err = index.IngestSource(orderedCtx, sourceKind)
			}
			scheduler.finished <- id
		}(id, job)
	}
	scheduler.run()
	workers.Wait()
	var ingestErr error
	for _, job := range jobs {
		if job.err != nil {
			if ingestErr == nil {
				ingestErr = fmt.Errorf("index vector source %s: %w", job.database.owner(), job.err)
			}
			continue
		}
		storedFingerprint := ""
		if sourceKind == "" {
			storedFingerprint = job.fingerprint
		}
		if err := sealSidecar(job.sidecar, job.database.owner(), f.Model, f.BuildVersion,
			job.contract, storedFingerprint); err != nil {
			return FederationDelta{}, err
		}
		result.add(job.database.owner(), job.delta)
	}
	if preparationErr != nil {
		return result, preparationErr
	}
	if ingestErr != nil {
		return result, ingestErr
	}
	return result, nil
}

type embeddingRequest struct {
	id    int
	ctx   context.Context
	model string
	input []string
	order sourceOrder
	reply chan embeddingReply
}

type embeddingReply struct {
	vectors [][]float32
	err     error
}

type embeddingScheduler struct {
	ctx      context.Context
	base     Embedder
	requests chan embeddingRequest
	finished chan int
	active   map[int]bool
	mu       sync.Mutex
}

func newEmbeddingScheduler(ctx context.Context, base Embedder, count int) *embeddingScheduler {
	active := make(map[int]bool, count)
	for id := 0; id < count; id++ {
		active[id] = true
	}
	return &embeddingScheduler{ctx: ctx, base: base, requests: make(chan embeddingRequest),
		finished: make(chan int, count), active: active}
}

func (s *embeddingScheduler) run() {
	pending := map[int]embeddingRequest{}
	for len(s.active) > 0 {
		for len(pending) < len(s.active) {
			select {
			case request := <-s.requests:
				pending[request.id] = request
			case id := <-s.finished:
				delete(s.active, id)
			case <-s.ctx.Done():
				for _, request := range pending {
					request.reply <- embeddingReply{err: s.ctx.Err()}
				}
				return
			}
		}
		if len(s.active) == 0 {
			return
		}
		selected := -1
		for id, request := range pending {
			if selected < 0 || orderNewer(request.order, pending[selected].order) {
				selected = id
			}
		}
		request := pending[selected]
		delete(pending, selected)
		vectors, err := s.embed(request.ctx, request.model, request.input)
		request.reply <- embeddingReply{vectors: vectors, err: err}
	}
}

func (s *embeddingScheduler) embed(ctx context.Context, model string, input []string) ([][]float32, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.base.Embed(ctx, model, input)
}

func orderNewer(left, right sourceOrder) bool {
	if left.timestamp != right.timestamp {
		return left.timestamp > right.timestamp
	}
	return left.id > right.id
}

type scheduledEmbedder struct {
	base      Embedder
	id        int
	scheduler *embeddingScheduler
}

func (e scheduledEmbedder) Pull(ctx context.Context, model string) error {
	return e.base.Pull(ctx, model)
}

func (e scheduledEmbedder) Embed(ctx context.Context, model string, input []string) ([][]float32, error) {
	order, ok := ctx.Value(sourceOrderKey{}).(sourceOrder)
	if !ok {
		return e.scheduler.embed(ctx, model, input)
	}
	reply := make(chan embeddingReply, 1)
	request := embeddingRequest{id: e.id, ctx: ctx, model: model, input: input, order: order, reply: reply}
	select {
	case e.scheduler.requests <- request:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case result := <-reply:
		return result.vectors, result.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (r *FederationDelta) add(owner string, delta Delta) {
	r.Added += delta.Added
	r.Updated += delta.Updated
	r.Removed += delta.Removed
	r.Unchanged += delta.Unchanged
	r.Sources += delta.Sources
	r.Chunks += delta.Chunks
	r.Databases = append(r.Databases, DatabaseDelta{Owner: owner, Counts: delta})
}

func (f Federation) index(database vectorDatabase, reader DeclaredCorpus, sidecar string) Index {
	kinds := make(map[string]bool, len(database.Tables))
	for _, table := range database.Tables {
		kinds[table.Name] = true
	}
	return Index{Corpus: reader, VectorPath: sidecar, Model: f.Model,
		Embedder: f.Embedder, Notice: f.Notice, Progress: f.Progress, Reembed: f.Reembed,
		SourceKinds: kinds, Database: database.Database, Events: f.Events}
}

type DeclaredCorpus struct {
	Core     CoreCLI
	Database vectorDatabase
}

type declaredCursor struct {
	time  string
	id    string
	valid bool
}

func (d DeclaredCorpus) CountChunks(ctx context.Context, sourceKind string) (int64, error) {
	tables := d.Database.Tables
	if sourceKind != "" {
		table, ok := d.table(sourceKind)
		if !ok {
			return 0, fmt.Errorf("unknown vector source %q", sourceKind)
		}
		tables = []vectorTable{table}
	}
	var total int64
	for _, table := range tables {
		alias := "_vector_count"
		size, overlap := table.chunking()
		counts := make([]string, 0, len(table.TextColumns))
		for _, column := range table.TextColumns {
			text := fmt.Sprintf("COALESCE(CAST(%s.%s AS TEXT),'')", alias, quoteIdentifier(column))
			counts = append(counts, chunkCountExpression(text, size, overlap))
		}
		statement := fmt.Sprintf(`SELECT COALESCE(SUM(%s),0) AS total FROM %s.%s AS %s WHERE %s.%s IS NOT NULL AND (%s)`,
			strings.Join(counts, "+"), quoteIdentifier(d.Database.Alias), quoteIdentifier(table.Name),
			alias, alias, quoteIdentifier(table.IDColumn), declaredNonEmptyPredicate(alias, table.TextColumns))
		rows, err := d.Core.queryIngest(ctx, statement)
		if err != nil {
			return 0, fmt.Errorf("count declared chunks %s/%s: %w", d.Database.owner(), table.Name, err)
		}
		if len(rows) != 1 {
			return 0, fmt.Errorf("count declared chunks %s/%s returned %d rows", d.Database.owner(), table.Name, len(rows))
		}
		count, err := integer(rows[0], "total")
		if err != nil {
			return 0, err
		}
		total += count
	}
	return total, nil
}

func (d DeclaredCorpus) WalkSources(ctx context.Context, sourceKind string,
	visit func(sourceRow) error) error {
	tables := d.Database.Tables
	if sourceKind != "" {
		table, ok := d.table(sourceKind)
		if !ok {
			return fmt.Errorf("unknown vector source %q", sourceKind)
		}
		tables = []vectorTable{table}
	}
	catalog := make(map[string]map[string]bool, len(d.Database.Tables))
	for _, table := range d.Database.Tables {
		catalog[table.Name] = table.availableColumns()
	}
	iterators := make([]*declaredTableIterator, 0, len(tables))
	for _, table := range tables {
		iterator := &declaredTableIterator{corpus: d, table: table, catalog: catalog}
		if err := iterator.advance(ctx); err != nil {
			return err
		}
		iterators = append(iterators, iterator)
	}
	for {
		selected := -1
		for index, iterator := range iterators {
			if iterator.current == nil {
				continue
			}
			if selected < 0 || sourceNewer(*iterator.current, *iterators[selected].current) {
				selected = index
			}
		}
		if selected < 0 {
			return nil
		}
		if err := visit(*iterators[selected].current); err != nil {
			return err
		}
		if err := iterators[selected].advance(ctx); err != nil {
			return err
		}
	}
}

type declaredTableIterator struct {
	corpus  DeclaredCorpus
	table   vectorTable
	catalog map[string]map[string]bool
	cursor  declaredCursor
	rows    []sourceRow
	index   int
	done    bool
	current *sourceRow
}

func (i *declaredTableIterator) advance(ctx context.Context) error {
	for {
		if i.index < len(i.rows) {
			row := i.rows[i.index]
			i.index++
			i.current = &row
			return nil
		}
		if i.done {
			i.current = nil
			return nil
		}
		values, err := i.corpus.Core.queryIngest(ctx, i.corpus.pageQuery(i.table, i.cursor, i.catalog))
		if err != nil {
			return fmt.Errorf("read declared surface %s/%s: %w", i.corpus.Database.owner(), i.table.Name, err)
		}
		i.done = len(values) < walkPageSize
		i.rows = i.rows[:0]
		i.index = 0
		for _, value := range values {
			id := stringValue(value["source_id"])
			if id == "" {
				continue
			}
			occurredAt := stringValue(value["context_time"])
			i.cursor = declaredCursor{time: occurredAt, id: id, valid: true}
			row := sourceRow{kind: i.table.Name, sourceID: id,
				fingerprintVersion: i.table.embeddingContractFingerprint(),
				title:              stringValue(value["context_title"]),
				project:            stringValue(value["context_project"]),
				occurredAt:         occurredAt,
				createdAt:          occurredAt}
			if i.table.Chunking != nil &&
				(i.table.Chunking.MaxChars != nil || i.table.Chunking.OverlapChars != nil) {
				size, overlap := i.table.chunking()
				row.chunkSize, row.overlap = size, overlap
			}
			i.rows = append(i.rows, expandColumnRows(row, i.table.TextColumns, value)...)
		}
	}
}

func sourceNewer(left, right sourceRow) bool {
	if left.occurredAt != right.occurredAt {
		return left.occurredAt > right.occurredAt
	}
	if left.sourceID != right.sourceID {
		return left.sourceID > right.sourceID
	}
	if left.column != right.column {
		return left.column < right.column
	}
	return left.kind > right.kind
}

func (d DeclaredCorpus) ResolveSource(ctx context.Context, kind string, where locator) (string, error) {
	table, ok := d.table(kind)
	if !ok {
		return "", fmt.Errorf("unknown vector source %q", kind)
	}
	if where.SourceID == "" {
		return "", nil
	}
	statement := fmt.Sprintf(`SELECT %s FROM %s.%s WHERE CAST(%s AS TEXT)=%s`,
		strings.TrimPrefix(declaredColumnSelect("", table.TextColumns), ", "), quoteIdentifier(d.Database.Alias),
		quoteIdentifier(table.Name), quoteIdentifier(table.IDColumn), sqlLiteral(where.SourceID))
	rows, err := d.Core.query(ctx, statement)
	if err != nil {
		return "", err
	}
	for _, values := range rows {
		expanded := expandColumnRows(sourceRow{kind: kind, sourceID: where.SourceID}, table.TextColumns, values)
		if len(expanded) == 0 {
			continue
		}
		text := expanded[0].rowText
		candidate := sourceRow{kind: kind, sourceID: where.SourceID, text: text, rowText: text,
			fingerprintVersion: table.embeddingContractFingerprint()}
		if candidate.identity() == where.Identity {
			return text, nil
		}
	}
	return "", nil
}

func (d DeclaredCorpus) ResolveSources(ctx context.Context,
	lookups []sourceLookup) (map[string]string, error) {
	resolved := make(map[string]string, len(lookups))
	idsByTable := make(map[string][]string)
	seen := make(map[string]bool)
	for _, lookup := range lookups {
		if lookup.where.SourceID == "" {
			continue
		}
		if _, ok := d.table(lookup.kind); !ok {
			return nil, fmt.Errorf("unknown vector source %q", lookup.kind)
		}
		key := sourceLookupKey(lookup.kind, lookup.where.SourceID)
		if seen[key] {
			continue
		}
		seen[key] = true
		idsByTable[lookup.kind] = append(idsByTable[lookup.kind], lookup.where.SourceID)
	}
	branches := make([]string, 0)
	for _, table := range d.Database.Tables {
		ids := idsByTable[table.Name]
		if len(ids) == 0 {
			continue
		}
		literals := make([]string, len(ids))
		for index, id := range ids {
			literals[index] = sqlLiteral(id)
		}
		inList := strings.Join(literals, ",")
		for _, column := range table.TextColumns {
			branches = append(branches, fmt.Sprintf(
				`SELECT %s AS source_kind,CAST(%s AS TEXT) AS source_id,%s AS column_name,CAST(%s AS TEXT) AS column_text FROM %s.%s WHERE CAST(%s AS TEXT) IN (%s)`,
				sqlLiteral(table.Name), quoteIdentifier(table.IDColumn), sqlLiteral(column),
				quoteIdentifier(column), quoteIdentifier(d.Database.Alias), quoteIdentifier(table.Name),
				quoteIdentifier(table.IDColumn), inList))
		}
	}
	if len(branches) == 0 {
		return resolved, nil
	}
	rows, err := d.Core.query(ctx, strings.Join(branches, " UNION ALL "))
	if err != nil {
		return nil, err
	}
	grouped := map[string]map[string]any{}
	kinds := map[string]string{}
	ids := map[string]string{}
	for _, values := range rows {
		kind := stringValue(values["source_kind"])
		id := stringValue(values["source_id"])
		column := stringValue(values["column_name"])
		key := sourceLookupKey(kind, id)
		if grouped[key] == nil {
			grouped[key] = map[string]any{}
			kinds[key] = kind
			ids[key] = id
		}
		grouped[key][column] = stringValue(values["column_text"])
	}
	for key, values := range grouped {
		kind, id := kinds[key], ids[key]
		table, ok := d.table(kind)
		if !ok {
			continue
		}
		expanded := expandColumnRows(sourceRow{kind: kind, sourceID: id}, table.TextColumns, values)
		if len(expanded) == 0 {
			continue
		}
		text := expanded[0].rowText
		candidate := sourceRow{kind: kind, sourceID: id, text: text, rowText: text,
			fingerprintVersion: table.embeddingContractFingerprint()}
		for _, lookup := range lookups {
			if lookup.kind != kind || lookup.where.SourceID != id {
				continue
			}
			if lookup.where.Identity == "" || candidate.identity() == lookup.where.Identity {
				resolved[key] = text
				break
			}
		}
	}
	return resolved, nil
}

func (d DeclaredCorpus) CountSources(ctx context.Context, sourceKind string) (int, error) {
	tables := d.Database.Tables
	if sourceKind != "" {
		table, ok := d.table(sourceKind)
		if !ok {
			return 0, fmt.Errorf("unknown vector source %q", sourceKind)
		}
		tables = []vectorTable{table}
	}
	total := 0
	for _, table := range tables {
		statement := fmt.Sprintf(`SELECT COUNT(*) AS n FROM %s.%s src WHERE %s IS NOT NULL AND (%s)`,
			quoteIdentifier(d.Database.Alias), quoteIdentifier(table.Name),
			quoteIdentifier(table.IDColumn), declaredNonEmptyPredicate("src", table.TextColumns))
		rows, err := d.Core.queryIngest(ctx, statement)
		if err != nil {
			return 0, err
		}
		if len(rows) == 0 {
			continue
		}
		count, ok := nullableInteger(rows[0]["n"])
		if !ok {
			count, _ = nullableInteger(rows[0]["N"])
		}
		total += int(count)
	}
	return total, nil
}

func (d DeclaredCorpus) pageQuery(table vectorTable, cursor declaredCursor,
	catalog map[string]map[string]bool) string {
	contextSQL, join, timeSQL := d.contextSQL(table, catalog)
	bound := ""
	if cursor.valid {
		bound = fmt.Sprintf(" AND (%s<%s OR (%s=%s AND CAST(src.%s AS TEXT)<%s))",
			timeSQL, sqlLiteral(cursor.time), timeSQL, sqlLiteral(cursor.time),
			quoteIdentifier(table.IDColumn), sqlLiteral(cursor.id))
	}
	return fmt.Sprintf(`SELECT CAST(src.%s AS TEXT) AS source_id%s%s FROM %s.%s src%s
		WHERE src.%s IS NOT NULL AND CAST(src.%s AS TEXT)<>''%s
		ORDER BY context_time DESC, source_id DESC LIMIT %d`,
		quoteIdentifier(table.IDColumn), declaredColumnSelect("src", table.TextColumns), contextSQL,
		quoteIdentifier(d.Database.Alias), quoteIdentifier(table.Name), join,
		quoteIdentifier(table.IDColumn), quoteIdentifier(table.IDColumn), bound, walkPageSize)
}

func (d DeclaredCorpus) contextSQL(table vectorTable,
	catalog map[string]map[string]bool) (string, string, string) {
	alias := quoteIdentifier(d.Database.Alias)
	columns := catalog[table.Name]
	timeColumns := qualifiedColumns("src", table.TimeColumns)
	join := ""
	if table.TimeJoin != nil {
		timeline := "timeline"
		join = fmt.Sprintf(" LEFT JOIN %s.%s %s ON CAST(src.%s AS TEXT)=CAST(%s.%s AS TEXT)",
			alias, quoteIdentifier(table.TimeJoin.Table), timeline,
			quoteIdentifier(table.TimeJoin.LocalColumn), timeline,
			quoteIdentifier(table.TimeJoin.ForeignColumn))
		timeColumns = append(timeColumns, qualifiedColumns(timeline, table.TimeJoin.TimeColumns)...)
	}
	timeExpression := "CAST(src." + quoteIdentifier(table.IDColumn) + " AS TEXT)"
	if len(timeColumns) > 0 {
		timeExpression = "COALESCE(" + strings.Join(append(timeColumns, "''"), ",") + ")"
	}
	title, project := "''", "''"
	if d.Database.Plugin != "roca-corpus" && d.Database.Plugin != "roca-ops" {
		return fmt.Sprintf(", %s AS context_title, %s AS context_project, %s AS context_time",
			title, project, timeExpression), join, timeExpression
	}
	switch table.Name {
	case "sessions":
		title = coalesceDeclared(columns, "src", "title")
		project = coalesceDeclared(columns, "src", "project")
	case "exchanges":
		sessions, hasSessions := d.table("sessions")
		sessionColumns := catalog["sessions"]
		if hasSessions && columns["session_id"] && sessionColumns[sessions.IDColumn] {
			title = coalesceDeclared(sessionColumns, "ctx", "title")
			project = coalesceDeclared(sessionColumns, "ctx", "project")
			join += fmt.Sprintf(" LEFT JOIN %s.%s ctx ON ctx.%s = src.%s", alias,
				quoteIdentifier(sessions.Name), quoteIdentifier(sessions.IDColumn), quoteIdentifier("session_id"))
		}
	case "thinking_blocks":
		sessions, hasSessions := d.table("sessions")
		sessionColumns := catalog["sessions"]
		if hasSessions && columns["session_id"] && sessionColumns[sessions.IDColumn] {
			title = coalesceDeclared(sessionColumns, "ctx", "title")
			project = coalesceDeclared(sessionColumns, "ctx", "project")
			join += fmt.Sprintf(" LEFT JOIN %s.%s ctx ON ctx.%s = src.%s", alias,
				quoteIdentifier(sessions.Name), quoteIdentifier(sessions.IDColumn), quoteIdentifier("session_id"))
		}
	case "memories":
		title = coalesceDeclared(columns, "src", "project")
		sessions, hasSessions := d.table("sessions")
		sessionColumns := catalog["sessions"]
		if d.Database.Plugin == "roca-corpus" && hasSessions &&
			columns["source_session"] && sessionColumns[sessions.IDColumn] {
			title = coalesceDeclaredPair(sessionColumns, "ctx", []string{"title"},
				columns, "src", []string{"project"})
			project = coalesceDeclared(sessionColumns, "ctx", "project")
			join += fmt.Sprintf(" LEFT JOIN %s.%s ctx ON ctx.%s = src.%s", alias,
				quoteIdentifier(sessions.Name), quoteIdentifier(sessions.IDColumn), quoteIdentifier("source_session"))
		}
	}
	return fmt.Sprintf(", %s AS context_title, %s AS context_project, %s AS context_time",
		title, project, timeExpression), join, timeExpression
}

func qualifiedColumns(alias string, columns []string) []string {
	result := make([]string, len(columns))
	for index, column := range columns {
		result[index] = alias + "." + quoteIdentifier(column)
	}
	return result
}

func coalesceDeclared(columns map[string]bool, alias string, names ...string) string {
	return coalesceDeclaredPair(columns, alias, names, nil, "", nil)
}

func coalesceDeclaredPair(first map[string]bool, firstAlias string, firstNames []string,
	second map[string]bool, secondAlias string, secondNames []string) string {
	values := make([]string, 0, len(firstNames)+len(secondNames)+1)
	for _, name := range firstNames {
		if first[name] {
			values = append(values, firstAlias+"."+quoteIdentifier(name))
		}
	}
	for _, name := range secondNames {
		if second[name] {
			values = append(values, secondAlias+"."+quoteIdentifier(name))
		}
	}
	values = append(values, "''")
	if len(values) == 1 {
		return values[0]
	}
	return "COALESCE(" + strings.Join(values, ", ") + ")"
}

func declaredColumnSelect(alias string, columns []string) string {
	var b strings.Builder
	prefix := ""
	if alias != "" {
		prefix = alias + "."
	}
	for _, column := range columns {
		b.WriteString(", CAST(")
		b.WriteString(prefix)
		b.WriteString(quoteIdentifier(column))
		b.WriteString(" AS TEXT) AS ")
		b.WriteString(quoteIdentifier(column))
	}
	return b.String()
}

func declaredNonEmptyPredicate(alias string, columns []string) string {
	parts := make([]string, len(columns))
	prefix := ""
	if alias != "" {
		prefix = alias + "."
	}
	for index, column := range columns {
		parts[index] = fmt.Sprintf("trim(COALESCE(CAST(%s%s AS TEXT),'')) <> ''",
			prefix, quoteIdentifier(column))
	}
	if len(parts) == 0 {
		return "1=1"
	}
	return strings.Join(parts, " OR ")
}

func (d DeclaredCorpus) table(name string) (vectorTable, bool) {
	for _, table := range d.Database.Tables {
		if table.Name == name {
			return table, true
		}
	}
	return vectorTable{}, false
}

func (d DeclaredCorpus) hasTable(name string) bool {
	_, ok := d.table(name)
	return ok
}

var errSidecarChanged = errors.New("sidecar source changed")

func databaseFingerprint(path, contract string) (string, error) {
	return incrementality.TargetFingerprint(incrementality.Target{
		Path: path, Kind: "vector-database", ParserVersion: declaredReaderVersion + ":" + contract,
		IncludeSQLiteWAL: true,
	})
}

func unchangedSidecar(path, owner, model, contract, sourceFingerprint string) (Delta, error) {
	store, err := openSQLite(path, true)
	if err != nil {
		if os.IsNotExist(err) || strings.Contains(err.Error(), "unable to open database file") {
			return Delta{}, errSidecarChanged
		}
		return Delta{}, fmt.Errorf("inspect vector sidecar for %s: %w", owner, err)
	}
	defer store.Close()
	metadata, err := readMetadata(store, "owner", "model", "dimensions", "contract", "source_fingerprint")
	if err != nil || metadata["owner"] != owner || metadata["model"] != model ||
		metadata["contract"] != contract || metadata["source_fingerprint"] != sourceFingerprint {
		return Delta{}, errSidecarChanged
	}
	if dimensions, _ := strconv.Atoi(metadata["dimensions"]); dimensions == 0 {
		return Delta{}, errSidecarChanged
	}
	var chunks, sources int
	if err := store.QueryRow(`SELECT COUNT(*) FROM chunks`).Scan(&chunks); err != nil {
		return Delta{}, errSidecarChanged
	}
	if err := store.QueryRow(`SELECT COUNT(*) FROM (SELECT source_kind,source_id FROM chunks GROUP BY source_kind,source_id)`).Scan(&sources); err != nil {
		return Delta{}, errSidecarChanged
	}
	return Delta{Unchanged: chunks, Chunks: chunks, Sources: sources}, nil
}

func readMetadata(db *sql.DB, keys ...string) (map[string]string, error) {
	result := make(map[string]string, len(keys))
	for _, key := range keys {
		var value string
		if err := db.QueryRow(`SELECT value FROM meta WHERE key=?`, key).Scan(&value); err != nil {
			return nil, err
		}
		result[key] = value
	}
	return result, nil
}

func (d vectorDatabase) contractFingerprint() string {
	fields := []string{declaredReaderVersion, d.Plugin, d.Database, d.Alias}
	for _, table := range d.Tables {
		fields = append(fields, table.readerContractFingerprint())
	}
	return incrementality.ContentFingerprint(fields...)
}

func (t vectorTable) readerContractFingerprint() string {
	fields := []string{declaredReaderVersion, t.Name, t.IDColumn}
	fields = append(fields, t.TimeColumns...)
	if t.TimeJoin != nil {
		fields = append(fields, t.TimeJoin.Table, t.TimeJoin.LocalColumn, t.TimeJoin.ForeignColumn)
		fields = append(fields, t.TimeJoin.TimeColumns...)
	}
	fields = append(fields, t.TextColumns...)
	if len(t.Columns) > 0 {
		fields = append(fields, "catalog")
		fields = append(fields, t.Columns...)
	}
	if t.Chunking != nil && (t.Chunking.MaxChars != nil || t.Chunking.OverlapChars != nil) {
		size, overlap := t.chunking()
		fields = append(fields, "chars", strconv.Itoa(size), strconv.Itoa(overlap))
	} else {
		fields = append(fields, "tokens", strconv.Itoa(defaultChunkTokens), strconv.Itoa(defaultOverlapTokens))
	}
	return incrementality.ContentFingerprint(fields...)
}

func (t vectorTable) availableColumns() map[string]bool {
	columns := make(map[string]bool, len(t.Columns)+len(t.TextColumns)+1)
	for _, column := range t.Columns {
		columns[column] = true
	}
	columns[t.IDColumn] = true
	for _, column := range t.TextColumns {
		columns[column] = true
	}
	return columns
}

func (t vectorTable) embeddingContractFingerprint() string {
	fields := []string{declaredReaderVersion, chunkPolicyVersion, t.Name, t.IDColumn, "per-column"}
	fields = append(fields, t.TextColumns...)
	if t.Chunking != nil && (t.Chunking.MaxChars != nil || t.Chunking.OverlapChars != nil) {
		size, overlap := t.chunking()
		fields = append(fields, "chars", strconv.Itoa(size), strconv.Itoa(overlap))
	} else {
		fields = append(fields, "tokens", strconv.Itoa(defaultChunkTokens), strconv.Itoa(defaultOverlapTokens))
	}
	return incrementality.ContentFingerprint(fields...)
}

func (t vectorTable) chunking() (int, int) {
	size, overlap := defaultChunkSize, defaultOverlap
	if t.Chunking == nil {
		return size, overlap
	}
	if t.Chunking.MaxChars != nil {
		size = *t.Chunking.MaxChars
	}
	if t.Chunking.OverlapChars != nil {
		overlap = *t.Chunking.OverlapChars
	}
	if overlap >= size {
		overlap = 0
	}
	return size, overlap
}

func SidecarPath(databasePath string) string {
	extension := filepath.Ext(databasePath)
	return strings.TrimSuffix(databasePath, extension) + ".vector" + extension
}

func sealSidecar(path, owner, model, buildVersion, contract, sourceFingerprint string) error {
	values := map[string]string{
		"owner": owner, "model": model, "version": buildVersion, "contract": contract,
	}
	if sourceFingerprint != "" {
		values["source_fingerprint"] = sourceFingerprint
	}
	return writeSidecarMeta(path, owner, values, nil)
}

// claimSidecar names the index before it is built. Identity written at the end
// of a run is identity a run that never ends never writes, and rows already on
// disk then read as an absent index: the product looks empty when it is not.
// The fingerprint of the source goes in the same breath, because an index that
// has not finished must never claim to already match what it was built from.
func claimSidecar(path, owner, buildVersion, contract string, full bool) error {
	var clear []string
	if full {
		clear = []string{"source_fingerprint"}
	}
	return writeSidecarMeta(path, owner, map[string]string{
		"owner": owner, "version": buildVersion, "contract": contract}, clear)
}

func writeSidecarMeta(path, owner string, values map[string]string, clear []string) error {
	store, err := openSQLite(path, false)
	if err != nil {
		return fmt.Errorf("open vector sidecar for %s: %w", owner, err)
	}
	defer store.Close()
	if err := ensureBaseSchema(store); err != nil {
		return err
	}
	var knownOwner string
	err = store.QueryRow(`SELECT value FROM meta WHERE key='owner'`).Scan(&knownOwner)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("read vector sidecar owner: %w", err)
	}
	if knownOwner != "" && knownOwner != owner {
		return fmt.Errorf("vector sidecar owner is %s, want %s", knownOwner, owner)
	}
	tx, err := store.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for key, value := range values {
		if _, err := tx.Exec(`INSERT OR REPLACE INTO meta(key,value) VALUES (?,?)`, key, value); err != nil {
			return err
		}
	}
	for _, key := range clear {
		if _, err := tx.Exec(`DELETE FROM meta WHERE key=?`, key); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func assertSidecarOwner(path, owner string) error {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return fmt.Errorf("inspect vector sidecar for %s: %w", owner, err)
	}
	store, err := openSQLite(path, true)
	if err != nil {
		return fmt.Errorf("inspect vector sidecar for %s: %w", owner, err)
	}
	defer store.Close()
	var knownOwner string
	err = store.QueryRow(`SELECT value FROM meta WHERE key='owner'`).Scan(&knownOwner)
	if errors.Is(err, sql.ErrNoRows) {
		var schema string
		if schemaErr := store.QueryRow(`SELECT value FROM meta WHERE key='schema'`).Scan(&schema); schemaErr != nil || schema != vectorStorageSchema {
			return fmt.Errorf("existing sidecar for %s is not an owned or interrupted vector index", owner)
		}
		if _, schemaErr := store.Exec(`SELECT id,source_kind,source_id,text_column,chunk_index,fingerprint,locator,updated_at FROM chunks LIMIT 0`); schemaErr != nil {
			return fmt.Errorf("existing sidecar for %s is not an owned or interrupted vector index", owner)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("read vector sidecar owner: %w", err)
	}
	if knownOwner != owner {
		return fmt.Errorf("vector sidecar owner is %s, want %s", knownOwner, owner)
	}
	return nil
}

func (d vectorDatabase) owner() string { return d.Plugin + "/" + d.Database }

func (f Federation) databasePath(database vectorDatabase) string {
	return filepath.Join(f.PluginRoot, database.Plugin, database.Path)
}

func (f Federation) CorpusIndex() (Index, error) {
	for _, database := range f.databases {
		if database.Plugin == "roca-corpus" && database.Database == "corpus" {
			path := SidecarPath(f.databasePath(database))
			model := f.Model
			if model == "" {
				model = ConfiguredModel(path)
			}
			copy := f
			copy.Model = model
			return copy.index(database, DeclaredCorpus{Core: f.Core, Database: database}, path), nil
		}
	}
	return Index{}, fmt.Errorf("the vector registry has no corpus declaration")
}

func (f Federation) ConfiguredModel() string {
	for _, database := range f.databases {
		if database.Plugin == "roca-corpus" && database.Database == "corpus" {
			return ConfiguredModel(SidecarPath(f.databasePath(database)))
		}
	}
	return DefaultModel
}

type CompactFederationReport struct {
	Databases      int   `json:"databases"`
	PagesBefore    int64 `json:"pages_before"`
	PagesAfter     int64 `json:"pages_after"`
	BytesBefore    int64 `json:"bytes_before"`
	BytesAfter     int64 `json:"bytes_after"`
	BytesReclaimed int64 `json:"bytes_reclaimed"`
	LiveChunks     int64 `json:"live_chunks"`
}

func (f Federation) Compact(ctx context.Context) (CompactFederationReport, error) {
	report := CompactFederationReport{}
	for _, database := range f.databases {
		path := SidecarPath(f.databasePath(database))
		if _, err := os.Stat(path); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return report, err
		}
		item, err := Compact(ctx, path)
		if err != nil {
			return report, fmt.Errorf("compact vector sidecar %s: %w", database.owner(), err)
		}
		report.Databases++
		report.PagesBefore += item.PagesBefore
		report.PagesAfter += item.PagesAfter
		report.BytesBefore += item.BytesBefore
		report.BytesAfter += item.BytesAfter
		report.BytesReclaimed += item.BytesReclaimed
		report.LiveChunks += item.LiveChunks
	}
	if report.Databases == 0 {
		return report, fmt.Errorf("vector search is not initialized; run `roca vector install`")
	}
	return report, nil
}

func (f Federation) HasSidecars() bool {
	for _, database := range f.databases {
		if _, err := os.Stat(SidecarPath(f.databasePath(database))); err == nil {
			return true
		}
	}
	return false
}

func (f Federation) RemoveLegacyMonolith(stateDir string) error {
	legacy := filepath.Join(stateDir, DatabaseFilename)
	for _, suffix := range []string{"", "-wal", "-shm", "-journal", ".index.lock"} {
		if err := os.Remove(legacy + suffix); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove superseded central vector index: %w", err)
		}
	}
	return nil
}

type FederatedWorker struct {
	Federation  Federation
	DataDir     string
	PullModel   bool
	Notifier    Notifier
	WaitForCalm func(context.Context) error
}

func (w FederatedWorker) Run(ctx context.Context) Completion {
	started := time.Now().UTC()
	completion := Completion{ExitStatus: 0, Model: w.Federation.Model, StartedAt: started}
	failIf := func(err error) {
		if err != nil && completion.Error == "" {
			completion.ExitStatus, completion.Error = 1, err.Error()
		}
	}
	if w.PullModel {
		failIf(w.Federation.Embedder.Pull(ctx, w.Federation.Model))
	}
	if completion.Error == "" && w.WaitForCalm != nil {
		failIf(w.WaitForCalm(ctx))
	}
	if completion.Error == "" {
		legacy := filepath.Join(w.DataDir, DatabaseFilename)
		if _, err := os.Stat(legacy); err == nil {
			if err := w.Federation.seedSidecarsFromLegacyMonolith(ctx, legacy); err != nil && w.Federation.Notice != nil {
				w.Federation.Notice("legacy vector reuse was skipped: " + err.Error())
			}
		}
	}
	if completion.Error == "" {
		delta, err := w.Federation.Ingest(ctx, "")
		completion.Delta = delta.Delta
		failIf(err)
	}
	if completion.Error == "" {
		failIf(w.Federation.RemoveLegacyMonolith(w.DataDir))
	}
	completion.FinishedAt = time.Now().UTC()
	if err := writeCompletion(w.DataDir, completion); err != nil {
		failIf(err)
	}
	if w.Notifier != nil {
		_ = w.Notifier.Notify(ctx, completion)
	}
	return completion
}

func validIdentifier(value string) bool { return sqlIdentifier.MatchString(value) }
