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
}

type VectorRegistration struct {
	Plugin   string        `json:"plugin"`
	Database string        `json:"database"`
	Path     string        `json:"path"`
	Alias    string        `json:"alias"`
	Tables   []VectorTable `json:"tables"`
}

func VectorRegistryPath(pluginRoot string) string {
	return filepath.Join(pluginRoot, VectorRegistryFilename)
}

// ComposeVectorRegistry projects only validated, explicitly opted-in columns.
// Paths stay relative to their plugin directory so local home paths never enter
// the contract.
func ComposeVectorRegistry(databases []Database) VectorRegistry {
	registry := VectorRegistry{Schema: vectorRegistrySchema, Databases: []VectorRegistration{}}
	for _, database := range databases {
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
		tables := make([]VectorTable, len(database.VectorTables))
		for index, table := range database.VectorTables {
			tables[index] = cloneVectorTable(table)
		}
		registry.Databases = append(registry.Databases, VectorRegistration{
			Plugin: database.Name, Database: database.DatabaseName,
			Path: path, Alias: database.Schema, Tables: tables,
		})
	}
	sortVectorRegistrations(registry.Databases)
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
	if err := registry.valid(); err != nil {
		return err
	}
	sortVectorRegistrations(registry.Databases)
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

func (registry VectorRegistry) valid() error {
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
			if err := table.Chunking.valid(database.Database, table.Name); err != nil {
				return err
			}
		}
	}
	return nil
}
