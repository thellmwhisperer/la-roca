package service

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/thellmwhisperer/la-roca/internal/provider/plugin"
	"github.com/thellmwhisperer/la-roca/internal/provider/query"
	"github.com/thellmwhisperer/la-roca/internal/store/search"
)

// SearchRequest is the zero-inference hybrid query: FTS plus optional vector,
// fused with RRF. No answering model runs on this path.
type SearchRequest struct {
	Question    string
	Databases   []string
	Top         int
	RequireBoth bool
	MaxChars    int
	// Overlay is the flag write on top of the service's resolved [query] knobs.
	Overlay search.Overlay
}

// SearchHit is one fused source with the evidence that placed it.
type SearchHit struct {
	Rank        int      `json:"rank"`
	Source      string   `json:"source"`
	Database    string   `json:"database"`
	Table       string   `json:"table"`
	ID          string   `json:"id"`
	Legs        []string `json:"legs"`
	Consensus   bool     `json:"consensus"`
	RRF         float64  `json:"rrf"`
	VectorScore *float64 `json:"vector_score,omitempty"`
	VectorRank  *int     `json:"vector_rank,omitempty"`
	FTSRank     *int     `json:"fts_rank,omitempty"`
	Snippet     string   `json:"snippet"`
}

// SearchResult is the AXI envelope for `roca query`.
type SearchResult struct {
	Question    string      `json:"question"`
	Engines     []string    `json:"engines"`
	Terms       []string    `json:"terms,omitempty"`
	Notices     []string    `json:"notices,omitempty"`
	Databases   []string    `json:"databases,omitempty"`
	RequireBoth bool        `json:"require_both,omitempty"`
	Top         int         `json:"top"`
	Hits        []SearchHit `json:"hits"`
	RowCount    int         `json:"row_count"`
	MaxChars    int         `json:"-"`
	LatencyMS   int64       `json:"latency_ms"`
	Version     string      `json:"version"`
	SourceSHA   string      `json:"source_sha"`
}

// VectorHit is one federated vector neighbor, matching the plugin JSON.
type VectorHit struct {
	Rank     int     `json:"rank"`
	Score    float64 `json:"score"`
	Database string  `json:"database"`
	Table    string  `json:"table"`
	ID       string  `json:"id"`
	Text     string  `json:"text"`
}

// VectorHits is the vector leg as HybridSearch consumes it.
type VectorHits struct {
	Results     []VectorHit `json:"results"`
	Notices     []string    `json:"notices"`
	Executed    bool        `json:"vector_executed"`
	MixedModels bool        `json:"mixed_models"`
}

// VectorSearchFunc runs the vector leg. Tests inject it; production shells to
// the existing `roca-vector query` plumbing when VectorEnabled is set.
type VectorSearchFunc func(ctx context.Context, question string, k int, databases string) (VectorHits, error)

// VectorLeg is one PluginVectorQuery invocation.
type VectorLeg struct {
	K         int
	Databases string
	Expand    bool
	Templates []string
	MinScore  float64
}

type searchSurface struct {
	Database    string
	Schema      string
	Table       string
	IDColumn    string
	TextColumns []string
	Columns     []string
	FTSTable    string
}

