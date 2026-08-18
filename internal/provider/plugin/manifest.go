package plugin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/thellmwhisperer/la-roca/internal/provider/query"
)

const (
	PackageFilename = "plugin.json"
	// HostBinary is what a package declares when its capabilities are commands
	// of the La Roca binary itself rather than of an executable it ships. It is
	// the one binary a manifest may name without supplying that file, so a
	// package with no executable payload stays data-only instead of having to
	// ship code to become installable.
	HostBinary = "roca"
)

// Manifest is the complete declaration a federated plugin ships. The kernel
// reads this file; it does not infer a plugin's databases, command surface, or
// semantic meaning from directory conventions.
type Manifest struct {
	Schema       int                   `json:"schema"`
	Name         string                `json:"name"`
	Version      string                `json:"version"`
	Binary       string                `json:"binary"`
	Databases    []DatabaseDeclaration `json:"databases"`
	Semantic     SemanticFragment      `json:"semantic"`
	Verbs        []Verb                `json:"verbs"`
	Capabilities []Capability          `json:"capabilities"`
}

type DatabaseDeclaration struct {
	Name       string     `json:"name"`
	Path       string     `json:"path"`
	Alias      string     `json:"alias"`
	Attachment Attachment `json:"attachment"`
	Custody    bool       `json:"custody,omitempty"`
	Retention  string     `json:"retention"`
}

type SemanticFragment struct {
	Databases []DatabaseSemantic `json:"databases"`
}

type DatabaseSemantic struct {
	Database    string          `json:"database"`
	Description string          `json:"description"`
	Questions   []string        `json:"questions,omitempty"`
	Tables      []SemanticTable `json:"tables"`
}

type Verb struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Capability  string `json:"capability"`
}

type Capability struct {
	Name    string   `json:"name"`
	Command []string `json:"command"`
}

// Registration is the one canonical verb projected onto both public surfaces.
// Keeping the names derived here prevents a CLI command and its MCP twin from
// drifting into separate contracts. CLI is the capability call as it is typed,
// which is the verb name only when the verb owns a command of its own.
type Registration struct {
	Plugin      string
	Name        string
	Description string
	CLI         string
	MCP         string
	Capability  string
	Binary      string
	Command     []string
}

func ReadManifest(path string) (Manifest, error) {
	file, err := os.Open(path)
	if err != nil {
		return Manifest{}, fmt.Errorf("read %s: %w", PackageFilename, err)
	}
	defer file.Close()
	manifest, err := DecodeManifest(file)
	if err != nil {
		return Manifest{}, fmt.Errorf("parse %s: %w", PackageFilename, err)
	}
	return manifest, nil
}

func DecodeManifest(reader io.Reader) (Manifest, error) {
	manifest, err := DecodeUnvalidatedManifest(reader)
	if err != nil {
		return Manifest{}, err
	}
	if err := manifest.Valid(); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

// DecodeUnvalidatedManifest reads a manifest strictly but leaves it unchecked,
// for the bundled packages whose identity is stamped from the running build
// rather than from the file they embed. Every such caller owes Valid before it
// writes the result.
func DecodeUnvalidatedManifest(reader io.Reader) (Manifest, error) {
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	var manifest Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, err
	}
	if err := requireJSONEnd(decoder); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

// Federated reports whether a plugin.json declares a federation manifest rather
// than the installer's legacy package metadata. Discovery and installation ask
// this one question, so a manifest that names any federated part is read and
// refused as a manifest on both sides instead of failing as unknown legacy
// fields on one of them.
func Federated(raw []byte) (bool, error) {
	var shape map[string]json.RawMessage
	if err := json.Unmarshal(raw, &shape); err != nil {
		return false, fmt.Errorf("parse %s: %w", PackageFilename, err)
	}
	for _, part := range []string{"databases", "binary", "semantic", "verbs", "capabilities"} {
		if shape[part] != nil {
			return true, nil
		}
	}
	return false, nil
}

func (r Registration) CommandContext(ctx context.Context, arguments ...string) *exec.Cmd {
	command := append(slices.Clone(r.Command), arguments...)
	return exec.CommandContext(ctx, r.Binary, command...)
}

func requireJSONEnd(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); errors.Is(err, io.EOF) {
		return nil
	} else if err != nil {
		return err
	}
	return fmt.Errorf("contains more than one JSON value")
}

