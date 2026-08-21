/*
*
@overview Declarative vector generation across plugin databases. ~730 lines, 13 public symbols, owns registry loading and sidecar orchestration.

	READING GUIDE
	-------------
	1. Start at Federation.Ingest       <- federated delta and GC orchestration
	2. Read DeclaredCorpus.WalkSources  <- manifest surface to read-only SQL
	3. Read LoadFederation              <- registry and path safety boundary
	4. Everything else is storage metadata and helpers

	MAIN FLOW
	---------
	LoadFederation -> Federation.Ingest -> DeclaredCorpus.WalkSources -> Index.Ingest -> sealSidecar

	PUBLIC API
	----------
	LoadFederation()       Load and validate the kernel-generated registry
	SidecarPath()          Derive the database-owned adjacent vector path
	Federation.Ingest()    Sweep every selected declared database incrementally
	Federation.CorpusIndex() Return the corpus compatibility query seat
	Federation.Compact()   Repack every existing declared sidecar
	Federation.HasSidecars() Report whether any declared sidecar exists
	Federation.RemoveLegacyMonolith() Remove the superseded central derived index
	Federation.ConfiguredModel() Read the corpus sidecar's selected model
	FederatedWorker.Run() Run the background federated build lifecycle
	FederationDelta        Aggregate and per-owner delta report
	DatabaseDelta          One database owner's delta report
	CompactFederationReport Aggregate compaction report
	DeclaredCorpus         Read and resolve one database's declared surfaces

	INTERNALS
	---------
	vectorRegistry, vectorDatabase, vectorTable, chunkingHints
	loadVectorRegistry, validateRegistry, databaseFingerprint, unchangedSidecar
	sealSidecar, declaredTextExpression, quoteIdentifier, validIdentifier

@exports LoadFederation, SidecarPath, Federation, FederationDelta, DatabaseDelta, CompactFederationReport, DeclaredCorpus, FederatedWorker
@deps CoreCLI and Index from this package; pkg/incrementality; encoding/json; filesystem and SQLite
*/
package vector

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/thellmwhisperer/la-roca/pkg/incrementality"
)

const (
	vectorRegistryFilename = "vector-registry.json"
	vectorRegistrySchema   = 1
	declaredReaderVersion  = "declared-surfaces-v1"
)

