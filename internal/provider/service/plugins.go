package service

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/thellmwhisperer/la-roca/internal/provider/plugin"
	"github.com/thellmwhisperer/la-roca/internal/provider/query"
	"github.com/thellmwhisperer/la-roca/internal/provider/query/sqlgate"
	"github.com/thellmwhisperer/la-roca/internal/store"
)

const rocaOpsPluginName = "roca-ops"

type pluginRoute struct {
	databases []plugin.Database
	omitted   []plugin.Descriptor
	warnings  []string
}

func (s *Service) pluginsForQuestion(ctx context.Context, question string) pluginRoute {
	return s.pluginsFor(ctx, question, plugin.Relevant)
}

func (s *Service) pluginsForSQL(ctx context.Context, statement string) pluginRoute {
	return s.pluginsFor(ctx, statement, plugin.Referenced)
}

func (s *Service) pluginsFor(ctx context.Context, input string,
	selectPlugins func(string, []plugin.Descriptor) []plugin.Descriptor) pluginRoute {
	if !s.pluginsActive() {
		return pluginRoute{}
	}
	candidates, warnings := plugin.Discover(s.opts.PluginDir)
	candidates = s.onDemand(candidates)
	return s.withResidents(validatePluginRouteLimit(ctx,
		selectPlugins(input, candidates), warnings, plugin.MaxAttached-len(s.resident)))
}

func validatePluginRoute(ctx context.Context, candidates []plugin.Descriptor,
	warnings []string) pluginRoute {
	return validatePluginRouteLimit(ctx, candidates, warnings, plugin.MaxAttached)
}

