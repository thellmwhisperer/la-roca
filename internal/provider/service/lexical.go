package service

import (
	"context"

	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/thellmwhisperer/la-roca/internal/provider/plugin"
	"github.com/thellmwhisperer/la-roca/internal/provider/query"
	"github.com/thellmwhisperer/la-roca/internal/provider/query/sqlgate"

	"github.com/thellmwhisperer/la-roca/internal/store/search"
)

func (s *Service) SearchByTerm(ctx context.Context, plan query.Plan, method string,
	maxChars int, matchAny bool, route PluginRoute) (columns []string, rows []map[string]any, stmt string,
	provenance *search.Provenance, warnings []string, err error) {

	if s.servingLayout() != LayoutLegacyServing && method != search.MethodLike {
		if err := s.ensureHubSearch(ctx); err != nil {
			if recoverErr := s.recoverHubSearchFailure(err); recoverErr != nil {
				return nil, nil, "", nil, nil, recoverErr
			}
		}
	}
	limit := plan.Limit
	if limit <= 0 {
		limit = 10
	}
	if !route.IncludeCore {
		databases := bundledSearchDatabases(route)
		if len(databases) == 0 {
			return nil, nil, "", nil, nil, nil
		}
		rows, stmt, warnings, err := s.residentMemoryRows(ctx, plan, maxChars, matchAny, limit, databases, "")
		if err != nil {
			return nil, nil, "", nil, warnings, err
		}
		columns := []string{"source", "id", "author", "text", "created_at"}
		if len(rows) > 0 {
			columns, rows = EnsureDatabaseColumn(columns, rows, fallbackDatabase(stmt, databases))
		}
		return columns, rows, stmt, nil, warnings, nil
	}
	gate, err := s.TheGate()
	if err != nil {
		return nil, nil, "", nil, nil, err
	}
	engine := &search.Engine{DB: s.db, Validate: gate.Validate}

	var sqlLexical string
	if method != search.MethodLike {
		if matchAny {
			sqlLexical, err = query.RenderSQLFTSAny(plan, s.registry.SearchExcluded(), limit)
		} else {
			sqlLexical, err = query.RenderSQLFTS(plan, s.registry.SearchExcluded(), limit)
		}
		if err != nil {
			return nil, nil, "", nil, nil, err
		}
	}

	request := search.Request{
		Term:       plan.Term,
		SQLLexical: sqlLexical,
		Method:     method,
		Limit:      limit,
	}
	result, err := engine.Search(ctx, request)
	if err != nil {
		return nil, nil, "", nil, nil, err
	}
	if result.Provenance.Method == search.MethodFTS && s.servingLayout() == LayoutShadowEqual {
		hubEngine := &search.Engine{DB: s.hubDB, Validate: gate.Validate}
		hubResult, hubErr := hubEngine.Search(ctx, request)
		equal := hubResult.Provenance.Method == result.Provenance.Method &&
			reflect.DeepEqual(result.Rows, hubResult.Rows)
		s.compareShadow(equal, hubErr, "shadow lexical rows differ")
	}

	if result.Provenance.Method == search.MethodLike {
		like, err := query.RenderSQLLike(plan, s.registry.SearchExcluded())
		if err != nil {
			return nil, nil, "", nil, nil, err
		}
		validated, err := gate.Validate(like)
		if err != nil {
			return nil, nil, "", nil, nil, err
		}
		columns, rows, err := s.execute(ctx, validated, plan.Term, maxChars)
		if err != nil {
			return nil, nil, "", nil, nil, err
		}
		// The LIKE floor requires every word, so the attached half requires every
		// word too: merging a looser search with a stricter one is not one search.
		return s.withResidentMemorySearch(ctx, plan, maxChars, false, limit,
			columns, rows, validated, &result.Provenance, route)
	}

	columns = []string{"source", "id", "author", "text", "created_at"}
	rows = make([]map[string]any, 0, len(result.Rows))
	for _, row := range result.Rows {
		var date any
		if row.Date.Valid {
			date = row.Date.String
		}
		var author any
		if row.Author.Valid {
			author = row.Author.String
		}
		rows = append(rows, map[string]any{
			"source":     row.Source,
			"id":         row.ID,
			"author":     author,
			"text":       Truncate(row.Text, maxChars, plan.Term),
			"created_at": date,
		})
	}
	return s.withResidentMemorySearch(ctx, plan, maxChars, matchAny, limit,
		columns, rows, result.SQL, &result.Provenance, route)
}

