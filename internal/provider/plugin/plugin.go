// Package plugin discovers and validates the read-only databases that extend a
// La Roca query without sharing its writable store.
package plugin

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"unicode"

	"github.com/thellmwhisperer/la-roca/internal/provider/query"
	"github.com/thellmwhisperer/la-roca/internal/provider/query/sqlgate"
	"gopkg.in/yaml.v3"
	_ "modernc.org/sqlite"
)

const (
	SemanticFilename = "semantic.yaml"
	// ManifestFilename is the installed package manifest every installer writes
	// and discovery reads, so both sides name the same file.
	ManifestFilename = ".roca-plugin.json"
	MaxAttached      = 10
	// ProvenanceColumn names every row's source database, so a semantic layer
	// may not declare a column that would be overwritten by it.
	ProvenanceColumn = "database"
)

type Attachment string

const (
	AttachmentOnDemand Attachment = "on-demand"
	AttachmentResident Attachment = "resident"
)

type Semantic struct {
	Version     int             `yaml:"version" json:"version"`
	Attachment  Attachment      `yaml:"attachment" json:"attachment"`
	Description string          `yaml:"description" json:"description"`
	Questions   []string        `yaml:"questions" json:"questions,omitempty"`
	Custody     bool            `yaml:"custody" json:"custody"`
	Tables      []SemanticTable `yaml:"tables" json:"tables"`
}

type SemanticTable struct {
	Name        string   `yaml:"name" json:"name"`
	Description string   `yaml:"description" json:"description"`
	Questions   []string `yaml:"questions" json:"questions,omitempty"`
	Columns     []string `yaml:"columns" json:"columns"`
}

type Descriptor struct {
	Name         string    `json:"name"`
	Directory    string    `json:"-"`
	Database     string    `json:"-"`
	DatabaseName string    `json:"database_name,omitempty"`
	Schema       string    `json:"schema"`
	Retention    string    `json:"retention,omitempty"`
	Semantic     Semantic  `json:"semantic"`
	Manifest     *Manifest `json:"-"`
	SourceLabel  string    `json:"-"`
	relevance    int
}

type Table struct {
	Name        string
	Columns     []string
	Description string
	Questions   []string
}

type Database struct {
	Descriptor
	Tables []Table
}

func (d Database) ReadOnlyURI() string {
	return databaseURI(d.Database)
}

func (d Descriptor) Source() string {
	if d.SourceLabel != "" {
		return d.SourceLabel
	}
	return "plugin:" + d.Name
}

func Discover(root string) ([]Descriptor, []string) {
	if strings.TrimSpace(root) == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, []string{fmt.Sprintf("plugins could not be discovered: %v", err)}
	}

	var found []Descriptor
	var warnings []string
	for _, entry := range entries {
		if !entry.IsDir() || !validPluginName(entry.Name()) {
			continue
		}
		directory := filepath.Join(root, entry.Name())
		if executablePackage(directory) {
			continue
		}
		descriptors, err := InspectAll(entry.Name(), directory)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("plugin %s is unavailable: %v", entry.Name(), err))
			continue
		}
		found = append(found, descriptors...)
	}
	found, schemaWarnings := resolveSchemas(found)
	warnings = append(warnings, schemaWarnings...)
	slices.SortFunc(found, func(a, b Descriptor) int {
		if compared := strings.Compare(a.Name, b.Name); compared != 0 {
			return compared
		}
		return strings.Compare(a.DatabaseName, b.DatabaseName)
	})
	return found, warnings
}

func executablePackage(directory string) bool {
	file, err := os.Open(filepath.Join(directory, ManifestFilename))
	if err != nil {
		return false
	}
	defer file.Close()
	var manifest struct {
		Schema int    `json:"schema"`
		Kind   string `json:"kind"`
	}
	return json.NewDecoder(file).Decode(&manifest) == nil &&
		manifest.Schema == 1 && manifest.Kind == "executable"
}

// Inspect parses one plugin directory without requiring it to be installed.
// Installers use the same structural validation as query discovery.
func Inspect(name, directory string) (Descriptor, error) {
	descriptors, err := InspectAll(name, directory)
	if err != nil {
		return Descriptor{}, err
	}
	if len(descriptors) != 1 {
		return Descriptor{}, fmt.Errorf("plugin %s declares %d databases; inspect all of them", name, len(descriptors))
	}
	return descriptors[0], nil
}

