package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/thellmwhisperer/la-roca/internal/provider/plugin"
	"github.com/thellmwhisperer/la-roca/internal/provider/query"
	"github.com/thellmwhisperer/la-roca/internal/provider/query/sqlgate"
	"github.com/thellmwhisperer/la-roca/internal/store"
)

const (
	rocaOpsPluginName    = "roca-ops"
	rocaCorpusPluginName = "roca-corpus" // the package La Roca ships to own perennial ingest
	StoreVerb            = "store"
	QueryVerb            = "query"
	ExecVerb             = "exec"
	SQLVerb              = "sql"
	// IngestVerb is the canonical verb the bundled corpus manifest declares and
	// this kernel routes. The command surface asks the same manifest for it, so
	// the seat and the CLI command it appears as are named once.
	IngestVerb = "ingest"
)

// ownsVerb reports whether a discovered package holds the seat the kernel
// routes a verb into. The manifest declares the verb, and the name La Roca
// ships bounds it: an installed third party may not take the seat by declaring
// the same verb, and a package installed before manifests existed, which
// declares nothing, keeps the seat it already had.
func ownsVerb(descriptor plugin.Descriptor, verb, packageName string) bool {
	return descriptor.Name == packageName &&
		(descriptor.Manifest == nil || descriptor.Manifest.HasVerb(verb))
}

// selfGated names the packages La Roca ships and whose attachment it owns
// itself. Each one reaches a connection through its own feature flag, never
// through the generic selection that carries third-party plugins, so an
// installed but unactivated package costs neither an attachment slot nor a
// place in the schema every answer is written against.
func selfGated(name string) bool {
	return name == rocaOpsPluginName
}

type pluginRoute struct {
	includeCore bool
	databases   []plugin.Database
	omitted     []plugin.Descriptor
	warnings    []string
}

func (s *Service) pluginsForQuestion(ctx context.Context, _ string) pluginRoute {
	route, err := s.questionRoute(ctx, nil)
	if err != nil {
		return pluginRoute{warnings: []string{err.Error()}}
	}
	return route
}

func (s *Service) pluginsForSQL(ctx context.Context, statement string) pluginRoute {
	return s.pluginsFor(ctx, statement, plugin.Referenced)
}

func (s *Service) pluginsFor(ctx context.Context, input string,
	selectPlugins func(string, []plugin.Descriptor) []plugin.Descriptor) pluginRoute {
	if !s.pluginsActive() {
		return pluginRoute{includeCore: true}
	}
	candidates, warnings := plugin.Discover(s.opts.PluginDir)
	candidates = s.onDemand(candidates)
	limit := max(0, plugin.MaxAttached-len(s.resident)-s.layerRegistryAttachmentCost())
	return s.withResidents(validatePluginRouteLimit(ctx,
		selectPlugins(input, candidates), warnings, limit))
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
	return s.opts.PluginsEnabled || s.opts.RocaOpsEnabled || s.opts.CorpusEnabled
}

func (s *Service) onDemand(candidates []plugin.Descriptor) []plugin.Descriptor {
	selected := make([]plugin.Descriptor, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.Semantic.Attachment != plugin.AttachmentOnDemand {
			continue
		}
		if selfGated(candidate.Name) || !s.opts.PluginsEnabled {
			continue
		}
		selected = append(selected, candidate)
	}
	return selected
}

func (s *Service) withResidents(route pluginRoute) pluginRoute {
	// A resident half that could not be opened has no database and still owes the
	// answer its warning, so the count of databases alone does not decide this.
	if len(s.resident) == 0 && len(s.residentOmitted) == 0 && len(s.residentWarnings) == 0 {
		route.includeCore = true
		return route
	}
	route.includeCore = true
	route.databases = append(slices.Clone(s.resident), route.databases...)
	route.omitted = append(slices.Clone(s.residentOmitted), route.omitted...)
	route.warnings = append(slices.Clone(s.residentWarnings), route.warnings...)
	return route
}

