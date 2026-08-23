package plugin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/thellmwhisperer/la-roca/internal/securefile"
)

const (
	VectorRegistryFilename = "vector-registry.json"
	vectorRegistrySchema   = 1
)

// VectorRegistry is the generated, inference-free projection consumed by the
// vector worker. plugin.json remains the source of truth.
type VectorRegistry struct {
	Schema    int                  `json:"schema"`
	Databases []VectorRegistration `json:"databases"`
	Routes    []VectorRoute        `json:"routes"`
}

// VectorRoute is the database-name inventory needed to give vector query the
// same explicit routing contract as query. It carries no content and grants no
// vector coverage; Databases remains the opt-in declaration projection.
type VectorRoute struct {
	Plugin   string `json:"plugin"`
	Database string `json:"database"`
	Alias    string `json:"alias"`
	Source   string `json:"source"`
}

type VectorRegistration struct {
	Plugin   string                    `json:"plugin"`
	Database string                    `json:"database"`
	Path     string                    `json:"path"`
	Alias    string                    `json:"alias"`
	Tables   []VectorRegistrationTable `json:"tables"`
}

// VectorRegistrationTable combines opt-in retrieval columns with the
// validated semantic catalog columns needed for safe contextual queries.
type VectorRegistrationTable struct {
	Name        string         `json:"name"`
	IDColumn    string         `json:"id_column"`
	TextColumns []string       `json:"text_columns"`
	Columns     []string       `json:"columns,omitempty"`
	Chunking    *ChunkingHints `json:"chunking,omitempty"`
}

func VectorRegistryPath(pluginRoot string) string {
	return filepath.Join(pluginRoot, VectorRegistryFilename)
}

// ComposeVectorRegistry projects validated catalog column names alongside the
// explicitly opted-in retrieval columns. Paths stay relative to their plugin
// directory so local home paths never enter the contract.
func ComposeVectorRegistry(databases []Database) VectorRegistry {
	registry := VectorRegistry{Schema: vectorRegistrySchema,
		Databases: []VectorRegistration{}, Routes: []VectorRoute{}}
	for _, database := range databases {
		routeName := database.DatabaseName
		if routeName == "" {
			routeName = database.Name
		}
		registry.Routes = append(registry.Routes, VectorRoute{
			Plugin: database.Name, Database: routeName,
			Alias: database.Schema, Source: database.Source(),
		})
		if len(database.VectorTables) == 0 || database.Manifest == nil {
			continue
		}
		path := ""
		for _, declaration := range database.Manifest.Databases {
			if declaration.Name == database.DatabaseName {
				path = declaration.Path
				break
			}
		}
		tables := make([]VectorRegistrationTable, len(database.VectorTables))
		for index, table := range database.VectorTables {
			cloned := cloneVectorTable(table)
			tables[index] = VectorRegistrationTable{
				Name: cloned.Name, IDColumn: cloned.IDColumn,
				TextColumns: cloned.TextColumns, Chunking: cloned.Chunking,
			}
			for _, semanticTable := range database.Tables {
				if semanticTable.Name == table.Name {
					tables[index].Columns = slices.Clone(semanticTable.Columns)
					break
				}
			}
		}
		registry.Databases = append(registry.Databases, VectorRegistration{
			Plugin: database.Name, Database: database.DatabaseName,
			Path: path, Alias: database.Schema, Tables: tables,
		})
	}
	sortVectorRegistrations(registry.Databases)
	sortVectorRoutes(registry.Routes)
	return registry
}

