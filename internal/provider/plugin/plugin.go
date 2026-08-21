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
	"time"
	"unicode"

	"github.com/thellmwhisperer/la-roca/internal/provider/query"
	"github.com/thellmwhisperer/la-roca/internal/provider/query/sqlgate"
	"github.com/thellmwhisperer/la-roca/internal/store"
	"gopkg.in/yaml.v3"
	_ "modernc.org/sqlite"
)

const (
	SemanticFilename = "semantic.yaml"
	// ManifestFilename is the installed package manifest every installer writes
	// and discovery reads, so both sides name the same file.
	ManifestFilename = ".roca-plugin.json"
	// BundledSource is the installed source only this build's own installer
	// writes. An installable third-party reference is a directory, a URL, or
	// owner/repo, so no installation an operator asks for can record it.
	BundledSource = "bundled:roca"
	MaxAttached   = 10
	// ProvenanceColumn names every row's source database, so a semantic layer
	// may not declare a column that would be overwritten by it.
	ProvenanceColumn = "database"
	// busyTimeout is how long a read-only open waits on a plugin database that
	// another process is recovering or writing, instead of failing the open with
	// "database is locked". Two agents can start at once, and both open the
	// resident plugins.
	busyTimeout = 15 * time.Second
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
	Name         string        `json:"name"`
	Directory    string        `json:"-"`
	Database     string        `json:"-"`
	DatabaseName string        `json:"database_name,omitempty"`
	Schema       string        `json:"schema"`
	Retention    string        `json:"retention,omitempty"`
	Semantic     Semantic      `json:"semantic"`
	VectorTables []VectorTable `json:"vector_tables,omitempty"`
	Manifest     *Manifest     `json:"-"`
	SourceLabel  string        `json:"-"`
	relevance    int
}

type Table struct {
	Name        string
	Columns     []string
	Description string
	Questions   []string
	FTS5        bool
}

type Database struct {
	Descriptor
	Tables   []Table
	snapshot *store.ReadOnlySnapshot
}

func (d Database) ReadOnlyURI() string {
	if d.snapshot != nil {
		return d.snapshot.URI()
	}
	return databaseURI(d.Database)
}

func (d Database) Close() error {
	if d.snapshot == nil {
		return nil
	}
	return d.snapshot.Close()
}

func (d Descriptor) Source() string {
	if d.SourceLabel != "" {
		return d.SourceLabel
	}
	return "plugin:" + d.Name
}