func (s *Service) openResidents(ctx context.Context) error {
	if !s.pluginsActive() && s.opts.PluginDir == "" {
		return nil
	}
	descriptors, warnings := plugin.Discover(s.opts.PluginDir)
	var stableLayers *plugin.Database
	if !s.opts.RocaOpsEnabled {
		var err error
		stableLayers, err = stableLayerDatabase(ctx, descriptors)
		if err != nil {
			return err
		}
	}
	var candidates []plugin.Descriptor
	if s.opts.RocaOpsEnabled {
		for _, descriptor := range descriptors {
			if ownsVerb(descriptor, StoreVerb, rocaOpsPluginName) &&
				descriptor.Semantic.Attachment == plugin.AttachmentResident {
				candidates = append(candidates, descriptor)
			}
		}
	}
	if s.opts.CorpusEnabled {
		for _, descriptor := range descriptors {
			if ownsVerb(descriptor, IngestVerb, rocaCorpusPluginName) &&
				descriptor.Semantic.Attachment == plugin.AttachmentResident {
				candidates = append(candidates, descriptor)
			}
		}
	}
	if s.opts.PluginsEnabled {
		for _, descriptor := range descriptors {
			if !selfGated(descriptor.Name) &&
				!ownsVerb(descriptor, IngestVerb, rocaCorpusPluginName) &&
				descriptor.Semantic.Attachment == plugin.AttachmentResident {
				candidates = append(candidates, descriptor)
			}
		}
	}
	// Discovery runs again for every answer and its warnings travel with that
	// per-query route. Keeping a copy here would report each of them twice.
	limit := plugin.MaxAttached
	if stableLayers != nil {
		limit--
	}
	route := validatePluginRouteLimit(ctx, candidates, nil, limit)
	s.resident, s.residentOmitted, s.residentWarnings = route.databases, route.omitted, route.warnings

	var opsDatabase *plugin.Database
	if s.opts.RocaOpsEnabled {
		opsDatabase = databaseForVerb(s.resident, StoreVerb, rocaOpsPluginName)
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
		if !s.opts.ReadOnly {
			var err error
			s.ops, err = store.Open(opsDatabase.Database)
			if err != nil {
				return fmt.Errorf("open %s for operational writes: %w", rocaOpsPluginName, err)
			}
		}
	}
	if opsDatabase != nil {
		s.layerSet = opsDatabase
		s.layerDB = s.ops
	} else {
		s.layerSet = stableLayers
		if stableLayers != nil && !s.opts.ReadOnly {
			var err error
			s.layerDB, err = store.Open(stableLayers.Database)
			if err != nil {
				return fmt.Errorf("open %s for the layer registry: %w", rocaOpsPluginName, err)
			}
		}
	}
	if s.opts.CorpusEnabled {
		corpusDatabase := databaseForVerb(s.resident, IngestVerb, rocaCorpusPluginName)
		if corpusDatabase == nil {
			reason := strings.Join(append(slices.Clone(warnings), route.warnings...), "; ")
			if reason == "" {
				reason = "the bundled plugin is not installed or is not declared resident"
			}
			// Read-only cannot install the package it would be demanding, so an
			// installation without it still answers from core and says so. Failing
			// the open would break every read on a machine under audit.
			if s.opts.ReadOnly {
				s.residentWarnings = append(s.residentWarnings, fmt.Sprintf(
					"the bundled %s plugin that owns %s is unavailable: %s; the answer covers core only",
					rocaCorpusPluginName, IngestVerb, reason))
				return nil
			}
			return fmt.Errorf("the bundled %s plugin that owns %s is unavailable: %s",
				rocaCorpusPluginName, IngestVerb, reason)
		}
		if !s.opts.ReadOnly {
			var err error
			s.corpus, err = store.Open(corpusDatabase.Database)
			if err != nil {
				return fmt.Errorf("open %s for perennial ingest: %w", rocaCorpusPluginName, err)
			}
		}
	}
	return nil
}

func stableLayerDatabase(ctx context.Context, descriptors []plugin.Descriptor) (*plugin.Database, error) {
	var databases []plugin.Database
	for _, descriptor := range descriptors {
		if !ownsVerb(descriptor, StoreVerb, rocaOpsPluginName) ||
			descriptor.Semantic.Attachment != plugin.AttachmentResident {
			continue
		}
		database, err := plugin.Validate(ctx, descriptor)
		if err != nil {
			return nil, fmt.Errorf("validate %s layer registry: %w", rocaOpsPluginName, err)
		}
		databases = append(databases, database)
	}
	if len(databases) == 0 {
		return nil, nil
	}
	selected := databaseForVerb(databases, StoreVerb, rocaOpsPluginName)
	if selected == nil {
		return nil, fmt.Errorf("%s declares no single durable layer registry", rocaOpsPluginName)
	}
	return selected, nil
}