func LoadVectorRegistry(path string) (VectorRegistry, error) {
	file, err := os.Open(path)
	if err != nil {
		return VectorRegistry{}, fmt.Errorf("read vector registry: %w", err)
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	var registry VectorRegistry
	if err := decoder.Decode(&registry); err != nil {
		return VectorRegistry{}, fmt.Errorf("read vector registry: %w", err)
	}
	if err := requireJSONEnd(decoder); err != nil {
		return VectorRegistry{}, fmt.Errorf("read vector registry: %w", err)
	}
	if registry.Schema != vectorRegistrySchema {
		return VectorRegistry{}, fmt.Errorf("vector registry schema is %d, want %d",
			registry.Schema, vectorRegistrySchema)
	}
	if registry.Databases == nil {
		registry.Databases = []VectorRegistration{}
	}
	if registry.Routes == nil {
		registry.Routes = []VectorRoute{}
	}
	if err := registry.valid(); err != nil {
		return VectorRegistry{}, err
	}
	return registry, nil
}

func SaveVectorRegistry(path string, registry VectorRegistry) error {
	registry.Schema = vectorRegistrySchema
	registry.Databases = slices.Clone(registry.Databases)
	if registry.Databases == nil {
		registry.Databases = []VectorRegistration{}
	}
	registry.Routes = slices.Clone(registry.Routes)
	if registry.Routes == nil {
		registry.Routes = []VectorRoute{}
	}
	if err := registry.valid(); err != nil {
		return err
	}
	sortVectorRegistrations(registry.Databases)
	sortVectorRoutes(registry.Routes)
	body, err := json.MarshalIndent(registry, "", "  ")
	if err != nil {
		return fmt.Errorf("encode vector registry: %w", err)
	}
	body = append(body, '\n')
	if existing, readErr := os.ReadFile(path); readErr == nil && bytes.Equal(existing, body) {
		return nil
	}
	if err := securefile.Write(path, body, 0o600, 0o700); err != nil {
		return fmt.Errorf("write vector registry: %w", err)
	}
	return nil
}

func sortVectorRegistrations(registrations []VectorRegistration) {
	sort.Slice(registrations, func(i, j int) bool {
		left, right := registrations[i], registrations[j]
		if left.Plugin != right.Plugin {
			return left.Plugin < right.Plugin
		}
		return left.Database < right.Database
	})
}

func sortVectorRoutes(routes []VectorRoute) {
	sort.Slice(routes, func(i, j int) bool {
		if routes[i].Plugin != routes[j].Plugin {
			return routes[i].Plugin < routes[j].Plugin
		}
		return routes[i].Database < routes[j].Database
	})
}

func (registry VectorRegistry) valid() error {
	seenRoutes := map[string]bool{}
	for _, route := range registry.Routes {
		key := route.Plugin + "\x00" + route.Database
		if !validPluginName(route.Plugin) || strings.TrimSpace(route.Database) == "" ||
			!validIdentifier(route.Alias) || strings.TrimSpace(route.Source) == "" || seenRoutes[key] {
			return fmt.Errorf("vector registry has an invalid or repeated route %q/%q",
				route.Plugin, route.Database)
		}
		seenRoutes[key] = true
	}
	seen := map[string]bool{}
	for _, database := range registry.Databases {
		key := database.Plugin + "\x00" + database.Database
		if !validPluginName(database.Plugin) || !validIdentifier(database.Database) || seen[key] {
			return fmt.Errorf("vector registry has an invalid or repeated database %q/%q",
				database.Plugin, database.Database)
		}
		seen[key] = true
		extension := strings.ToLower(filepath.Ext(database.Path))
		if !safeManifestFile(database.Path) ||
			(extension != ".db" && extension != ".sqlite" && extension != ".sqlite3") {
			return fmt.Errorf("vector registry database %s/%s has invalid path %q",
				database.Plugin, database.Database, database.Path)
		}
		if !validIdentifier(database.Alias) || len(database.Tables) == 0 {
			return fmt.Errorf("vector registry database %s/%s needs an alias and tables",
				database.Plugin, database.Database)
		}
		seenTables := map[string]bool{}
		for _, table := range database.Tables {
			if !validIdentifier(table.Name) || seenTables[table.Name] ||
				!validIdentifier(table.IDColumn) || len(table.TextColumns) == 0 {
				return fmt.Errorf("vector registry database %s/%s has invalid table %q",
					database.Plugin, database.Database, table.Name)
			}
			seenTables[table.Name] = true
			seenColumns := map[string]bool{}
			for _, column := range table.TextColumns {
				if !validIdentifier(column) || seenColumns[column] {
					return fmt.Errorf("vector registry table %s/%s.%s has invalid or repeated text column %q",
						database.Plugin, database.Database, table.Name, column)
				}
				seenColumns[column] = true
			}
			catalogColumns := map[string]bool{}
			for _, column := range table.Columns {
				if !validIdentifier(column) || catalogColumns[column] {
					return fmt.Errorf("vector registry table %s/%s.%s has invalid or repeated catalog column %q",
						database.Plugin, database.Database, table.Name, column)
				}
				catalogColumns[column] = true
			}
			if len(catalogColumns) > 0 {
				if !catalogColumns[table.IDColumn] {
					return fmt.Errorf("vector registry table %s/%s.%s catalog omits id column %q",
						database.Plugin, database.Database, table.Name, table.IDColumn)
				}
				for _, column := range table.TextColumns {
					if !catalogColumns[column] {
						return fmt.Errorf("vector registry table %s/%s.%s catalog omits text column %q",
							database.Plugin, database.Database, table.Name, column)
					}
				}
			}
			if err := table.Chunking.valid(database.Database, table.Name); err != nil {
				return err
			}
		}
	}
	return nil
}