func (m Manifest) Valid() error {
	if m.Schema != 1 {
		return fmt.Errorf("%s schema is %d, want 1", PackageFilename, m.Schema)
	}
	if !validPluginName(m.Name) {
		return fmt.Errorf("%s has invalid plugin name %q", PackageFilename, m.Name)
	}
	if strings.TrimSpace(m.Version) == "" {
		return fmt.Errorf("%s has no version", PackageFilename)
	}
	if !safeManifestFile(m.Binary) {
		return fmt.Errorf("%s has invalid binary %q", PackageFilename, m.Binary)
	}
	if len(m.Databases) == 0 {
		return fmt.Errorf("%s declares no databases", PackageFilename)
	}

	declarations := make(map[string]DatabaseDeclaration, len(m.Databases))
	paths := make(map[string]bool, len(m.Databases))
	aliases := make(map[string]bool, len(m.Databases))
	for _, database := range m.Databases {
		if !validIdentifier(database.Name) || declarations[database.Name].Name != "" {
			return fmt.Errorf("%s has invalid or repeated database name %q", PackageFilename, database.Name)
		}
		if !safeManifestFile(database.Path) || paths[database.Path] {
			return fmt.Errorf("%s has invalid or repeated database path %q", PackageFilename, database.Path)
		}
		extension := strings.ToLower(filepath.Ext(database.Path))
		if extension != ".db" && extension != ".sqlite" && extension != ".sqlite3" {
			return fmt.Errorf("database %s path %q is not a SQLite filename", database.Name, database.Path)
		}
		if !validIdentifier(database.Alias) || aliases[database.Alias] {
			return fmt.Errorf("%s has invalid or repeated database alias %q", PackageFilename, database.Alias)
		}
		if database.Attachment != AttachmentResident && database.Attachment != AttachmentOnDemand {
			return fmt.Errorf("database %s attachment is %q, want %q or %q", database.Name,
				database.Attachment, AttachmentResident, AttachmentOnDemand)
		}
		if strings.TrimSpace(database.Retention) == "" {
			return fmt.Errorf("database %s has no plugin-owned retention policy", database.Name)
		}
		declarations[database.Name], paths[database.Path], aliases[database.Alias] = database, true, true
	}

	semantics := make(map[string]bool, len(m.Semantic.Databases))
	for _, fragment := range m.Semantic.Databases {
		declaration, exists := declarations[fragment.Database]
		if !exists {
			return fmt.Errorf("semantic database %q has no database declaration", fragment.Database)
		}
		if semantics[fragment.Database] {
			return fmt.Errorf("semantic database %q is repeated", fragment.Database)
		}
		semantic := Semantic{
			Version: 1, Attachment: declaration.Attachment, Custody: declaration.Custody,
			Description: fragment.Description, Questions: fragment.Questions, Tables: fragment.Tables,
		}
		if err := semantic.validFor(PackageFilename); err != nil {
			return fmt.Errorf("database %s: %w", fragment.Database, err)
		}
		semantics[fragment.Database] = true
	}
	for name := range declarations {
		if !semantics[name] {
			return fmt.Errorf("database %q has no semantic declaration", name)
		}
	}

	capabilities := make(map[string]Capability, len(m.Capabilities))
	for _, capability := range m.Capabilities {
		if !validIdentifier(capability.Name) || capabilities[capability.Name].Name != "" {
			return fmt.Errorf("%s has invalid or repeated capability %q", PackageFilename, capability.Name)
		}
		if len(capability.Command) == 0 {
			return fmt.Errorf("capability %s has no command", capability.Name)
		}
		for _, argument := range capability.Command {
			if strings.TrimSpace(argument) == "" {
				return fmt.Errorf("capability %s has an empty command argument", capability.Name)
			}
		}
		capabilities[capability.Name] = capability
	}
	verbs := make(map[string]bool, len(m.Verbs))
	for _, verb := range m.Verbs {
		if !validIdentifier(verb.Name) || verbs[verb.Name] {
			return fmt.Errorf("%s has invalid or repeated verb %q", PackageFilename, verb.Name)
		}
		if strings.TrimSpace(verb.Description) == "" {
			return fmt.Errorf("verb %s has no description", verb.Name)
		}
		if _, exists := capabilities[verb.Capability]; !exists {
			return fmt.Errorf("verb %s names missing capability %q", verb.Name, verb.Capability)
		}
		verbs[verb.Name] = true
	}
	return nil
}

func safeManifestFile(name string) bool {
	return name != "" && filepath.Base(name) == name && name != "." && name != ".." &&
		!strings.ContainsAny(name, "/\\\x00")
}