// InspectAll parses every database declaration in a plugin. A directory that
// has not migrated yet continues through the legacy semantic.yaml reader;
// manifest-backed plugins never fall back when their declaration is malformed.
func InspectAll(name, directory string) ([]Descriptor, error) {
	if !validPluginName(name) {
		return nil, fmt.Errorf("invalid plugin name %q", name)
	}
	manifestPath := filepath.Join(directory, PackageFilename)
	if raw, err := os.ReadFile(manifestPath); err == nil {
		federated, err := Federated(raw)
		if err != nil {
			return nil, err
		}
		if federated {
			manifest, err := ReadManifest(manifestPath)
			if err != nil {
				return nil, err
			}
			if manifest.Name != name {
				return nil, fmt.Errorf("%s names plugin %q, not directory %q", PackageFilename, manifest.Name, name)
			}
			descriptors := make([]Descriptor, 0, len(manifest.Databases))
			multiple := len(manifest.Databases) > 1
			for _, declaration := range manifest.Databases {
				semantic, _ := manifest.semanticFor(declaration.Name)
				source := "plugin:" + name
				if multiple {
					source += "/" + declaration.Name
				}
				descriptors = append(descriptors, Descriptor{
					Name: name, Directory: directory,
					Database: filepath.Join(directory, declaration.Path), DatabaseName: declaration.Name,
					Schema: declaration.Alias, Retention: declaration.Retention,
					Semantic: semantic, Manifest: &manifest, SourceLabel: source,
				})
			}
			return descriptors, nil
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read %s: %w", PackageFilename, err)
	}
	semantic, err := readSemantic(filepath.Join(directory, SemanticFilename))
	if err != nil {
		return nil, err
	}
	database, err := soleDatabase(directory)
	if err != nil {
		return nil, err
	}
	return []Descriptor{{
		Name: name, Directory: directory, Database: database,
		Schema: schemaName(name), Semantic: semantic,
	}}, nil
}

// disambiguateSchemas rewrites only the aliases discovery derives from a
// directory name, and it weighs them against every alias in play: a derived
// alias yields to a declared one, so a legacy directory can never collide the
// manifest that named the same alias out of the catalogue.
func disambiguateSchemas(descriptors []Descriptor) {
	counts := schemaCounts(descriptors)
	for index := range descriptors {
		if descriptors[index].Manifest != nil || counts[descriptors[index].Schema] < 2 {
			continue
		}
		digest := sha256.Sum256([]byte(descriptors[index].Name))
		descriptors[index].Schema += fmt.Sprintf("_%x", digest[:4])
	}
}

func schemaCounts(descriptors []Descriptor) map[string]int {
	counts := make(map[string]int, len(descriptors))
	for _, descriptor := range descriptors {
		counts[descriptor.Schema]++
	}
	return counts
}

func resolveSchemas(descriptors []Descriptor) ([]Descriptor, []string) {
	disambiguateSchemas(descriptors)
	counts := schemaCounts(descriptors)
	conflicts := map[string]bool{}
	var found []Descriptor
	for _, descriptor := range descriptors {
		if counts[descriptor.Schema] > 1 {
			conflicts[descriptor.Schema] = true
			continue
		}
		found = append(found, descriptor)
	}
	aliases := make([]string, 0, len(conflicts))
	for alias := range conflicts {
		aliases = append(aliases, alias)
	}
	slices.Sort(aliases)
	var warnings []string
	for _, alias := range aliases {
		warnings = append(warnings, fmt.Sprintf(
			"plugin attach alias %q is declared more than once; conflicting plugins are unavailable", alias))
	}
	return found, warnings
}

func readSemantic(path string) (Semantic, error) {
	file, err := os.Open(path)
	if err != nil {
		return Semantic{}, fmt.Errorf("read %s: %w", SemanticFilename, err)
	}
	defer file.Close()
	decoder := yaml.NewDecoder(file)
	decoder.KnownFields(true)
	var semantic Semantic
	if err := decoder.Decode(&semantic); err != nil {
		return Semantic{}, fmt.Errorf("parse %s: %w", SemanticFilename, err)
	}
	if semantic.Attachment == "" {
		semantic.Attachment = AttachmentOnDemand
	}
	if err := semantic.valid(); err != nil {
		return Semantic{}, err
	}
	return semantic, nil
}

func (s Semantic) valid() error {
	return s.validFor(SemanticFilename)
}

func (s Semantic) validFor(source string) error {
	if s.Version != 1 {
		return fmt.Errorf("%s semantic version is %d, want 1", source, s.Version)
	}
	if s.Attachment != AttachmentOnDemand && s.Attachment != AttachmentResident {
		return fmt.Errorf("%s attachment is %q, want %q or %q", source,
			s.Attachment, AttachmentResident, AttachmentOnDemand)
	}
	if strings.TrimSpace(s.Description) == "" {
		return fmt.Errorf("%s has no description", source)
	}
	if len(s.Tables) == 0 {
		return fmt.Errorf("%s describes no tables", source)
	}
	servesQuestions := len(s.Questions) > 0
	seen := map[string]bool{}
	for _, table := range s.Tables {
		if !validIdentifier(table.Name) || seen[table.Name] {
			return fmt.Errorf("%s has an invalid or repeated table %q", source, table.Name)
		}
		seen[table.Name] = true
		if strings.TrimSpace(table.Description) == "" || len(table.Columns) == 0 {
			return fmt.Errorf("table %s needs a description and columns", table.Name)
		}
		servesQuestions = servesQuestions || len(table.Questions) > 0
		columns := map[string]bool{}
		for _, column := range table.Columns {
			if !validIdentifier(column) || columns[column] {
				return fmt.Errorf("table %s has an invalid or repeated column %q", table.Name, column)
			}
			if strings.EqualFold(column, ProvenanceColumn) {
				return fmt.Errorf("table %s declares the reserved column %q, which carries row provenance",
					table.Name, column)
			}
			columns[column] = true
		}
	}
	if !servesQuestions {
		return fmt.Errorf("%s declares no questions it serves", source)
	}
	return nil
}

func soleDatabase(directory string) (string, error) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return "", err
	}
	var databases []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		extension := strings.ToLower(filepath.Ext(entry.Name()))
		if extension == ".db" || extension == ".sqlite" || extension == ".sqlite3" {
			databases = append(databases, filepath.Join(directory, entry.Name()))
		}
	}
	if len(databases) != 1 {
		return "", fmt.Errorf("expected one .db or .sqlite database, found %d", len(databases))
	}
	return databases[0], nil
}