func validatePluginRouteLimit(ctx context.Context, candidates []plugin.Descriptor,
	warnings []string, limit int) pluginRoute {
	route := pluginRoute{warnings: slices.Clone(warnings)}
	for _, candidate := range candidates {
		if len(route.databases) == limit {
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

func (s *Service) pluginsActive() bool {
	return s.opts.PluginsEnabled || s.opts.RocaOpsEnabled
}

func (s *Service) onDemand(candidates []plugin.Descriptor) []plugin.Descriptor {
	selected := make([]plugin.Descriptor, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.Semantic.Attachment != plugin.AttachmentOnDemand {
			continue
		}
		if candidate.Name == rocaOpsPluginName || !s.opts.PluginsEnabled {
			continue
		}
		selected = append(selected, candidate)
	}
	return selected
}

func (s *Service) withResidents(route pluginRoute) pluginRoute {
	if len(s.resident) == 0 {
		return route
	}
	route.databases = append(slices.Clone(s.resident), route.databases...)
	route.omitted = append(slices.Clone(s.residentOmitted), route.omitted...)
	route.warnings = append(slices.Clone(s.residentWarnings), route.warnings...)
	return route
}

func (s *Service) openResidents(ctx context.Context) error {
	if !s.pluginsActive() {
		return nil
	}
	descriptors, warnings := plugin.Discover(s.opts.PluginDir)
	var candidates []plugin.Descriptor
	if s.opts.RocaOpsEnabled {
		for _, descriptor := range descriptors {
			if descriptor.Name == rocaOpsPluginName && descriptor.Semantic.Attachment == plugin.AttachmentResident {
				candidates = append(candidates, descriptor)
			}
		}
	}
	if s.opts.PluginsEnabled {
		for _, descriptor := range descriptors {
			if descriptor.Name != rocaOpsPluginName && descriptor.Semantic.Attachment == plugin.AttachmentResident {
				candidates = append(candidates, descriptor)
			}
		}
	}
	// Discovery runs again for every answer and its warnings travel with that
	// per-query route. Keeping a copy here would report each of them twice.
	route := validatePluginRoute(ctx, candidates, nil)
	s.resident, s.residentOmitted, s.residentWarnings = route.databases, route.omitted, route.warnings

	var opsDatabase *plugin.Database
	if s.opts.RocaOpsEnabled {
		for index := range s.resident {
			if s.resident[index].Name == rocaOpsPluginName {
				opsDatabase = &s.resident[index]
				break
			}
		}
		if opsDatabase == nil {
			reason := strings.Join(append(slices.Clone(warnings), route.warnings...), "; ")
			if reason == "" {
				reason = "the bundled plugin is not installed or is not declared resident"
			}
			return fmt.Errorf("features.roca_ops is enabled but %s is unavailable: %s",
				rocaOpsPluginName, reason)
		}
		// Read-only refuses every write before database I/O, and opening the
		// operational store is itself one: it sets the journal mode and restricts
		// the artefacts. Reads still reach it through the resident attachment.
		if s.opts.ReadOnly {
			return nil
		}
		var err error
		s.ops, err = store.Open(opsDatabase.Database)
		if err != nil {
			return fmt.Errorf("open %s for operational writes: %w", rocaOpsPluginName, err)
		}
	}
	return nil
}

func (s *Service) openQueryConnection(ctx context.Context) (*sql.Conn, []string, error) {
	reader, err := s.db.ReadOnly()
	if err != nil {
		return nil, nil, err
	}
	connection, err := reader.Conn(ctx)
	if err != nil {
		return nil, nil, err
	}
	attached := make([]string, 0, len(s.resident))
	for _, database := range s.resident {
		if _, err := connection.ExecContext(ctx,
			"ATTACH DATABASE ? AS "+quoteSchema(database.Schema), database.ReadOnlyURI()); err != nil {
			closeQueryConnection(connection, attached)
			return nil, nil, fmt.Errorf("attach resident plugin %s read-only: %w", database.Name, err)
		}
		attached = append(attached, database.Schema)
	}
	return connection, attached, nil
}

func closeQueryConnection(connection *sql.Conn, attached []string) {
	for index := len(attached) - 1; index >= 0; index-- {
		_, _ = connection.ExecContext(context.Background(),
			"DETACH DATABASE "+quoteSchema(attached[index]))
	}
	_ = connection.Close()
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
	connection, attached, err := s.openQueryConnection(queryCtx)
	if err != nil {
		return nil, nil, executionError(ctx, queryCtx, timeout, err)
	}
	defer func() { closeQueryConnection(connection, attached) }()
	for _, database := range databases {
		if database.Semantic.Attachment == plugin.AttachmentResident {
			continue
		}
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
	present := slices.Contains(columns, plugin.ProvenanceColumn)
	if !present {
		columns = append(slices.Clone(columns), plugin.ProvenanceColumn)
	}
	labels := strings.Split(database, "+")
	allowed := make(map[string]bool, len(labels)+1)
	for _, label := range labels {
		allowed[label] = true
	}
	allowed[database] = true
	for _, row := range rows {
		value := fmt.Sprint(row[plugin.ProvenanceColumn])
		if len(labels) == 1 || !present || !allowed[value] {
			row[plugin.ProvenanceColumn] = database
		}
	}
	return columns, rows
}

func fallbackDatabase(statement string, databases []plugin.Database) string {
	descriptors := make([]plugin.Descriptor, 0, len(databases))
	for _, database := range databases {
		descriptors = append(descriptors, database.Descriptor)
	}
	referenced := plugin.Referenced(statement, descriptors)
	labels := make([]string, 0, len(referenced)+1)
	if referencesCoreTable(statement) || len(referenced) == 0 {
		labels = append(labels, "core")
	}
	for _, descriptor := range referenced {
		labels = append(labels, descriptor.Source())
	}
	return strings.Join(labels, "+")
}

// tableSource opens a list of table references and clauseBoundary closes it.
// What lies between the two is a comma-separated list, and a core table is any
// unqualified entry in it, wherever it stands.
var (
	tableSource    = regexp.MustCompile(`(?i)\b(?:from|join)\b`)
	clauseBoundary = regexp.MustCompile(
		`(?i)\b(?:where|group|order|limit|offset|having|window|on|using|select|values|returning|union|intersect|except)\b`)
)

func referencesCoreTable(statement string) bool {
	normalized := strings.NewReplacer("\n", " ", "\r", " ", "\t", " ", `"`, "", "`", "",
		"(", " ( ", ")", " ) ").Replace(strings.ToLower(statement))
	core := make(map[string]bool)
	for _, table := range theModelsSchema().Tables {
		core[table.Name] = true
	}
	for _, clause := range tableSource.Split(normalized, -1)[1:] {
		if end := clauseBoundary.FindStringIndex(clause); end != nil {
			clause = clause[:end[0]]
		}
		for _, reference := range strings.Split(clause, ",") {
			fields := strings.Fields(reference)
			if len(fields) > 0 && core[strings.TrimPrefix(fields[0], "main.")] {
				return true
			}
		}
	}
	return false
}

func quoteSchema(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}
