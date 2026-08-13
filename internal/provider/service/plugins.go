package service

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/thellmwhisperer/la-roca/internal/provider/plugin"
	"github.com/thellmwhisperer/la-roca/internal/provider/query"
	"github.com/thellmwhisperer/la-roca/internal/provider/query/sqlgate"
)

type pluginRoute struct {
	databases []plugin.Database
	omitted   []plugin.Descriptor
	warnings  []string
}

func (s *Service) pluginsForQuestion(ctx context.Context, question string) pluginRoute {
	if !s.opts.PluginsEnabled {
		return pluginRoute{}
	}
	candidates, warnings := plugin.Discover(s.opts.PluginDir)
	ranked, _ := plugin.Relevant(question, candidates, len(candidates))
	return validatePluginRoute(ctx, ranked, warnings)
}

func (s *Service) pluginsForSQL(ctx context.Context, statement string) pluginRoute {
	if !s.opts.PluginsEnabled {
		return pluginRoute{}
	}
	candidates, warnings := plugin.Discover(s.opts.PluginDir)
	referenced, _ := plugin.Referenced(statement, candidates, len(candidates))
	return validatePluginRoute(ctx, referenced, warnings)
}

func validatePluginRoute(ctx context.Context, candidates []plugin.Descriptor,
	warnings []string) pluginRoute {
	route := pluginRoute{warnings: slices.Clone(warnings)}
	for _, candidate := range candidates {
		if len(route.databases) == plugin.MaxAttached {
			route.omitted = append(route.omitted, candidate)
			continue
		}
		database, err := plugin.Validate(ctx, candidate)
		if err != nil {
			route.warnings = append(route.warnings, fmt.Sprintf(
				"plugin %s semantic layer does not match its database: %v; plugin skipped",
				candidate.Name, err))
			continue
		}
		route.databases = append(route.databases, database)
	}
	if len(route.omitted) > 0 {
		route.warnings = append(route.warnings, fmt.Sprintf(
			"SQLite attachment limit is %d; omitted relevant databases: %s",
			plugin.MaxAttached, strings.Join(route.omittedSources(), ", ")))
	}
	return route
}

func (r pluginRoute) consulted() []string {
	consulted := make([]string, 1, len(r.databases)+1)
	consulted[0] = "core"
	for _, database := range r.databases {
		consulted = append(consulted, database.Source())
	}
	return consulted
}

func (r pluginRoute) omittedSources() []string {
	omitted := make([]string, 0, len(r.omitted))
	for _, descriptor := range r.omitted {
		omitted = append(omitted, descriptor.Source())
	}
	return omitted
}

func (s *Service) gateFor(databases []plugin.Database) (*sqlgate.Gate, func(), error) {
	if len(databases) == 0 {
		gate, err := s.theGate()
		return gate, func() {}, err
	}
	schemas := make([]sqlgate.Schema, 0, len(databases))
	for _, database := range databases {
		tables := make([]sqlgate.Table, 0, len(database.Tables))
		for _, table := range database.Tables {
			tables = append(tables, sqlgate.Table{Name: table.Name, Columns: table.Columns})
		}
		schemas = append(schemas, sqlgate.Schema{Name: database.Schema, Tables: tables})
	}
	gate, err := sqlgate.OpenWithSchemas(schemas)
	if err != nil {
		return nil, func() {}, err
	}
	return gate, func() { _ = gate.Close() }, nil
}

func schemaWithPlugins(databases []plugin.Database) query.Schema {
	base := theModelsSchema()
	if len(databases) == 0 {
		return base
	}
	schema := query.Schema{
		Tables: make([]query.Table, len(base.Tables), len(base.Tables)+len(databases)),
		Joins:  slices.Clone(base.Joins),
	}
	copy(schema.Tables, base.Tables)
	for index := range schema.Tables {
		schema.Tables[index].Columns = slices.Clone(schema.Tables[index].Columns)
		schema.Tables[index].Database = "core"
	}
	for _, database := range databases {
		for _, table := range database.Tables {
			questions := append(slices.Clone(database.Semantic.Questions), table.Questions...)
			schema.Tables = append(schema.Tables, query.Table{
				Name:        database.Schema + "." + table.Name,
				Columns:     slices.Clone(table.Columns),
				Description: database.Semantic.Description + " " + table.Description,
				Questions:   questions,
				Database:    database.Source(),
			})
		}
	}
	return schema
}