// Search is the hybrid retrieval seat: rarity-selected FTS, template-expanded
// vector when a sidecar exists, RRF fusion, labeled evidence.
func (s *Service) Search(ctx context.Context, req SearchRequest) (SearchResult, error) {
	start := time.Now()
	if err := query.ValidateQuestion(req.Question); err != nil {
		return SearchResult{}, err
	}
	settings := s.QuerySettings().Apply(req.Overlay)
	top := req.Top
	if top <= 0 {
		top = settings.Top
	}
	maxChars := TextBudget(req.MaxChars)
	result := SearchResult{
		Question:    req.Question,
		Hits:        []SearchHit{},
		Top:         top,
		RequireBoth: req.RequireBoth,
		MaxChars:    maxChars,
		Version:     s.opts.Version,
		SourceSHA:   s.opts.Commit,
	}
	if _, err := s.EnsureSchema(ctx); err != nil {
		return result, err
	}
	inventory := s.InventoryRoute(ctx)
	defer inventory.CloseOnDemand()
	route, err := QuestionRoute(req.Databases, inventory)
	if err != nil {
		return result, err
	}
	if s.PluginsActive() {
		result.Databases = attachedNames(route.IncludeCore, route.Databases)
	} else if route.IncludeCore {
		result.Databases = []string{ScopeCore}
	}
	result.Notices = append(result.Notices, route.Warnings...)

	surfaces := collectSurfaces(route)
	tokens := uniqueTokens(search.Tokenize(req.Question))
	terms, termErr := s.selectTerms(ctx, route, surfaces, tokens, maxChars, settings)
	if termErr != nil {
		return result, termErr
	}
	result.Terms = terms

	legs, legsErr := s.runSearchLegs(ctx, req, route, surfaces, terms, maxChars, settings)
	if legsErr != nil {
		return result, legsErr
	}
	if legs.ftsRan {
		result.Engines = append(result.Engines, search.LegFTS)
	}
	result.Notices = append(result.Notices, legs.notices...)
	if legs.vectorOK {
		result.Engines = append(result.Engines, search.LegVector)
	}
	if len(result.Engines) == 0 && len(surfaces) > 0 {
		// A zero-DF query has no MATCH expression to execute. When vectors are
		// unavailable, still name FTS as the active degradation path so an empty
		// machine-readable envelope does not report an unknown search engine.
		result.Engines = append(result.Engines, search.LegFTS)
	}

	fused := search.FuseRRF(legs.vectorDocs, legs.ftsDocs, settings.RRFK)
	if req.RequireBoth {
		kept := make([]search.FusedDoc, 0, len(fused))
		for _, doc := range fused {
			if doc.Consensus {
				kept = append(kept, doc)
			}
		}
		fused = kept
	}
	if len(fused) > top {
		fused = fused[:top]
	}
	result.Hits = make([]SearchHit, 0, len(fused))
	for index, doc := range fused {
		hit := SearchHit{
			Rank:      index + 1,
			Source:    doc.Key,
			Database:  doc.Database,
			Table:     doc.Table,
			ID:        doc.ID,
			Legs:      doc.Legs,
			Consensus: doc.Consensus,
			RRF:       doc.Score,
			Snippet:   Truncate(doc.Snippet, maxChars, req.Question),
		}
		if doc.HasVector {
			score, rank := doc.VectorScore, doc.VectorRank
			hit.VectorScore, hit.VectorRank = &score, &rank
		}
		if doc.HasFTS {
			rank := doc.FTSRank
			hit.FTSRank = &rank
		}
		result.Hits = append(result.Hits, hit)
	}
	result.RowCount = len(result.Hits)
	result.LatencyMS = time.Since(start).Milliseconds()
	return result, nil
}

func uniqueTokens(tokens []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(tokens))
	for _, token := range tokens {
		if token == "" || seen[token] {
			continue
		}
		seen[token] = true
		out = append(out, token)
	}
	return out
}

func vectorRanked(hits []VectorHit, minScore float64) []search.RankedDoc {
	docs := make([]search.RankedDoc, 0, len(hits))
	for _, hit := range hits {
		table := hit.Table
		if table == "" {
			continue
		}
		docs = append(docs, search.RankedDoc{
			Key:      search.SourceKey(hit.Database, table, hit.ID),
			Rank:     hit.Rank,
			Score:    hit.Score,
			Database: hit.Database,
			Table:    table,
			ID:       hit.ID,
			Snippet:  hit.Text,
		})
	}
	return search.ApplyVectorFloor(docs, minScore)
}

type searchLegs struct {
	ftsDocs    []search.RankedDoc
	ftsRan     bool
	vectorDocs []search.RankedDoc
	vectorOK   bool
	notices    []string
}

