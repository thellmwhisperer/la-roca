package service

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/thellmwhisperer/la-roca/internal/provider/plugin"
)

const (
	// ScopeAll selects every attached database for one question.
	ScopeAll = "all"
	// ScopeCore is the name of the main La Roca database in --databases.
	ScopeCore = "core"
)

type DatabaseScope struct {
	Databases        []string            `json:"databases"`
	Selected         []DatabaseSelection `json:"selected"`
	OmittedDatabases []string            `json:"omitted_databases,omitempty"`
	Warnings         []string            `json:"warnings,omitempty"`
}

type DatabaseSelection struct {
	Source   string `json:"source"`
	Database string `json:"database"`
}

// ParseDatabaseList splits the --databases value. Empty means the default
// scope. Unknown names are the caller's problem after inventory is known.
func ParseDatabaseList(raw string) ([]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	parts := strings.Split(raw, ",")
	names := make([]string, 0, len(parts))
	for _, part := range parts {
		name := strings.TrimSpace(part)
		if name == "" {
			return nil, fmt.Errorf("empty database name in --databases")
		}
		names = append(names, name)
	}
	if slices.Contains(names, ScopeAll) && len(names) != 1 {
		return nil, fmt.Errorf("%s cannot be combined with other database names", ScopeAll)
	}
	return names, nil
}

func scopeName(database plugin.Database) string {
	if database.DatabaseName != "" {
		return database.DatabaseName
	}
	return database.Name
}