// residentCorpus is the attached bundled corpus, or nil when this installation
// has none. It is the only handle read-only has on that database, which it
// never opens for writing.
func (s *Service) residentCorpus() *plugin.Database {
	return databaseForVerb(s.resident, IngestVerb, rocaCorpusPluginName)
}

// databaseForVerb resolves the single database a verb writes into. A package
// that declares the verb over several databases names no seat at all, because
// the kernel would otherwise pick one of them by discovery order.
func databaseForVerb(databases []plugin.Database, verb, packageName string) *plugin.Database {
	var selected *plugin.Database
	for index := range databases {
		if !ownsVerb(databases[index].Descriptor, verb, packageName) {
			continue
		}
		if selected != nil {
			return nil
		}
		selected = &databases[index]
	}
	return selected
}

func (s *Service) openQueryConnection(ctx context.Context) (*sql.Conn, []string, error) {
	return s.openQueryConnectionOn(ctx, s.db)
}

func (s *Service) openQueryConnectionOn(ctx context.Context, target *store.DB) (*sql.Conn, []string, error) {
	reader, err := target.ReadOnly()
	if err != nil {
		return nil, nil, err
	}
	connection, err := reader.Conn(ctx)
	if err != nil {
		return nil, nil, err
	}
	if target == s.hubDB {
		return connection, nil, nil
	}
	attached, err := plugin.Attach(ctx, connection, s.resident)
	if err != nil {
		_ = connection.Close()
		return nil, nil, err
	}
	if s.layerRegistryAttachmentCost() != 0 {
		registryAttached, err := plugin.Attach(ctx, connection, []plugin.Database{*s.layerSet})
		if err != nil {
			plugin.Detach(context.Background(), connection, attached)
			_ = connection.Close()
			return nil, nil, err
		}
		attached = append(attached, registryAttached...)
	}
	if s.layerSet != nil {
		if err := exposeLayerRegistry(ctx, connection, s.layerSet.Schema); err != nil {
			hideLayerRegistry(connection)
			plugin.Detach(context.Background(), connection, attached)
			_ = connection.Close()
			return nil, nil, err
		}
	}
	return connection, attached, nil
}

func closeQueryConnection(connection *sql.Conn, attached []string) {
	hideLayerRegistry(connection)
	plugin.Detach(context.Background(), connection, attached)
	_ = connection.Close()
}

func (s *Service) layerRegistryAttachmentCost() int {
	if s.layerSet == nil {
		return 0
	}
	for _, database := range s.resident {
		if database.Schema == s.layerSet.Schema {
			return 0
		}
	}
	return 1
}

func exposeLayerRegistry(ctx context.Context, connection *sql.Conn, schema string) (resultErr error) {
	if _, err := connection.ExecContext(ctx, "PRAGMA query_only = OFF"); err != nil {
		return fmt.Errorf("open the layer registry compatibility view: %w", err)
	}
	defer func() {
		if _, err := connection.ExecContext(context.WithoutCancel(ctx), "PRAGMA query_only = ON"); err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("close the layer registry compatibility view: %w", err))
		}
	}()
	if _, err := connection.ExecContext(ctx, "DROP VIEW IF EXISTS temp.layers"); err != nil {
		return fmt.Errorf("replace the layer registry compatibility view: %w", err)
	}
	_, err := connection.ExecContext(ctx, `CREATE TEMP VIEW layers AS
		SELECT name, description, schema_file, access_mode, ingest_allowed, is_coordination,
		       search_excluded, alias_of, added_by, deprecated, lifecycle, capabilities,
		       since_version FROM `+quoteSchema(schema)+`.layers`)
	if err != nil {
		return fmt.Errorf("compose the layer registry compatibility view: %w", err)
	}
	return nil
}