func (s *Service) runSearchLegs(ctx context.Context, req SearchRequest, route PluginRoute,
	surfaces []searchSurface, terms []string, maxChars int, settings search.Settings) (searchLegs, error) {
	runFTS := func() ([]search.RankedDoc, bool, error) {
		docs, err := s.searchFTS(ctx, route, surfaces, terms, maxChars, settings)
		if err != nil {
			return nil, false, err
		}
		return docs, len(surfaces) > 0 && len(terms) > 0, nil
	}
	runVector := func() ([]search.RankedDoc, []string, bool) {
		hits, notices, ok, err := s.searchVector(ctx, req, settings)
		if err != nil {
			return nil, []string{"vector search unavailable: " + err.Error()}, false
		}
		if !ok {
			return nil, notices, false
		}
		return vectorRanked(hits, settings.MinVectorScore), notices, true
	}
	if !settings.ParallelLegs {
		ftsDocs, ftsRan, err := runFTS()
		if err != nil {
			return searchLegs{}, err
		}
		vectorDocs, notices, vectorOK := runVector()
		return searchLegs{ftsDocs: ftsDocs, ftsRan: ftsRan, vectorDocs: vectorDocs,
			vectorOK: vectorOK, notices: notices}, nil
	}
	var (
		ftsDocs    []search.RankedDoc
		ftsRan     bool
		ftsErr     error
		vectorDocs []search.RankedDoc
		notices    []string
		vectorOK   bool
		wg         sync.WaitGroup
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		ftsDocs, ftsRan, ftsErr = runFTS()
	}()
	go func() {
		defer wg.Done()
		vectorDocs, notices, vectorOK = runVector()
	}()
	wg.Wait()
	return searchLegs{ftsDocs: ftsDocs, ftsRan: ftsRan, vectorDocs: vectorDocs,
		vectorOK: vectorOK, notices: notices}, ftsErr
}

func (s *Service) searchVector(ctx context.Context, req SearchRequest, settings search.Settings) (
	[]VectorHit, []string, bool, error) {
	databases := strings.Join(req.Databases, ",")
	var hits VectorHits
	var err error
	switch {
	case s.opts.VectorSearch != nil:
		hits, err = s.opts.VectorSearch(ctx, req.Question, settings.Oversample, databases)
	case s.opts.VectorEnabled:
		hits, err = PluginVectorQuery(ctx, s.opts.DBPath, req.Question, VectorLeg{
			K: settings.Oversample, Databases: databases,
			Expand: settings.ExpandTemplates(), Templates: settings.TemplateList,
			MinScore: settings.MinVectorScore,
		})
	default:
		return nil, []string{"vector index is not installed; continuing with FTS-only"}, false, nil
	}
	if err != nil {
		return nil, nil, false, err
	}
	if hits.MixedModels {
		notices := append(append([]string{}, hits.Notices...),
			"mixed-model vector results cannot be fused; continuing with FTS-only")
		return nil, notices, false, nil
	}
	if hits.Executed || len(hits.Results) > 0 {
		return hits.Results, hits.Notices, true, nil
	}
	return nil, hits.Notices, false, nil
}

func collectSurfaces(route PluginRoute) []searchSurface {
	var surfaces []searchSurface
	if route.IncludeCore {
		surfaces = append(surfaces, surfacesFromTables(ScopeCore, "", coreSearchTables(), nil)...)
	}
	for _, database := range route.Databases {
		name := scopeName(database)
		surfaces = append(surfaces, surfacesFromTables(name, database.Schema, database.Tables, database.VectorTables)...)
	}
	return surfaces
}

func coreSearchTables() []plugin.Table {
	schema := theModelsSchema()
	tables := make([]plugin.Table, 0, len(schema.Tables))
	for _, table := range schema.Tables {
		tables = append(tables, plugin.Table{
			Name: table.Name, Columns: append([]string(nil), table.Columns...), FTS5: table.FTS5,
		})
	}
	return tables
}