// databaseLabel names one database inside its package. A descriptor that has
// not migrated to a manifest declares no database name, so the alias it is
// attached under is the only name it has.
func (d Descriptor) databaseLabel() string {
	if d.DatabaseName != "" {
		return d.DatabaseName
	}
	return d.Schema
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

// installedPackage is the part of the local installation inventory discovery
// reads: the kind that decides whether a directory is a data plugin at all, and
// the source that says whether this build installed it itself.
type installedPackage struct {
	Schema int    `json:"schema"`
	Kind   string `json:"kind"`
	Source string `json:"source"`
}

func readInstalledPackage(directory string) (installedPackage, bool) {
	file, err := os.Open(filepath.Join(directory, ManifestFilename))
	if err != nil {
		return installedPackage{}, false
	}
	defer file.Close()
	var manifest installedPackage
	if err := json.NewDecoder(file).Decode(&manifest); err != nil {
		return installedPackage{}, false
	}
	return manifest, true
}

func executablePackage(directory string) bool {
	manifest, ok := readInstalledPackage(directory)
	return ok && manifest.Schema == 1 && manifest.Kind == "executable"
}

func bundledPackage(directory string) bool {
	manifest, ok := readInstalledPackage(directory)
	return ok && manifest.Source == BundledSource
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
					Semantic: semantic, VectorTables: manifest.vectorFor(declaration.Name),
					Manifest: &manifest, SourceLabel: source,
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

// resolveSchemas settles every alias more than one descriptor declares, and
// names the plugins that declared it so the operator knows which installation
// to act on. A package this build installed itself keeps the alias it declared:
// a third-party declaration may make itself unavailable, never the bundled seat
// it collides with. Between equals the collision stays an error, because a
// declared alias is not a name the kernel rewrites.
func resolveSchemas(descriptors []Descriptor) ([]Descriptor, []string) {
	disambiguateSchemas(descriptors)
	counts := schemaCounts(descriptors)
	claims := map[string][]Descriptor{}
	var found []Descriptor
	for _, descriptor := range descriptors {
		if counts[descriptor.Schema] < 2 {
			found = append(found, descriptor)
			continue
		}
		claims[descriptor.Schema] = append(claims[descriptor.Schema], descriptor)
	}
	aliases := make([]string, 0, len(claims))
	for alias := range claims {
		aliases = append(aliases, alias)
	}
	slices.Sort(aliases)
	var warnings []string
	for _, alias := range aliases {
		kept, evicted := settleClaim(claims[alias])
		if kept == nil {
			warnings = append(warnings, fmt.Sprintf(
				"plugin attach alias %q is declared by %s; those plugins are unavailable",
				alias, strings.Join(pluginNames(evicted), ", ")))
			continue
		}
		found = append(found, *kept)
		warnings = append(warnings, fmt.Sprintf(
			"plugin attach alias %q belongs to the bundled %s plugin; %s is unavailable",
			alias, kept.Name, strings.Join(pluginNames(evicted), ", ")))
	}
	return found, warnings
}

// settleClaim answers which claimant, if any, keeps a contested alias. Exactly
// one bundled claimant wins it; anything else leaves every claimant without it.
func settleClaim(claimants []Descriptor) (*Descriptor, []Descriptor) {
	var bundled, others []Descriptor
	for _, claimant := range claimants {
		if bundledPackage(claimant.Directory) {
			bundled = append(bundled, claimant)
			continue
		}
		others = append(others, claimant)
	}
	if len(bundled) != 1 {
		return nil, claimants
	}
	return &bundled[0], others
}

func pluginNames(descriptors []Descriptor) []string {
	names := make([]string, 0, len(descriptors))
	for _, descriptor := range descriptors {
		if !slices.Contains(names, descriptor.Name) {
			names = append(names, descriptor.Name)
		}
	}
	slices.Sort(names)
	return names
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
	return validate(ctx, descriptor, false)
}

func ValidateImmutable(ctx context.Context, descriptor Descriptor) (Database, error) {
	return validate(ctx, descriptor, true)
}

func validate(ctx context.Context, descriptor Descriptor, immutable bool) (Database, error) {
	var db *sql.DB
	var snapshot *store.ReadOnlySnapshot
	var err error
	if immutable {
		snapshot, err = store.OpenReadOnlySnapshot(ctx, descriptor.Database)
		if err == nil {
			db = snapshot.SQL()
		}
	} else {
		db, err = sql.Open("sqlite", databaseURI(descriptor.Database))
	}
	if err != nil {
		return Database{}, fmt.Errorf("open plugin %s read-only: %w", descriptor.Name, err)
	}
	keepSnapshot := false
	defer func() {
		if snapshot != nil && !keepSnapshot {
			_ = snapshot.Close()
		}
		if snapshot == nil {
			_ = db.Close()
		}
	}()
	if err := db.PingContext(ctx); err != nil {
		return Database{}, fmt.Errorf("open plugin %s read-only: %w", descriptor.Name, err)
	}

	actual, err := inspectTables(ctx, db)
	if err != nil {
		return Database{}, fmt.Errorf("inspect plugin %s: %w", descriptor.Name, err)
	}
	declared := make(map[string]SemanticTable, len(descriptor.Semantic.Tables))
	shadows := make(map[string]bool)
	for name, table := range actual {
		if table.FTS5 {
			for _, shadow := range sqlgate.FTS5ShadowTables(name) {
				shadows[shadow] = true
			}
		}
	}
	for _, table := range descriptor.Semantic.Tables {
		declared[table.Name] = table
	}
	for name, inspected := range actual {
		if sqlgate.IsHiddenTable(name) || shadows[strings.ToLower(name)] {
			continue
		}
		table, ok := declared[name]
		if !ok {
			return Database{}, fmt.Errorf("semantic layer omits database table %s", name)
		}
		if !slices.Equal(inspected.Columns, table.Columns) {
			return Database{}, fmt.Errorf("semantic layer columns for %s are %v but the database has %v",
				name, table.Columns, inspected.Columns)
		}
	}
	for name := range declared {
		if sqlgate.IsHiddenTable(name) || shadows[strings.ToLower(name)] {
			continue
		}
		if _, ok := actual[name]; !ok {
			return Database{}, fmt.Errorf("semantic layer describes missing database table %s", name)
		}
	}
	for _, table := range descriptor.VectorTables {
		inspected, exists := actual[table.Name]
		if !exists {
			return Database{}, fmt.Errorf("vector layer describes missing database table %s", table.Name)
		}
		columns := make(map[string]bool, len(inspected.Columns))
		for _, column := range inspected.Columns {
			columns[column] = true
		}
		if !columns[table.IDColumn] {
			return Database{}, fmt.Errorf("vector layer id column %s.%s is missing from the database",
				table.Name, table.IDColumn)
		}
		for _, column := range table.TextColumns {
			if !columns[column] {
				return Database{}, fmt.Errorf("vector layer text column %s.%s is missing from the database",
					table.Name, column)
			}
		}
	}

	tables := make([]Table, 0, len(descriptor.Semantic.Tables))
	for _, table := range descriptor.Semantic.Tables {
		if sqlgate.IsHiddenTable(table.Name) || shadows[strings.ToLower(table.Name)] {
			continue
		}
		tables = append(tables, Table{
			Name: table.Name, Columns: slices.Clone(table.Columns),
			Description: table.Description, Questions: slices.Clone(table.Questions),
			FTS5: actual[table.Name].FTS5,
		})
	}
	descriptor.VectorTables = slices.Clone(descriptor.VectorTables)
	for index := range descriptor.VectorTables {
		descriptor.VectorTables[index] = cloneVectorTable(descriptor.VectorTables[index])
	}
	keepSnapshot = true
	return Database{Descriptor: descriptor, Tables: tables, snapshot: snapshot}, nil
}

// databaseURI resolves the path first because a plugin root reached through a
// relative path would put its first segment in the URI authority, which SQLite
// refuses to open at all. It is always read-only, and it waits on the busy
// timeout instead of failing the open the moment another process holds a write
// lock on the same plugin database.
func databaseURI(path string) string {
	if absolute, err := filepath.Abs(path); err == nil {
		path = absolute
	}
	values := url.Values{
		"mode":    {"ro"},
		"_pragma": {fmt.Sprintf("busy_timeout(%d)", busyTimeout.Milliseconds())},
	}
	uri := url.URL{Scheme: "file", Path: filepath.ToSlash(path), RawQuery: values.Encode()}
	return uri.String()
}

type inspectedTable struct {
	Columns []string
	FTS5    bool
}

func inspectTables(ctx context.Context, db *sql.DB) (map[string]inspectedTable, error) {
	rows, err := db.QueryContext(ctx, `SELECT name, sql FROM sqlite_schema
		WHERE type IN ('table', 'view') AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	actual := make(map[string]inspectedTable)
	for rows.Next() {
		var name string
		var ddl sql.NullString
		if err := rows.Scan(&name, &ddl); err != nil {
			return nil, err
		}
		actual[name] = inspectedTable{FTS5: sqlgate.IsFTS5DDL(ddl.String)}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for name, inspected := range actual {
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
			inspected.Columns = append(inspected.Columns, column)
		}
		if err := columns.Close(); err != nil {
			return nil, err
		}
		actual[name] = inspected
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