func hideLayerRegistry(connection *sql.Conn) {
	ctx := context.Background()
	var kind string
	if err := connection.QueryRowContext(ctx,
		"SELECT type FROM sqlite_temp_master WHERE name = 'layers'").Scan(&kind); err != nil || kind != "view" {
		return
	}
	_, _ = connection.ExecContext(ctx, "PRAGMA query_only = OFF")
	_, _ = connection.ExecContext(ctx, "DROP VIEW IF EXISTS temp.layers")
	_, _ = connection.ExecContext(ctx, "PRAGMA query_only = ON")
}

func (r pluginRoute) consulted() []string {
	consulted := make([]string, 0, len(r.databases)+1)
	if r.includeCore {
		consulted = append(consulted, "core")
	}
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

func (s *Service) gateFor(includeCore bool, databases []plugin.Database) (*sqlgate.Gate, func(), error) {
	if len(databases) == 0 && includeCore {
		gate, err := s.theGate()
		return gate, func() {}, err
	}
	schemas := make([]sqlgate.Schema, 0, len(databases))
	for _, database := range databases {
		tables := make([]sqlgate.Table, 0, len(database.Tables))
		for _, table := range database.Tables {
			tables = append(tables, sqlgate.Table{Name: table.Name, Columns: table.Columns, FTS5: table.FTS5})
		}
		schemas = append(schemas, sqlgate.Schema{Name: database.Schema, Tables: tables})
	}
	var gate *sqlgate.Gate
	var err error
	if includeCore {
		gate, err = sqlgate.OpenWithSchemas(schemas)
	} else {
		gate, err = sqlgate.OpenAttached(schemas)
	}
	if err != nil {
		return nil, func() {}, err
	}
	return gate, func() { _ = gate.Close() }, nil
}

func schemaWithPlugins(includeCore bool, databases []plugin.Database) query.Schema {
	if !includeCore {
		return plugin.Compose(query.Schema{}, databases)
	}
	base := theModelsSchema()
	if len(databases) == 0 {
		return base
	}
	// The parsed schema is read once per process and handed out by value, so
	// labeling its tables in place would brand the shared copy for every later
	// answer, including the ones that consult no plugin at all.
	base.Tables = slices.Clone(base.Tables)
	for index := range base.Tables {
		base.Tables[index].Database = "core"
	}
	return plugin.Compose(base, databases)
}

func (s *Service) executeWithPlugins(ctx context.Context, statement, term string,
	maxChars int, databases []plugin.Database) ([]string, []map[string]any, error) {
	if s.servingLayout() != LayoutLegacyServing && s.hub != nil && needsHubSearch(statement) {
		if err := s.ensureHubSearch(ctx); err != nil {
			if recoverErr := s.recoverHubSearchFailure(err); recoverErr != nil {
				return nil, nil, recoverErr
			}
		}
	}
	columns, rows, err := s.executeWithDatabase(ctx, statement, term, maxChars, databases, s.db)
	if err != nil || s.servingLayout() != LayoutShadowEqual || s.hubDB == nil {
		return columns, rows, err
	}
	hubColumns, hubRows, hubErr := s.executeWithDatabase(
		ctx, statement, term, maxChars, databases, s.hubDB)
	s.compareShadow(s.shadowEqual(columns, rows, hubColumns, hubRows), hubErr, "shadow rows differ")
	return columns, rows, nil
}

func (s *Service) executeWithDatabase(ctx context.Context, statement, term string,
	maxChars int, databases []plugin.Database, target *store.DB) ([]string, []map[string]any, error) {
	timeout, bounded := s.queryExecutionBudget()
	queryCtx := ctx
	var cancel context.CancelFunc = func() {}
	if bounded {
		queryCtx, cancel = context.WithTimeout(ctx, timeout)
	}
	defer cancel()
	connection, attached, err := s.openQueryConnectionOn(queryCtx, target)
	if err != nil {
		return nil, nil, executionError(ctx, queryCtx, timeout, err)
	}
	defer func() { closeQueryConnection(connection, attached) }()
	var onDemand []plugin.Database
	for _, database := range databases {
		if database.Semantic.Attachment != plugin.AttachmentResident {
			onDemand = append(onDemand, database)
		}
	}
	newlyAttached, err := plugin.Attach(queryCtx, connection, onDemand)
	if err != nil {
		return nil, nil, err
	}
	attached = append(attached, newlyAttached...)
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