func (s *Service) withResidentMemorySearch(ctx context.Context, plan query.Plan, maxChars int,
	matchAny bool, limit int, columns []string, rows []map[string]any, stmt string,
	provenance *search.Provenance, route PluginRoute) ([]string, []map[string]any, string, *search.Provenance,
	[]string, error) {
	databases := bundledSearchDatabases(route)
	if len(databases) == 0 {
		return columns, rows, stmt, provenance, nil, nil
	}
	// The provenance column belongs to the answer's shape and not to its content:
	// once a bundled database is in scope, every run of the same command declares
	// the same header, whether or not that half matched anything this time.
	if route.IncludeCore {
		columns, rows = EnsureDatabaseColumn(columns, rows, "core")
	}
	residentRows, declared, warnings, err := s.residentMemoryRows(
		ctx, plan, maxChars, matchAny, limit, databases, stmt)
	if err != nil {
		return columns, rows, stmt, provenance, warnings, nil
	}
	if len(residentRows) == 0 {
		return columns, rows, stmt, provenance, warnings, nil
	}
	rows = append(rows, residentRows...)
	rows = dedupRows(strings.ReplaceAll(plan.Term, "+", " "), columns, rows)
	rows = limitMergedSearchRows(rows, limit)
	return columns, rows, declared, provenance, warnings, nil
}

func (s *Service) residentMemoryRows(ctx context.Context, plan query.Plan, maxChars int,
	matchAny bool, limit int, databases []plugin.Database,
	core string) ([]map[string]any, string, []string, error) {
	gate, closeGate, err := s.GateFor(core != "", databases)
	if err != nil {
		return nil, core, nil, err
	}
	defer closeGate()
	var statements []string
	if core != "" {
		statements = append(statements, core)
	}
	var rows []map[string]any
	var warnings []string
	for _, database := range databases {
		var statement string
		switch database.Name {
		case rocaOpsPluginName:
			statement, err = query.RenderSQLAttachedMemoryLike(plan,
				s.registry.SearchExcluded(), database.Schema, limit, matchAny)
		case rocaCorpusPluginName:
			statement, err = query.RenderSQLAttachedCorpusLike(plan,
				s.registry.SearchExcluded(), database.Schema, limit, matchAny)
		}
		if err == nil {
			statement, err = gate.Validate(statement)
		}
		var Found []map[string]any
		if err == nil {
			_, Found, err = s.ExecuteWithPlugins(ctx, statement, plan.Term, maxChars, databases)
		}
		if err != nil {
			warnings = append(warnings, fmt.Sprintf(
				"%s could not be searched: %v; the answer omits that database",
				database.Source(), err))
			continue
		}
		rows = append(rows, Found...)
		statements = append(statements, statement)
	}
	return rows, declaredSearchSQL(gate, statements, limit), warnings, nil
}

func limitMergedSearchRows(rows []map[string]any, limit int) []map[string]any {
	ordered := slices.Clone(rows)
	slices.SortStableFunc(ordered, func(a, b map[string]any) int {
		created := func(row map[string]any) string {
			if row["created_at"] == nil {
				return ""
			}
			return fmt.Sprint(row["created_at"])
		}
		return strings.Compare(created(b), created(a))
	})
	if limit > 0 && len(ordered) > limit {
		return ordered[:limit]
	}
	return ordered
}

func declaredSearchSQL(gate *sqlgate.Gate, statements []string, limit int) string {
	if len(statements) == 0 {
		return ""
	}
	if len(statements) < 2 {
		return statements[0]
	}
	merged, err := query.RenderSearchUnionParts(statements, limit)
	if err != nil {
		return statements[0]
	}
	validated, err := gate.Validate(merged)
	if err != nil {
		return statements[0]
	}
	return validated
}
