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

func QuestionRoute(names []string, inventory PluginRoute) (PluginRoute, error) {
	route, err := resolveScope(names, inventory)
	if err != nil {
		return PluginRoute{}, err
	}
	if len(names) == 0 || (len(names) == 1 && names[0] == ScopeAll) {
		return route, nil
	}
	route.Warnings = append(slices.Clone(inventory.Warnings), route.Warnings...)
	return route, nil
}

func (s *Service) InventoryRoute(ctx context.Context) PluginRoute {
	if !s.PluginsActive() {
		return PluginRoute{IncludeCore: true}
	}
	route := PluginRoute{
		IncludeCore: true,
		Databases:   slices.Clone(s.resident),
		Omitted:     slices.Clone(s.residentOmitted),
		Warnings:    slices.Clone(s.residentWarnings),
	}
	if !s.opts.PluginsEnabled {
		return route
	}
	candidates, warnings := plugin.Discover(s.opts.PluginDir)
	route.Warnings = append(route.Warnings, warnings...)
	limit := max(0, plugin.MaxAttached-len(route.Databases)-s.layerRegistryAttachmentCost())
	extra := validatePluginRouteLimit(ctx, s.onDemand(candidates), nil, limit, s.opts.ReadOnly)
	route.Databases = append(route.Databases, extra.Databases...)
	route.Omitted = append(route.Omitted, extra.Omitted...)
	route.Warnings = append(route.Warnings, extra.Warnings...)
	return route
}

func (s *Service) ResolveDatabaseScope(ctx context.Context, names []string) (DatabaseScope, error) {
	inventory := s.InventoryRoute(ctx)
	defer inventory.CloseOnDemand()
	route, err := QuestionRoute(names, inventory)
	if err != nil {
		return DatabaseScope{}, err
	}
	databases := make([]string, 0, len(route.Databases)+1)
	selected := make([]DatabaseSelection, 0, len(route.Databases)+1)
	if route.IncludeCore {
		databases = append(databases, ScopeCore)
		selected = append(selected, DatabaseSelection{Source: ScopeCore, Database: ScopeCore})
	}
	for _, database := range route.Databases {
		name := scopeName(database)
		databases = append(databases, name)
		selected = append(selected, DatabaseSelection{Source: database.Source(), Database: name})
	}
	return DatabaseScope{
		Databases:        databases,
		Selected:         selected,
		OmittedDatabases: route.OmittedSources(),
		Warnings:         slices.Clone(route.Warnings),
	}, nil
}

func resolveScope(names []string, inventory PluginRoute) (PluginRoute, error) {
	if len(names) == 0 {
		return inventory, nil
	}
	if len(names) == 1 && names[0] == ScopeAll {
		return inventory, nil
	}
	attached := attachedNames(inventory.IncludeCore, inventory.Databases)
	route := PluginRoute{}
	var unknown []string
	for _, name := range names {
		if name == ScopeCore {
			route.IncludeCore = true
			continue
		}
		var matched *plugin.Database
		for index := range inventory.Databases {
			if matchesScope(inventory.Databases[index], name) {
				matched = &inventory.Databases[index]
				break
			}
		}
		if matched == nil {
			unknown = append(unknown, name)
			continue
		}
		already := slices.ContainsFunc(route.Databases, func(database plugin.Database) bool {
			return database.Schema == matched.Schema
		})
		if !already {
			route.Databases = append(route.Databases, *matched)
		}
	}
	if len(unknown) > 0 {
		return PluginRoute{}, fmt.Errorf("unknown database %q; attached databases: %s",
			strings.Join(unknown, ", "), strings.Join(attached, ", "))
	}
	return route, nil
}

func (r PluginRoute) UnusedNames(inventory PluginRoute) []string {
	selected := make(map[string]bool, len(r.Databases)+1)
	if r.IncludeCore {
		selected[ScopeCore] = true
	}
	for _, database := range r.Databases {
		selected[scopeName(database)] = true
	}
	var unused []string
	for _, name := range attachedNames(inventory.IncludeCore, inventory.Databases) {
		if !selected[name] {
			unused = append(unused, name)
		}
	}
	return unused
}

func (r PluginRoute) CanWiden(inventory PluginRoute) bool {
	return len(r.UnusedNames(inventory)) > 0
}

// WidenReply reports that the reading seat asked for a second SQL pass over
// the attached databases that were held back.

// CanWidenAfterInterpretation reports whether a reading-seat reply may buy a
// second SQL pass. SQL failures stay attributed to their first scoped pass;
// widening cannot turn them into a different query with a different verdict.

func bundledSearchDatabases(route PluginRoute) []plugin.Database {
	var databases []plugin.Database
	for _, database := range route.Databases {
		if database.Name == rocaOpsPluginName || database.Name == rocaCorpusPluginName {
			databases = append(databases, database)
		}
	}
	return databases
}