// Relevant ranks every candidate whose semantic layer speaks to the question.
// It does not truncate: the SQLite attachment cap has one owner, and it is the
// caller that attaches.
func Relevant(question string, candidates []Descriptor) []Descriptor {
	ranked := make([]Descriptor, 0, len(candidates))
	for _, candidate := range candidates {
		candidate.relevance = relevance(question, candidate)
		if candidate.relevance > 0 {
			ranked = append(ranked, candidate)
		}
	}
	slices.SortStableFunc(ranked, func(a, b Descriptor) int {
		if a.relevance != b.relevance {
			return b.relevance - a.relevance
		}
		return strings.Compare(a.Name, b.Name)
	})
	return ranked
}

func Referenced(statement string, candidates []Descriptor) []Descriptor {
	type hit struct {
		descriptor Descriptor
		position   int
	}
	var hits []hit
	for _, candidate := range candidates {
		pattern := regexp.MustCompile(`(?i)(?:\b` + regexp.QuoteMeta(candidate.Schema) +
			`\b|"` + regexp.QuoteMeta(candidate.Schema) + `")\s*\.`)
		if location := pattern.FindStringIndex(statement); location != nil {
			hits = append(hits, hit{candidate, location[0]})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].position < hits[j].position })
	referenced := make([]Descriptor, 0, len(hits))
	for _, item := range hits {
		referenced = append(referenced, item.descriptor)
	}
	return referenced
}

func relevance(question string, candidate Descriptor) int {
	normalized := query.Normalize(question)
	wanted := tokenSet(normalized)
	if len(wanted) == 0 {
		return 0
	}
	texts := []string{candidate.Name, candidate.Semantic.Description}
	for _, table := range candidate.Semantic.Tables {
		texts = append(texts, table.Name, table.Description)
	}
	score := 0
	for _, declared := range candidate.Semantic.Questions {
		phrase := query.Normalize(declared)
		if phrase != "" && (normalized == phrase || strings.Contains(normalized, phrase)) {
			score += 1000
		}
	}
	for _, table := range candidate.Semantic.Tables {
		for _, declared := range table.Questions {
			phrase := query.Normalize(declared)
			if phrase != "" && (normalized == phrase || strings.Contains(normalized, phrase)) {
				score += 1000
			}
		}
	}
	seen := map[string]bool{}
	for token := range tokenSet(strings.Join(texts, " ")) {
		if wanted[token] && !seen[token] {
			score += 10
			seen[token] = true
		}
	}
	if wanted[query.Normalize(candidate.Name)] {
		score += 30
	}
	return score
}