func attachedNames(includeCore bool, databases []plugin.Database) []string {
	names := make([]string, 0, len(databases)+1)
	if includeCore {
		names = append(names, ScopeCore)
	}
	for _, database := range databases {
		if name := scopeName(database); name != "" && !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	return names
}

func matchesScope(database plugin.Database, name string) bool {
	switch strings.TrimSpace(name) {
	case scopeName(database), database.Name, database.Source(), database.Schema:
		return true
	}
	return false
}

func questionRoute(names []string, inventory pluginRoute) (pluginRoute, error) {
	route, err := resolveScope(names, inventory)
	if err != nil {
		return pluginRoute{}, err
	}
	if len(names) == 1 && names[0] == ScopeAll {
		return route, nil
	}
	route.warnings = append(slices.Clone(inventory.warnings), route.warnings...)
	return route, nil
}

func (s *Service) inventoryRoute(ctx context.Context) pluginRoute {
	if !s.pluginsActive() {
		return pluginRoute{includeCore: true}
	}
	route := pluginRoute{
		includeCore: true,
		databases:   slices.Clone(s.resident),
		omitted:     slices.Clone(s.residentOmitted),
		warnings:    slices.Clone(s.residentWarnings),
	}
	if !s.opts.PluginsEnabled {
		return route
	}
	candidates, warnings := plugin.Discover(s.opts.PluginDir)
	route.warnings = append(route.warnings, warnings...)
	limit := max(0, plugin.MaxAttached-len(route.databases)-s.layerRegistryAttachmentCost())
	extra := validatePluginRouteLimit(ctx, s.onDemand(candidates), nil, limit, s.opts.ReadOnly)
	route.databases = append(route.databases, extra.databases...)
	route.omitted = append(route.omitted, extra.omitted...)
	route.warnings = append(route.warnings, extra.warnings...)
	return route
}

func (s *Service) ResolveDatabaseScope(ctx context.Context, names []string) (DatabaseScope, error) {
	inventory := s.inventoryRoute(ctx)
	route, err := questionRoute(names, inventory)
	if err != nil {
		return DatabaseScope{}, err
	}
	databases := make([]string, 0, len(route.databases)+1)
	selected := make([]DatabaseSelection, 0, len(route.databases)+1)
	if route.includeCore {
		databases = append(databases, ScopeCore)
		selected = append(selected, DatabaseSelection{Source: ScopeCore, Database: ScopeCore})
	}
	for _, database := range route.databases {
		name := scopeName(database)
		databases = append(databases, name)
		selected = append(selected, DatabaseSelection{Source: database.Source(), Database: name})
	}
	return DatabaseScope{
		Databases:        databases,
		Selected:         selected,
		OmittedDatabases: route.omittedSources(),
		Warnings:         slices.Clone(route.warnings),
	}, nil
}

func resolveScope(names []string, inventory pluginRoute) (pluginRoute, error) {
	if len(names) == 0 {
		if corpus := databaseForVerb(inventory.databases, IngestVerb, rocaCorpusPluginName); corpus != nil {
			// Historical core rows stay readable beside the corpus. Ops, cron,
			// and other plugins stay out until the question names them.
			return pluginRoute{includeCore: true, databases: []plugin.Database{*corpus}}, nil
		}
		return pluginRoute{includeCore: true}, nil
	}
	if len(names) == 1 && names[0] == ScopeAll {
		return inventory, nil
	}
	attached := attachedNames(inventory.includeCore, inventory.databases)
	route := pluginRoute{}
	var unknown []string
	for _, name := range names {
		if name == ScopeCore {
			route.includeCore = true
			continue
		}
		var matched *plugin.Database
		for index := range inventory.databases {
			if matchesScope(inventory.databases[index], name) {
				matched = &inventory.databases[index]
				break
			}
		}
		if matched == nil {
			unknown = append(unknown, name)
			continue
		}
		already := slices.ContainsFunc(route.databases, func(database plugin.Database) bool {
			return database.Schema == matched.Schema
		})
		if !already {
			route.databases = append(route.databases, *matched)
		}
	}
	if len(unknown) > 0 {
		return pluginRoute{}, fmt.Errorf("unknown database %q; attached databases: %s",
			strings.Join(unknown, ", "), strings.Join(attached, ", "))
	}
	return route, nil
}

func (r pluginRoute) unusedNames(inventory pluginRoute) []string {
	selected := make(map[string]bool, len(r.databases)+1)
	if r.includeCore {
		selected[ScopeCore] = true
	}
	for _, database := range r.databases {
		selected[scopeName(database)] = true
	}
	var unused []string
	for _, name := range attachedNames(inventory.includeCore, inventory.databases) {
		if !selected[name] {
			unused = append(unused, name)
		}
	}
	return unused
}

func (r pluginRoute) canWiden(inventory pluginRoute) bool {
	return len(r.unusedNames(inventory)) > 0
}

func insufficientAnswer(res QueryResult) bool {
	if res.Path == PathAsk || res.Path == PathRefused || res.Path == PathUnresolved {
		return false
	}
	if wideningBlockedByDegradation(res.Degraded) {
		return false
	}
	return res.RowCount == 0
}

func wideningBlockedByDegradation(degraded string) bool {
	switch degraded {
	case DegradedInvalidSQL, DegradedExecution, DegradedTimeout:
		return true
	default:
		return false
	}
}

// WidenReply reports that the reading seat asked for a second SQL pass over
// the attached databases that were held back.
func WidenReply(text string) bool {
	return strings.TrimSpace(text) == "WIDEN"
}

// CanWidenAfterInterpretation reports whether a reading-seat reply may buy a
// second SQL pass. SQL failures stay attributed to their first scoped pass;
// widening cannot turn them into a different query with a different verdict.
func CanWidenAfterInterpretation(res QueryResult, text string) bool {
	return len(res.UnusedDatabases) > 0 && !wideningBlockedByDegradation(res.Degraded) &&
		WidenReply(text)
}

func bundledSearchDatabases(route pluginRoute) []plugin.Database {
	var databases []plugin.Database
	for _, database := range route.databases {
		if database.Name == rocaOpsPluginName || database.Name == rocaCorpusPluginName {
			databases = append(databases, database)
		}
	}
	return databases
}