func surfacesFromTables(database, schema string, tables []plugin.Table, vectors []plugin.VectorTable) []searchSurface {
	var surfaces []searchSurface
	paired := map[string]bool{}
	for _, vector := range vectors {
		fts, ok := findFTSTable(tables, vector.Name, vector.TextColumns)
		if !ok {
			continue
		}
		paired[fts.Name] = true
		surfaces = append(surfaces, searchSurface{
			Database: database, Schema: schema, Table: vector.Name,
			IDColumn: vector.IDColumn, TextColumns: append([]string(nil), vector.TextColumns...),
			Columns: append([]string(nil), sourceColumns(tables, vector.Name)...), FTSTable: fts.Name,
		})
	}
	if len(vectors) > 0 {
		return surfaces
	}
	for _, table := range tables {
		if !table.FTS5 || paired[table.Name] {
			continue
		}
		source, ok := findSourceTable(tables, table)
		if !ok {
			continue
		}
		surfaces = append(surfaces, searchSurface{
			Database: database, Schema: schema, Table: source.Name,
			IDColumn: inferIDColumn(source.Columns), TextColumns: append([]string(nil), table.Columns...),
			Columns: append([]string(nil), source.Columns...), FTSTable: table.Name,
		})
	}
	return surfaces
}

func sourceColumns(tables []plugin.Table, name string) []string {
	for _, table := range tables {
		if !table.FTS5 && table.Name == name {
			return table.Columns
		}
	}
	return nil
}

func findFTSTable(tables []plugin.Table, source string, textColumns []string) (plugin.Table, bool) {
	want := source + "_fts"
	for _, table := range tables {
		if table.FTS5 && table.Name == want {
			return table, true
		}
	}
	var matches []plugin.Table
	for _, table := range tables {
		if table.FTS5 && columnsCovered(table.Columns, textColumns) {
			matches = append(matches, table)
		}
	}
	if len(matches) == 1 {
		return matches[0], true
	}
	for _, table := range tables {
		if !table.FTS5 {
			continue
		}
		stem := strings.TrimSuffix(table.Name, "_fts")
		if stem != "" && strings.HasPrefix(source, stem) {
			return table, true
		}
	}
	return plugin.Table{}, false
}

func findSourceTable(tables []plugin.Table, fts plugin.Table) (plugin.Table, bool) {
	stem := strings.TrimSuffix(fts.Name, "_fts")
	for _, table := range tables {
		if !table.FTS5 && table.Name == stem {
			return table, true
		}
	}
	var matches []plugin.Table
	for _, table := range tables {
		if table.FTS5 {
			continue
		}
		if columnsCovered(fts.Columns, table.Columns) &&
			(stem == "" || strings.HasPrefix(table.Name, stem)) {
			matches = append(matches, table)
		}
	}
	if len(matches) == 1 {
		return matches[0], true
	}
	return plugin.Table{}, false
}

func columnsCovered(have, catalog []string) bool {
	if len(have) == 0 {
		return false
	}
	allowed := map[string]bool{}
	for _, column := range catalog {
		allowed[column] = true
	}
	for _, column := range have {
		if !allowed[column] {
			return false
		}
	}
	return true
}

func inferIDColumn(columns []string) string {
	for _, column := range columns {
		if column == "id" {
			return column
		}
	}
	for _, column := range columns {
		if strings.HasSuffix(column, "_id") {
			return column
		}
	}
	if len(columns) > 0 {
		return columns[0]
	}
	return "rowid"
}