func (m Manifest) semanticFor(name string) (Semantic, bool) {
	var declaration DatabaseDeclaration
	for _, candidate := range m.Databases {
		if candidate.Name == name {
			declaration = candidate
			break
		}
	}
	for _, fragment := range m.Semantic.Databases {
		if fragment.Database == name {
			return Semantic{
				Version: 1, Attachment: declaration.Attachment, Custody: declaration.Custody,
				Description: fragment.Description, Questions: slices.Clone(fragment.Questions),
				Tables: slices.Clone(fragment.Tables),
			}, true
		}
	}
	return Semantic{}, false
}

func (m Manifest) HasVerb(name string) bool {
	return slices.ContainsFunc(m.Verbs, func(verb Verb) bool { return verb.Name == name })
}

func Register(manifests ...Manifest) ([]Registration, error) {
	seen := map[string]string{}
	var registrations []Registration
	for _, manifest := range manifests {
		if err := manifest.Valid(); err != nil {
			return nil, err
		}
		capabilities := make(map[string]Capability, len(manifest.Capabilities))
		for _, capability := range manifest.Capabilities {
			capabilities[capability.Name] = capability
		}
		for _, verb := range manifest.Verbs {
			// Both public names are derived from this one, so a single owner per
			// verb is what keeps the CLI command and its MCP twin from being
			// claimed by two plugins. The CLI name is the capability's own call,
			// so a verb riding an existing command names that command rather
			// than advertising one the binary does not have.
			if owner := seen[verb.Name]; owner != "" {
				return nil, fmt.Errorf("verb %q is declared by both %s and %s", verb.Name, owner, manifest.Name)
			}
			capability := capabilities[verb.Capability]
			registrations = append(registrations, Registration{
				Plugin: manifest.Name, Name: verb.Name, Description: verb.Description,
				CLI: strings.Join(capability.Command, " "), MCP: "roca_" + verb.Name, Capability: capability.Name,
				Binary: manifest.Binary, Command: slices.Clone(capability.Command),
			})
			seen[verb.Name] = manifest.Name
		}
	}
	return registrations, nil
}

// Compose adds validated plugin fragments to the catalog handed to NL-to-SQL.
// It accepts an empty base, which is the normal shape of a database-less
// kernel, while the current compatibility adapter may still pass legacy tables.
func Compose(base query.Schema, databases []Database) query.Schema {
	schema := query.Schema{Tables: make([]query.Table, len(base.Tables)), Joins: slices.Clone(base.Joins)}
	copy(schema.Tables, base.Tables)
	for index := range schema.Tables {
		schema.Tables[index].Columns = slices.Clone(schema.Tables[index].Columns)
	}
	for _, database := range databases {
		for _, table := range database.Tables {
			questions := append(slices.Clone(database.Semantic.Questions), table.Questions...)
			schema.Tables = append(schema.Tables, query.Table{
				Name: database.Schema + "." + table.Name, Columns: slices.Clone(table.Columns),
				Description: database.Semantic.Description + " " + table.Description,
				Questions:   questions, Database: database.Source(), FTS5: table.FTS5,
			})
		}
	}
	return schema
}

type statementExecutor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func Attach(ctx context.Context, executor statementExecutor, databases []Database) ([]string, error) {
	attached := make([]string, 0, len(databases))
	for _, database := range databases {
		if _, err := executor.ExecContext(ctx, "ATTACH DATABASE ? AS "+quoteIdentifier(database.Schema),
			database.ReadOnlyURI()); err != nil {
			Detach(context.Background(), executor, attached)
			return nil, fmt.Errorf("attach plugin %s database %s read-only: %w",
				database.Name, database.databaseLabel(), err)
		}
		attached = append(attached, database.Schema)
	}
	return attached, nil
}

func Detach(ctx context.Context, executor statementExecutor, schemas []string) {
	for index := len(schemas) - 1; index >= 0; index-- {
		_, _ = executor.ExecContext(ctx, "DETACH DATABASE "+quoteIdentifier(schemas[index]))
	}
}

// Hub is the kernel's database-neutral attach point. Its main schema is an
// empty in-memory database; every durable database belongs to a plugin.
type Hub struct {
	*sql.DB
	attached []string
}

func OpenHub(ctx context.Context, databases []Database) (*Hub, error) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return nil, fmt.Errorf("open in-memory plugin hub: %w", err)
	}
	db.SetMaxOpenConns(1)
	attached, err := Attach(ctx, db, databases)
	if err != nil {
		db.Close()
		return nil, err
	}
	return &Hub{DB: db, attached: attached}, nil
}

func (h *Hub) Close() error {
	if h == nil || h.DB == nil {
		return nil
	}
	Detach(context.Background(), h.DB, h.attached)
	return h.DB.Close()
}