func tokenSet(text string) map[string]bool {
	tokens := map[string]bool{}
	for _, token := range strings.Fields(query.Normalize(text)) {
		if len([]rune(token)) >= 3 {
			tokens[token] = true
		}
	}
	return tokens
}

func Validate(ctx context.Context, descriptor Descriptor) (Database, error) {
	db, err := sql.Open("sqlite", databaseURI(descriptor.Database))
	if err != nil {
		return Database{}, fmt.Errorf("open plugin %s read-only: %w", descriptor.Name, err)
	}
	defer db.Close()
	if err := db.PingContext(ctx); err != nil {
		return Database{}, fmt.Errorf("open plugin %s read-only: %w", descriptor.Name, err)
	}

	actual, err := inspectTables(ctx, db)
	if err != nil {
		return Database{}, fmt.Errorf("inspect plugin %s: %w", descriptor.Name, err)
	}
	declared := make(map[string]SemanticTable, len(descriptor.Semantic.Tables))
	for _, table := range descriptor.Semantic.Tables {
		declared[table.Name] = table
	}
	for name, columns := range actual {
		if sqlgate.IsHiddenTable(name) {
			continue
		}
		table, ok := declared[name]
		if !ok {
			return Database{}, fmt.Errorf("semantic layer omits database table %s", name)
		}
		if !slices.Equal(columns, table.Columns) {
			return Database{}, fmt.Errorf("semantic layer columns for %s are %v but the database has %v",
				name, table.Columns, columns)
		}
	}
	for name := range declared {
		if sqlgate.IsHiddenTable(name) {
			continue
		}
		if _, ok := actual[name]; !ok {
			return Database{}, fmt.Errorf("semantic layer describes missing database table %s", name)
		}
	}

	tables := make([]Table, 0, len(descriptor.Semantic.Tables))
	for _, table := range descriptor.Semantic.Tables {
		if sqlgate.IsHiddenTable(table.Name) {
			continue
		}
		tables = append(tables, Table{
			Name: table.Name, Columns: slices.Clone(table.Columns),
			Description: table.Description, Questions: slices.Clone(table.Questions),
		})
	}
	return Database{Descriptor: descriptor, Tables: tables}, nil
}

// databaseURI resolves the path first because a plugin root reached through a
// relative path would put its first segment in the URI authority, which SQLite
// refuses to open at all.
func databaseURI(path string) string {
	if absolute, err := filepath.Abs(path); err == nil {
		path = absolute
	}
	uri := url.URL{Scheme: "file", Path: filepath.ToSlash(path),
		RawQuery: url.Values{"mode": {"ro"}}.Encode()}
	return uri.String()
}

func inspectTables(ctx context.Context, db *sql.DB) (map[string][]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT name FROM sqlite_schema
		WHERE type IN ('table', 'view') AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	actual := make(map[string][]string, len(names))
	for _, name := range names {
		columns, err := db.QueryContext(ctx, "PRAGMA table_info("+quoteIdentifier(name)+")")
		if err != nil {
			return nil, err
		}
		for columns.Next() {
			var cid, notNull, primaryKey int
			var column, kind string
			var defaultValue any
			if err := columns.Scan(&cid, &column, &kind, &notNull, &defaultValue, &primaryKey); err != nil {
				columns.Close()
				return nil, err
			}
			actual[name] = append(actual[name], column)
		}
		if err := columns.Close(); err != nil {
			return nil, err
		}
	}
	return actual, nil
}

func schemaName(name string) string {
	var b strings.Builder
	b.WriteString("plugin_")
	for _, char := range name {
		if unicode.IsLetter(char) || unicode.IsDigit(char) || char == '_' {
			b.WriteRune(char)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}

// validPluginName refuses a leading dot: the plugin root also holds installer
// scratch state, and only what an installer named without a dot is a plugin.
func validPluginName(name string) bool {
	if name == "" || strings.HasPrefix(name, ".") {
		return false
	}
	for _, char := range name {
		if !unicode.IsLetter(char) && !unicode.IsDigit(char) && !strings.ContainsRune("-_.", char) {
			return false
		}
	}
	return true
}

func validIdentifier(name string) bool {
	if name == "" {
		return false
	}
	for index, char := range name {
		if index == 0 && !unicode.IsLetter(char) && char != '_' {
			return false
		}
		if index > 0 && !unicode.IsLetter(char) && !unicode.IsDigit(char) && char != '_' {
			return false
		}
	}
	return true
}

func quoteIdentifier(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}