func (s *Service) selectTerms(ctx context.Context, route PluginRoute, surfaces []searchSurface,
	tokens []string, maxChars int, settings search.Settings) ([]string, error) {
	if len(tokens) == 0 || len(surfaces) == 0 {
		return nil, nil
	}
	branches := make([]string, 0, len(surfaces)*(len(tokens)+1))
	for _, surface := range surfaces {
		branches = append(branches, "SELECT -1 AS token_index, 0 AS docs, COUNT(*) AS corpus_docs FROM "+
			qualified(surface.Schema, surface.Table))
	}
	valid := make([]bool, len(tokens))
	for index, token := range tokens {
		match := search.MatchExpression(token, search.MatchAll)
		if match == "" {
			continue
		}
		valid[index] = true
		for _, surface := range surfaces {
			branches = append(branches, "SELECT "+strconv.Itoa(index)+
				" AS token_index, COUNT(*) AS docs, 0 AS corpus_docs FROM "+
				qualified(surface.Schema, surface.FTSTable)+" WHERE "+
				quoteIdent(surface.FTSTable)+" MATCH "+sqlString(match))
		}
	}
	statement := "SELECT token_index, SUM(docs) AS docs, SUM(corpus_docs) AS corpus_docs FROM (" +
		strings.Join(branches, " UNION ALL ") + ") GROUP BY token_index ORDER BY token_index"
	rows, err := s.runSearchSQL(ctx, route, statement, maxChars)
	if err != nil {
		return nil, err
	}
	corpusDocs := 0
	documentCounts := make(map[int]int, len(tokens))
	for _, row := range rows {
		index := scalarInt(row["token_index"])
		if index < 0 {
			corpusDocs = scalarInt(row["corpus_docs"])
			continue
		}
		documentCounts[index] = scalarInt(row["docs"])
	}
	stats := make([]search.TermStat, 0, len(tokens))
	for index, token := range tokens {
		if valid[index] {
			stats = append(stats, search.TermStat{Term: token, Docs: documentCounts[index]})
		}
	}
	return search.SelectRareTerms(stats, corpusDocs, settings.MaxDFRatio, settings.MaxRareTerms), nil
}

func (s *Service) searchFTS(ctx context.Context, route PluginRoute, surfaces []searchSurface,
	terms []string, maxChars int, settings search.Settings) ([]search.RankedDoc, error) {
	if len(terms) == 0 || len(surfaces) == 0 {
		return nil, nil
	}
	match := search.MatchExpression(strings.Join(terms, "+"), search.MatchAll)
	if match == "" {
		return nil, nil
	}
	branches := make([]string, 0, len(surfaces))
	for _, surface := range surfaces {
		branches = append(branches, ftsBranch(surface, match))
	}
	statement := "SELECT database, \"table\", id, snippet, rango FROM (" +
		strings.Join(branches, " UNION ALL ") + ") ORDER BY rango LIMIT " +
		strconv.Itoa(settings.Oversample)
	rows, err := s.runSearchSQL(ctx, route, statement, maxChars)
	if err != nil {
		return nil, err
	}
	docs := make([]search.RankedDoc, 0, len(rows))
	for index, row := range rows {
		docs = append(docs, search.RankedDoc{
			Database: fmt.Sprint(row["database"]),
			Table:    fmt.Sprint(row["table"]),
			ID:       fmt.Sprint(row["id"]),
			Snippet:  fmt.Sprint(row["snippet"]),
			Rank:     index + 1,
		})
		docs[len(docs)-1].Key = search.SourceKey(docs[len(docs)-1].Database,
			docs[len(docs)-1].Table, docs[len(docs)-1].ID)
	}
	return search.CollapseBestRank(docs), nil
}

func ftsBranch(surface searchSurface, match string) string {
	alias := "s"
	fts := qualified(surface.Schema, surface.FTSTable)
	source := qualified(surface.Schema, surface.Table)
	current := ""
	if columnsCovered([]string{surface.IDColumn, "supersedes"}, surface.Columns) {
		current = fmt.Sprintf(" WHERE %s.%s NOT IN (SELECT %s FROM %s WHERE %s IS NOT NULL)",
			alias, quoteIdent(surface.IDColumn), quoteIdent("supersedes"), source, quoteIdent("supersedes"))
	}
	return fmt.Sprintf(
		"SELECT %s AS database, %s AS \"table\", CAST(%s.%s AS TEXT) AS id, %s AS snippet, f.rango AS rango "+
			"FROM (SELECT rowid AS fila, bm25(%s) AS rango FROM %s WHERE %s MATCH %s) AS f "+
			"JOIN %s AS %s ON %s.rowid = f.fila%s",
		sqlString(surface.Database), sqlString(surface.Table),
		alias, quoteIdent(surface.IDColumn), snippetExpr(alias, surface.TextColumns),
		quoteIdent(surface.FTSTable), fts, quoteIdent(surface.FTSTable), sqlString(match),
		source, alias, alias, current)
}