var (
	manifestPluginName = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
	sqlIdentifier      = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// -- 1/7 HELPER · Registry contract and safe path loading --

type vectorRegistry struct {
	Schema    int              `json:"schema"`
	Databases []vectorDatabase `json:"databases"`
}

type vectorDatabase struct {
	Plugin   string        `json:"plugin"`
	Database string        `json:"database"`
	Path     string        `json:"path"`
	Alias    string        `json:"alias"`
	Tables   []vectorTable `json:"tables"`
}

type vectorTable struct {
	Name        string         `json:"name"`
	IDColumn    string         `json:"id_column"`
	TextColumns []string       `json:"text_columns"`
	Chunking    *chunkingHints `json:"chunking,omitempty"`
}

type chunkingHints struct {
	MaxChars     *int `json:"max_chars,omitempty"`
	OverlapChars *int `json:"overlap_chars,omitempty"`
}

type Federation struct {
	Core         CoreCLI
	PluginRoot   string
	Model        string
	BuildVersion string
	Embedder     Embedder
	Notice       func(string)
	databases    []vectorDatabase
}

func LoadFederation(core CoreCLI, pluginRoot, model, buildVersion string,
	embedder Embedder, notice func(string)) (Federation, error) {
	absolute, err := filepath.Abs(pluginRoot)
	if err != nil {
		return Federation{}, fmt.Errorf("resolve plugin root: %w", err)
	}
	registry, err := loadVectorRegistry(filepath.Join(absolute, vectorRegistryFilename))
	if err != nil {
		return Federation{}, err
	}
	if len(registry.Databases) == 0 {
		return Federation{}, fmt.Errorf("vector registry declares no databases")
	}
	if buildVersion == "" {
		buildVersion = "dev"
	}
	return Federation{Core: core, PluginRoot: absolute, Model: model,
		BuildVersion: buildVersion, Embedder: embedder, Notice: notice,
		databases: registry.Databases}, nil
}

func loadVectorRegistry(path string) (vectorRegistry, error) {
	file, err := os.Open(path)
	if err != nil {
		return vectorRegistry{}, fmt.Errorf("read vector registry: %w", err)
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	var registry vectorRegistry
	if err := decoder.Decode(&registry); err != nil {
		return vectorRegistry{}, fmt.Errorf("read vector registry: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return vectorRegistry{}, fmt.Errorf("read vector registry: contains more than one JSON value")
		}
		return vectorRegistry{}, fmt.Errorf("read vector registry: %w", err)
	}
	if err := validateRegistry(registry); err != nil {
		return vectorRegistry{}, err
	}
	return registry, nil
}

func validateRegistry(registry vectorRegistry) error {
	if registry.Schema != vectorRegistrySchema {
		return fmt.Errorf("vector registry schema is %d, want %d", registry.Schema, vectorRegistrySchema)
	}
	seenDatabases := map[string]bool{}
	databasePaths := map[string]string{}
	for _, database := range registry.Databases {
		owner := database.owner()
		if !manifestPluginName.MatchString(database.Plugin) || !validIdentifier(database.Database) ||
			seenDatabases[owner] {
			return fmt.Errorf("vector registry has invalid or repeated database %q", owner)
		}
		seenDatabases[owner] = true
		if filepath.Base(database.Path) != database.Path || filepath.Clean(database.Path) != database.Path {
			return fmt.Errorf("vector registry database %s has unsafe path %q", owner, database.Path)
		}
		switch strings.ToLower(filepath.Ext(database.Path)) {
		case ".db", ".sqlite", ".sqlite3":
		default:
			return fmt.Errorf("vector registry database %s has invalid SQLite path %q", owner, database.Path)
		}
		packagePath := filepath.Join(database.Plugin, database.Path)
		if previous := databasePaths[packagePath]; previous != "" {
			return fmt.Errorf("vector registry databases %s and %s share path %q", previous, owner, packagePath)
		}
		databasePaths[packagePath] = owner
		if !validIdentifier(database.Alias) || len(database.Tables) == 0 {
			return fmt.Errorf("vector registry database %s needs an alias and tables", owner)
		}
		seenTables := map[string]bool{}
		for _, table := range database.Tables {
			if !validIdentifier(table.Name) || !validIdentifier(table.IDColumn) ||
				seenTables[table.Name] || len(table.TextColumns) == 0 {
				return fmt.Errorf("vector registry database %s has invalid table %q", owner, table.Name)
			}
			seenTables[table.Name] = true
			seenColumns := map[string]bool{}
			for _, column := range table.TextColumns {
				if !validIdentifier(column) || seenColumns[column] {
					return fmt.Errorf("vector registry table %s/%s has invalid text column %q",
						owner, table.Name, column)
				}
				seenColumns[column] = true
			}
			if err := validateChunking(table.Chunking); err != nil {
				return fmt.Errorf("vector registry table %s/%s: %w", owner, table.Name, err)
			}
		}
	}
	for _, database := range registry.Databases {
		sidecar := filepath.Join(database.Plugin, SidecarPath(database.Path))
		if sourceOwner := databasePaths[sidecar]; sourceOwner != "" {
			return fmt.Errorf("vector registry sidecar for %s collides with database %s at %q",
				database.owner(), sourceOwner, sidecar)
		}
	}
	return nil
}

func validateChunking(hints *chunkingHints) error {
	if hints == nil {
		return nil
	}
	if hints.MaxChars != nil && *hints.MaxChars <= 0 {
		return fmt.Errorf("max_chars must be positive")
	}
	if hints.OverlapChars != nil && *hints.OverlapChars < 0 {
		return fmt.Errorf("overlap_chars must not be negative")
	}
	if hints.MaxChars != nil && hints.OverlapChars != nil && *hints.OverlapChars >= *hints.MaxChars {
		return fmt.Errorf("overlap_chars must be smaller than max_chars")
	}
	return nil
}

// -/ 1/7

// -- 2/7 CORE · Federation.Ingest <- START HERE --

type FederationDelta struct {
	Delta
	Databases []DatabaseDelta `json:"databases"`
}

type DatabaseDelta struct {
	Owner  string `json:"owner"`
	Counts Delta  `json:"counts"`
}

func (f Federation) Ingest(ctx context.Context, sourceKind string) (FederationDelta, error) {
	if f.Model == "" || f.Embedder == nil {
		return FederationDelta{}, fmt.Errorf("embedding model and provider are required")
	}
	result := FederationDelta{Databases: []DatabaseDelta{}}
	matched := sourceKind == ""
	for _, database := range f.databases {
		reader := DeclaredCorpus{Core: f.Core, Database: database}
		if sourceKind != "" && !reader.hasTable(sourceKind) {
			continue
		}
		matched = true
		databasePath := f.databasePath(database)
		sidecar := SidecarPath(databasePath)
		if err := assertSidecarOwner(sidecar, database.owner()); err != nil {
			return FederationDelta{}, err
		}
		contract := database.contractFingerprint()
		fingerprint, err := databaseFingerprint(databasePath, contract)
		if err != nil {
			return FederationDelta{}, fmt.Errorf("fingerprint vector source %s: %w", database.owner(), err)
		}
		var delta Delta
		if sourceKind == "" {
			delta, err = unchangedSidecar(sidecar, database.owner(), f.Model, contract, fingerprint)
			if err == nil {
				if err := sealSidecar(sidecar, database.owner(), f.Model, f.BuildVersion, contract, fingerprint); err != nil {
					return FederationDelta{}, err
				}
				result.add(database.owner(), delta)
				continue
			}
			if !errors.Is(err, errSidecarChanged) {
				return FederationDelta{}, err
			}
		}
		index := f.index(database, reader, sidecar)
		if sourceKind == "" {
			delta, err = index.Ingest(ctx)
		} else {
			delta, err = index.IngestSource(ctx, sourceKind)
		}
		if err != nil {
			return FederationDelta{}, fmt.Errorf("index vector source %s: %w", database.owner(), err)
		}
		storedFingerprint := ""
		if sourceKind == "" {
			storedFingerprint = fingerprint
		}
		if err := sealSidecar(sidecar, database.owner(), f.Model, f.BuildVersion, contract, storedFingerprint); err != nil {
			return FederationDelta{}, err
		}
		result.add(database.owner(), delta)
	}
	if !matched {
		return FederationDelta{}, fmt.Errorf("unknown vector source %q", sourceKind)
	}
	return result, nil
}

func (r *FederationDelta) add(owner string, delta Delta) {
	r.Added += delta.Added
	r.Updated += delta.Updated
	r.Removed += delta.Removed
	r.Unchanged += delta.Unchanged
	r.Sources += delta.Sources
	r.Chunks += delta.Chunks
	r.Databases = append(r.Databases, DatabaseDelta{Owner: owner, Counts: delta})
}

func (f Federation) index(database vectorDatabase, reader DeclaredCorpus, sidecar string) Index {
	kinds := make(map[string]bool, len(database.Tables))
	for _, table := range database.Tables {
		kinds[table.Name] = true
	}
	return Index{Corpus: reader, VectorPath: sidecar, Model: f.Model,
		Embedder: f.Embedder, Notice: f.Notice, SourceKinds: kinds}
}

// -/ 2/7

// -- 3/7 CORE · DeclaredCorpus.WalkSources and ResolveSource --

type DeclaredCorpus struct {
	Core     CoreCLI
	Database vectorDatabase
}

func (d DeclaredCorpus) WalkSources(ctx context.Context, sourceKind string,
	visit func(sourceRow) error) error {
	tables := d.Database.Tables
	if sourceKind != "" {
		table, ok := d.table(sourceKind)
		if !ok {
			return fmt.Errorf("unknown vector source %q", sourceKind)
		}
		tables = []vectorTable{table}
	}
	for _, table := range tables {
		cursor := ""
		for {
			rows, err := d.Core.query(ctx, d.pageQuery(table, cursor))
			if err != nil {
				return fmt.Errorf("read declared surface %s/%s: %w", d.Database.owner(), table.Name, err)
			}
			for _, values := range rows {
				id := stringValue(values["source_id"])
				text := stringValue(values["text"])
				if id == "" || text == "" {
					continue
				}
				cursor = id
				size, overlap := table.chunking()
				row := sourceRow{kind: table.Name, sourceID: id, text: text,
					chunkSize: size, overlap: overlap,
					fingerprintVersion: table.contractFingerprint()}
				if err := visit(row); err != nil {
					return err
				}
			}
			if len(rows) < walkPageSize {
				break
			}
		}
	}
	return nil
}

func (d DeclaredCorpus) ResolveSource(ctx context.Context, kind string, where locator) (string, error) {
	table, ok := d.table(kind)
	if !ok {
		return "", fmt.Errorf("unknown vector source %q", kind)
	}
	if where.SourceID == "" {
		return "", nil
	}
	statement := fmt.Sprintf(`SELECT %s AS text FROM %s.%s WHERE CAST(%s AS TEXT)=%s`,
		declaredTextExpression(table.TextColumns), quoteIdentifier(d.Database.Alias),
		quoteIdentifier(table.Name), quoteIdentifier(table.IDColumn), sqlLiteral(where.SourceID))
	rows, err := d.Core.query(ctx, statement)
	if err != nil {
		return "", err
	}
	for _, values := range rows {
		text := stringValue(values["text"])
		candidate := sourceRow{kind: kind, sourceID: where.SourceID, text: text,
			fingerprintVersion: table.contractFingerprint()}
		if candidate.identity() == where.Identity {
			return text, nil
		}
	}
	return "", nil
}

func (d DeclaredCorpus) pageQuery(table vectorTable, cursor string) string {
	return fmt.Sprintf(`WITH vector_rows AS (
		SELECT CAST(%s AS TEXT) AS source_id,%s AS text FROM %s.%s WHERE %s IS NOT NULL
	) SELECT source_id,text FROM vector_rows
	WHERE source_id<>'' AND text<>'' AND source_id>%s ORDER BY source_id LIMIT %d`,
		quoteIdentifier(table.IDColumn), declaredTextExpression(table.TextColumns),
		quoteIdentifier(d.Database.Alias), quoteIdentifier(table.Name), quoteIdentifier(table.IDColumn),
		sqlLiteral(cursor), walkPageSize)
}

func (d DeclaredCorpus) table(name string) (vectorTable, bool) {
	for _, table := range d.Database.Tables {
		if table.Name == name {
			return table, true
		}
	}
	return vectorTable{}, false
}

func (d DeclaredCorpus) hasTable(name string) bool {
	_, ok := d.table(name)
	return ok
}

func declaredTextExpression(columns []string) string {
	parts := make([]string, len(columns))
	for index, column := range columns {
		parts[index] = fmt.Sprintf("COALESCE(CAST(%s AS TEXT),'')", quoteIdentifier(column))
	}
	return "trim(" + strings.Join(parts, " || char(10) || char(10) || ") + ")"
}

// -/ 3/7

// -- 4/7 HELPER · Fingerprints and unchanged database fast path --

var errSidecarChanged = errors.New("sidecar source changed")

func databaseFingerprint(path, contract string) (string, error) {
	return incrementality.TargetFingerprint(incrementality.Target{
		Path: path, Kind: "vector-database", ParserVersion: declaredReaderVersion + ":" + contract,
		IncludeSQLiteWAL: true,
	})
}

func unchangedSidecar(path, owner, model, contract, sourceFingerprint string) (Delta, error) {
	store, err := openSQLite(path, true)
	if err != nil {
		if os.IsNotExist(err) || strings.Contains(err.Error(), "unable to open database file") {
			return Delta{}, errSidecarChanged
		}
		return Delta{}, fmt.Errorf("inspect vector sidecar for %s: %w", owner, err)
	}
	defer store.Close()
	metadata, err := readMetadata(store, "owner", "model", "dimensions", "contract", "source_fingerprint")
	if err != nil || metadata["owner"] != owner || metadata["model"] != model ||
		metadata["contract"] != contract || metadata["source_fingerprint"] != sourceFingerprint {
		return Delta{}, errSidecarChanged
	}
	if dimensions, _ := strconv.Atoi(metadata["dimensions"]); dimensions == 0 {
		return Delta{}, errSidecarChanged
	}
	var chunks, sources int
	if err := store.QueryRow(`SELECT COUNT(*) FROM chunks`).Scan(&chunks); err != nil {
		return Delta{}, errSidecarChanged
	}
	if err := store.QueryRow(`SELECT COUNT(*) FROM (SELECT source_kind,source_id FROM chunks GROUP BY source_kind,source_id)`).Scan(&sources); err != nil {
		return Delta{}, errSidecarChanged
	}
	return Delta{Unchanged: chunks, Chunks: chunks, Sources: sources}, nil
}

func readMetadata(db *sql.DB, keys ...string) (map[string]string, error) {
	result := make(map[string]string, len(keys))
	for _, key := range keys {
		var value string
		if err := db.QueryRow(`SELECT value FROM meta WHERE key=?`, key).Scan(&value); err != nil {
			return nil, err
		}
		result[key] = value
	}
	return result, nil
}

func (d vectorDatabase) contractFingerprint() string {
	fields := []string{declaredReaderVersion, d.Plugin, d.Database, d.Alias}
	for _, table := range d.Tables {
		fields = append(fields, table.contractFingerprint())
	}
	return incrementality.ContentFingerprint(fields...)
}

func (t vectorTable) contractFingerprint() string {
	fields := []string{declaredReaderVersion, t.Name, t.IDColumn}
	fields = append(fields, t.TextColumns...)
	size, overlap := t.chunking()
	fields = append(fields, strconv.Itoa(size), strconv.Itoa(overlap))
	return incrementality.ContentFingerprint(fields...)
}

func (t vectorTable) chunking() (int, int) {
	size, overlap := defaultChunkSize, defaultOverlap
	if t.Chunking == nil {
		return size, overlap
	}
	if t.Chunking.MaxChars != nil {
		size = *t.Chunking.MaxChars
	}
	if t.Chunking.OverlapChars != nil {
		overlap = *t.Chunking.OverlapChars
	}
	if overlap >= size {
		overlap = 0
	}
	return size, overlap
}

// -/ 4/7

// -- 5/7 HELPER · Sidecar ownership and metadata sealing --

func SidecarPath(databasePath string) string {
	extension := filepath.Ext(databasePath)
	return strings.TrimSuffix(databasePath, extension) + ".vector" + extension
}

func sealSidecar(path, owner, model, buildVersion, contract, sourceFingerprint string) error {
	store, err := openSQLite(path, false)
	if err != nil {
		return fmt.Errorf("open vector sidecar for %s: %w", owner, err)
	}
	defer store.Close()
	if err := ensureBaseSchema(store); err != nil {
		return err
	}
	var knownOwner string
	err = store.QueryRow(`SELECT value FROM meta WHERE key='owner'`).Scan(&knownOwner)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("read vector sidecar owner: %w", err)
	}
	if knownOwner != "" && knownOwner != owner {
		return fmt.Errorf("vector sidecar owner is %s, want %s", knownOwner, owner)
	}
	values := map[string]string{
		"owner": owner, "model": model, "version": buildVersion, "contract": contract,
	}
	if sourceFingerprint != "" {
		values["source_fingerprint"] = sourceFingerprint
	}
	tx, err := store.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for key, value := range values {
		if _, err := tx.Exec(`INSERT OR REPLACE INTO meta(key,value) VALUES (?,?)`, key, value); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func assertSidecarOwner(path, owner string) error {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return fmt.Errorf("inspect vector sidecar for %s: %w", owner, err)
	}
	store, err := openSQLite(path, true)
	if err != nil {
		return fmt.Errorf("inspect vector sidecar for %s: %w", owner, err)
	}
	defer store.Close()
	var knownOwner string
	err = store.QueryRow(`SELECT value FROM meta WHERE key='owner'`).Scan(&knownOwner)
	if errors.Is(err, sql.ErrNoRows) {
		var schema string
		if schemaErr := store.QueryRow(`SELECT value FROM meta WHERE key='schema'`).Scan(&schema); schemaErr != nil || schema != vectorStorageSchema {
			return fmt.Errorf("existing sidecar for %s is not an owned or interrupted vector index", owner)
		}
		if _, schemaErr := store.Exec(`SELECT id,source_kind,source_id,chunk_index,fingerprint,locator,updated_at FROM chunks LIMIT 0`); schemaErr != nil {
			return fmt.Errorf("existing sidecar for %s is not an owned or interrupted vector index", owner)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("read vector sidecar owner: %w", err)
	}
	if knownOwner != owner {
		return fmt.Errorf("vector sidecar owner is %s, want %s", knownOwner, owner)
	}
	return nil
}

func (d vectorDatabase) owner() string { return d.Plugin + "/" + d.Database }

func (f Federation) databasePath(database vectorDatabase) string {
	return filepath.Join(f.PluginRoot, database.Plugin, database.Path)
}

// -/ 5/7

// -- 6/7 HELPER · Corpus compatibility and federated compaction --

func (f Federation) CorpusIndex() (Index, error) {
	for _, database := range f.databases {
		if database.Plugin == "roca-corpus" && database.Database == "corpus" {
			path := SidecarPath(f.databasePath(database))
			model := f.Model
			if model == "" {
				model = ConfiguredModel(path)
			}
			copy := f
			copy.Model = model
			return copy.index(database, DeclaredCorpus{Core: f.Core, Database: database}, path), nil
		}
	}
	return Index{}, fmt.Errorf("the vector registry has no corpus declaration")
}

func (f Federation) ConfiguredModel() string {
	for _, database := range f.databases {
		if database.Plugin == "roca-corpus" && database.Database == "corpus" {
			return ConfiguredModel(SidecarPath(f.databasePath(database)))
		}
	}
	return DefaultModel
}

type CompactFederationReport struct {
	Databases      int   `json:"databases"`
	PagesBefore    int64 `json:"pages_before"`
	PagesAfter     int64 `json:"pages_after"`
	BytesBefore    int64 `json:"bytes_before"`
	BytesAfter     int64 `json:"bytes_after"`
	BytesReclaimed int64 `json:"bytes_reclaimed"`
	LiveChunks     int64 `json:"live_chunks"`
}

func (f Federation) Compact(ctx context.Context) (CompactFederationReport, error) {
	report := CompactFederationReport{}
	for _, database := range f.databases {
		path := SidecarPath(f.databasePath(database))
		if _, err := os.Stat(path); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return report, err
		}
		item, err := Compact(ctx, path)
		if err != nil {
			return report, fmt.Errorf("compact vector sidecar %s: %w", database.owner(), err)
		}
		report.Databases++
		report.PagesBefore += item.PagesBefore
		report.PagesAfter += item.PagesAfter
		report.BytesBefore += item.BytesBefore
		report.BytesAfter += item.BytesAfter
		report.BytesReclaimed += item.BytesReclaimed
		report.LiveChunks += item.LiveChunks
	}
	if report.Databases == 0 {
		return report, fmt.Errorf("vector search is not initialized; run `roca vector install`")
	}
	return report, nil
}

func (f Federation) HasSidecars() bool {
	for _, database := range f.databases {
		if _, err := os.Stat(SidecarPath(f.databasePath(database))); err == nil {
			return true
		}
	}
	return false
}

func (f Federation) RemoveLegacyMonolith(stateDir string) error {
	legacy := filepath.Join(stateDir, DatabaseFilename)
	for _, suffix := range []string{"", "-wal", "-shm", "-journal", ".index.lock"} {
		if err := os.Remove(legacy + suffix); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove superseded central vector index: %w", err)
		}
	}
	return nil
}

type FederatedWorker struct {
	Federation  Federation
	DataDir     string
	PullModel   bool
	Notifier    Notifier
	WaitForCalm func(context.Context) error
}

func (w FederatedWorker) Run(ctx context.Context) Completion {
	started := time.Now().UTC()
	completion := Completion{ExitStatus: 0, Model: w.Federation.Model, StartedAt: started}
	failIf := func(err error) {
		if err != nil && completion.Error == "" {
			completion.ExitStatus, completion.Error = 1, err.Error()
		}
	}
	if w.PullModel {
		failIf(w.Federation.Embedder.Pull(ctx, w.Federation.Model))
	}
	if completion.Error == "" && w.WaitForCalm != nil {
		failIf(w.WaitForCalm(ctx))
	}
	if completion.Error == "" {
		delta, err := w.Federation.Ingest(ctx, "")
		completion.Delta = delta.Delta
		failIf(err)
	}
	if completion.Error == "" {
		failIf(w.Federation.RemoveLegacyMonolith(w.DataDir))
	}
	completion.FinishedAt = time.Now().UTC()
	if err := writeCompletion(w.DataDir, completion); err != nil {
		failIf(err)
	}
	if w.Notifier != nil {
		_ = w.Notifier.Notify(ctx, completion)
	}
	return completion
}

// -/ 6/7

// -- 7/7 HELPER · Deterministic identifier validation --

func validIdentifier(value string) bool { return sqlIdentifier.MatchString(value) }

// -/ 7/7