func (s *Service) executeWithPlugins(ctx context.Context, statement, term string,
	maxChars int, databases []plugin.Database) ([]string, []map[string]any, error) {
	timeout, bounded := s.queryExecutionBudget()
	queryCtx := ctx
	var cancel context.CancelFunc = func() {}
	if bounded {
		queryCtx, cancel = context.WithTimeout(ctx, timeout)
	}
	defer cancel()
	reader, err := s.db.ReadOnly()
	if err != nil {
		return nil, nil, err
	}
	connection, err := reader.Conn(queryCtx)
	if err != nil {
		return nil, nil, err
	}
	defer connection.Close()
	attached := make([]string, 0, len(databases))
	defer func() {
		for index := len(attached) - 1; index >= 0; index-- {
			_, _ = connection.ExecContext(context.Background(),
				"DETACH DATABASE "+quoteSchema(attached[index]))
		}
	}()
	for _, database := range databases {
		if _, err := connection.ExecContext(queryCtx,
			"ATTACH DATABASE ? AS "+quoteSchema(database.Schema), database.ReadOnlyURI()); err != nil {
			return nil, nil, fmt.Errorf("attach plugin %s read-only: %w", database.Name, err)
		}
		attached = append(attached, database.Schema)
	}
	rows, err := connection.QueryContext(queryCtx, statement)
	if err != nil {
		return nil, nil, executionError(ctx, queryCtx, timeout, err)
	}
	columns, result, scanErr := scanRows(rows, maxChars, term)
	closeErr := rows.Close()
	if scanErr != nil {
		return nil, nil, executionError(ctx, queryCtx, timeout, scanErr)
	}
	if closeErr != nil {
		return nil, nil, executionError(ctx, queryCtx, timeout, closeErr)
	}
	if len(databases) > 0 {
		columns, result = ensureDatabaseColumn(columns, result, fallbackDatabase(statement, databases))
	}
	return columns, result, nil
}

func ensureDatabaseColumn(columns []string, rows []map[string]any,
	database string) ([]string, []map[string]any) {
	present := slices.Contains(columns, "database")
	if !present {
		columns = append(slices.Clone(columns), "database")
	}
	labels := strings.Split(database, "+")
	allowed := make(map[string]bool, len(labels)+1)
	for _, label := range labels {
		allowed[label] = true
	}
	allowed[database] = true
	for _, row := range rows {
		value := fmt.Sprint(row["database"])
		if len(labels) == 1 || !present || !allowed[value] {
			row["database"] = database
		}
	}
	return columns, rows
}

func fallbackDatabase(statement string, databases []plugin.Database) string {
	descriptors := make([]plugin.Descriptor, 0, len(databases))
	for _, database := range databases {
		descriptors = append(descriptors, database.Descriptor)
	}
	referenced, _ := plugin.Referenced(statement, descriptors, len(descriptors))
	labels := make([]string, 0, len(referenced)+1)
	if referencesCoreTable(statement) || len(referenced) == 0 {
		labels = append(labels, "core")
	}
	for _, descriptor := range referenced {
		labels = append(labels, descriptor.Source())
	}
	return strings.Join(labels, "+")
}

func referencesCoreTable(statement string) bool {
	normalized := strings.NewReplacer("\n", " ", "\r", " ", "\t", " ", `"`, "", "`", "").Replace(strings.ToLower(statement))
	for _, table := range theModelsSchema().Tables {
		for _, lead := range []string{"from ", "join "} {
			if strings.Contains(normalized, lead+table.Name+" ") ||
				strings.Contains(normalized, lead+"main."+table.Name+" ") {
				return true
			}
		}
	}
	return false
}

func quoteSchema(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}