func snippetExpr(alias string, columns []string) string {
	if len(columns) == 0 {
		return "''"
	}
	parts := make([]string, len(columns))
	for i, column := range columns {
		parts[i] = fmt.Sprintf("COALESCE(CAST(%s.%s AS TEXT),'')", alias, quoteIdent(column))
	}
	return "trim(" + strings.Join(parts, " || char(10) || char(10) || ") + ")"
}

func qualified(schema, table string) string {
	if schema == "" {
		return quoteIdent(table)
	}
	return quoteIdent(schema) + "." + quoteIdent(table)
}

func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

func sqlString(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func scalarInt(raw any) int {
	switch value := raw.(type) {
	case int64:
		return int(value)
	case int:
		return value
	case float64:
		return int(value)
	case json.Number:
		n, _ := value.Int64()
		return int(n)
	default:
		n, _ := strconv.Atoi(fmt.Sprint(value))
		return n
	}
}

func (s *Service) runSearchSQL(ctx context.Context, route PluginRoute, statement string,
	maxChars int) ([]map[string]any, error) {
	if route.IncludeCore {
		if err := s.ensureHubSearchViews(ctx); err != nil {
			return nil, err
		}
	}
	gate, closeGate, err := s.GateFor(route.IncludeCore, route.Databases)
	if err != nil {
		return nil, err
	}
	defer closeGate()
	validated, err := gate.Validate(statement)
	if err != nil {
		return nil, err
	}
	_, rows, err := s.ExecuteWithPlugins(ctx, validated, "", maxChars, route.Databases)
	return rows, err
}

// PluginVectorSearch shells out to `roca-vector query --expand-templates`.
func PluginVectorSearch(dbPath string) VectorSearchFunc {
	return func(ctx context.Context, question string, k int, databases string) (VectorHits, error) {
		return PluginVectorQuery(ctx, dbPath, question, VectorLeg{
			K: k, Databases: databases, Expand: true, MinScore: search.MinVectorScore,
		})
	}
}

// PluginVectorQuery shells out to `roca-vector query` with the resolved knobs.
func PluginVectorQuery(ctx context.Context, dbPath, question string, leg VectorLeg) (VectorHits, error) {
	path, err := exec.LookPath("roca-vector")
	if err != nil {
		return VectorHits{Notices: []string{
			"vector plugin is not installed; continuing with FTS-only",
		}}, nil
	}
	args := []string{"--json", "query"}
	if leg.Expand {
		args = append(args, "--expand-templates")
		for _, template := range leg.Templates {
			args = append(args, "--template", template)
		}
	}
	args = append(args, "--min-score", strconv.FormatFloat(leg.MinScore, 'f', -1, 64))
	if strings.TrimSpace(leg.Databases) != "" {
		args = append(args, "--databases", leg.Databases)
	}
	args = append(args, question, strconv.Itoa(leg.K))
	if strings.TrimSpace(dbPath) != "" {
		args = append([]string{"--db-path", dbPath}, args...)
	}
	cmd := exec.CommandContext(ctx, path, args...)
	out, runErr := cmd.Output()
	if runErr != nil {
		message := runErr.Error()
		if exit, ok := runErr.(*exec.ExitError); ok && len(exit.Stderr) > 0 {
			message = strings.TrimSpace(string(exit.Stderr))
		}
		return VectorHits{Notices: []string{"vector search unavailable: " + message}}, nil
	}
	var envelope struct {
		Results        []VectorHit `json:"results"`
		Notices        []string    `json:"notices"`
		VectorExecuted bool        `json:"vector_executed"`
		MixedModels    bool        `json:"mixed_models"`
	}
	if err := json.Unmarshal(out, &envelope); err != nil {
		return VectorHits{}, fmt.Errorf("decode vector query: %w", err)
	}
	return VectorHits{Results: envelope.Results, Notices: envelope.Notices,
		Executed: envelope.VectorExecuted, MixedModels: envelope.MixedModels}, nil
}
