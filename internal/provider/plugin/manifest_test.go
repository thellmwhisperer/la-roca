package plugin_test

import (
	"database/sql"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/provider/plugin"
	"github.com/thellmwhisperer/la-roca/internal/provider/query"
	_ "modernc.org/sqlite"
)

func TestManifestValidationRejectsMalformedDeclarationsActionably(t *testing.T) {
	valid := manifestFixture(`{
  "schema": 1,
  "name": "synthetic",
  "version": "1.0.0",
  "binary": "roca-proxy0",
  "databases": [{
    "name": "records",
    "path": "index0.db",
    "alias": "plugin_synthetic_records",
    "attachment": "resident",
    "retention": "The plugin retains every synthetic record."
  }],
  "semantic": {"databases": [{
    "database": "records",
    "description": "Synthetic records.",
    "questions": ["Which synthetic records exist?"],
    "tables": [{"name": "records", "description": "One synthetic record.", "columns": ["id", "value"]}]
  }]},
  "verbs": [{"name": "inspect", "description": "Inspect synthetic records.", "capability": "inspect"}],
  "capabilities": [{"name": "inspect", "command": ["inspect"]}]
}`)
	if _, err := plugin.DecodeManifest(strings.NewReader(valid)); err != nil {
		t.Fatalf("valid manifest with x and 0 in filenames: %v", err)
	}

	tests := []struct {
		name string
		edit func(string) string
		want string
	}{
		{"unknown field", func(raw string) string { return strings.Replace(raw, `"binary":`, `"mystery": true, "binary":`, 1) }, "unknown field"},
		{"unsafe alias", func(raw string) string {
			return strings.Replace(raw, "plugin_synthetic_records", "plugin-synthetic-records", 1)
		}, "alias"},
		{"semantic database missing", func(raw string) string {
			return strings.Replace(raw, `"database": "records"`, `"database": "missing"`, 1)
		}, "missing"},
		{"verb capability missing", func(raw string) string {
			return strings.Replace(raw, `"capability": "inspect"`, `"capability": "absent"`, 1)
		}, "absent"},
		{"capability command missing", func(raw string) string {
			return strings.Replace(raw, `"command": ["inspect"]`, `"command": []`, 1)
		}, "has no command"},
		{"nul in filename", func(raw string) string {
			return strings.Replace(raw, "roca-proxy0", `roca-\u0000proxy`, 1)
		}, "invalid binary"},
		{"manifest semantic source", func(raw string) string {
			return strings.Replace(raw, `"tables": [{"name": "records"`, `"tables": [{"name": "bad-name"`, 1)
		}, "plugin.json has an invalid or repeated table"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), plugin.PackageFilename)
			if err := os.WriteFile(path, []byte(test.edit(valid)), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := plugin.ReadManifest(path); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("malformed manifest error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestVectorManifestValidationKeepsEmbeddabilityExplicitAndActionable(t *testing.T) {
	valid := manifestFixture(`{
  "schema": 1,
  "name": "synthetic",
  "version": "1.0.0",
  "binary": "roca-synthetic",
  "databases": [{
    "name": "records",
    "path": "records.db",
    "alias": "plugin_synthetic_records",
    "attachment": "resident",
    "retention": "The plugin retains every synthetic record."
  }],
  "semantic": {"databases": [{
    "database": "records",
    "description": "Synthetic records.",
    "questions": ["Which synthetic records exist?"],
    "tables": [{"name": "records", "description": "One synthetic record.", "columns": ["id", "title", "body", "telemetry"]}]
  }]},
  "vector": {"databases": [{
    "database": "records",
    "tables": [{
      "name": "records",
      "id_column": "id",
      "text_columns": ["title", "body"],
      "chunking": {"max_chars": 4000, "overlap_chars": 400}
    }]
  }]},
  "verbs": [],
  "capabilities": []
}`)
	manifest, err := plugin.DecodeManifest(strings.NewReader(valid))
	if err != nil {
		t.Fatal(err)
	}
	table := manifest.Vector.Databases[0].Tables[0]
	if !slices.Equal(table.TextColumns, []string{"title", "body"}) ||
		table.Chunking == nil || *table.Chunking.MaxChars != 4000 || *table.Chunking.OverlapChars != 400 {
		t.Fatalf("vector table = %+v", table)
	}
	collision := manifest
	collision.Databases = append(slices.Clone(manifest.Databases), plugin.DatabaseDeclaration{
		Name: "existing_sidecar", Path: "records.vector.db", Alias: "plugin_synthetic_existing_sidecar",
		Attachment: plugin.AttachmentResident, Retention: "The plugin retains the non-vectorized database.",
	})
	collision.Semantic.Databases = append(slices.Clone(manifest.Semantic.Databases), plugin.DatabaseSemantic{
		Database: "existing_sidecar", Description: "Non-vectorized records.",
		Questions: []string{"Which non-vectorized records exist?"},
		Tables:    []plugin.SemanticTable{{Name: "entries", Description: "One entry.", Columns: []string{"id"}}},
	})
	if err := collision.Valid(); err == nil || !strings.Contains(err.Error(), "collides") {
		t.Fatalf("manifest sidecar collision passed with %v", err)
	}

	tests := []struct {
		name string
		edit func(string) string
		want string
	}{
		{"empty fragment", func(raw string) string {
			return replaceVectorFragment(raw, `"vector": {"databases": []},`)
		}, "vector fragment declares no databases"},
		{"undeclared database", func(raw string) string {
			return replaceLast(raw, "\"database\": \"records\",\n    \"tables\": [{", "\"database\": \"missing\",\n    \"tables\": [{")
		}, "has no database declaration"},
		{"missing semantic table", func(raw string) string {
			return replaceLast(raw, "\"name\": \"records\",\n      \"id_column\"", "\"name\": \"missing\",\n      \"id_column\"")
		}, "has no semantic table declaration"},
		{"missing id column", func(raw string) string {
			return strings.Replace(raw, `"id_column": "id"`, `"id_column": "missing"`, 1)
		}, "missing id column"},
		{"no text columns", func(raw string) string {
			return strings.Replace(raw, `"text_columns": ["title", "body"]`, `"text_columns": []`, 1)
		}, "declares no text columns"},
		{"missing text column", func(raw string) string {
			return strings.Replace(raw, `"text_columns": ["title", "body"]`, `"text_columns": ["missing"]`, 1)
		}, "missing, or repeated text column"},
		{"repeated text column", func(raw string) string {
			return strings.Replace(raw, `"text_columns": ["title", "body"]`, `"text_columns": ["title", "title"]`, 1)
		}, "missing, or repeated text column"},
		{"empty chunking", func(raw string) string {
			return strings.Replace(raw, `"chunking": {"max_chars": 4000, "overlap_chars": 400}`, `"chunking": {}`, 1)
		}, "empty chunking hints"},
		{"non-positive max", func(raw string) string {
			return strings.Replace(raw, `"max_chars": 4000`, `"max_chars": 0`, 1)
		}, "max_chars must be positive"},
		{"negative overlap", func(raw string) string {
			return strings.Replace(raw, `"overlap_chars": 400`, `"overlap_chars": -1`, 1)
		}, "overlap_chars must not be negative"},
		{"overlap reaches max", func(raw string) string {
			return strings.Replace(raw, `"overlap_chars": 400`, `"overlap_chars": 4000`, 1)
		}, "overlap_chars must be smaller"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := plugin.DecodeManifest(strings.NewReader(test.edit(valid)))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("vector manifest error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestManifestEngineDiscoversAttachesComposesAndRegisters(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "synthetic")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := manifestFixture(`{
  "schema": 1,
  "name": "synthetic",
  "version": "1.0.0",
  "binary": "roca-synthetic",
  "databases": [
    {"name": "records", "path": "records.db", "alias": "synthetic_records", "attachment": "resident", "retention": "Keep the complete record archive."},
    {"name": "runs", "path": "runs.db", "alias": "synthetic_runs", "attachment": "resident", "retention": "Keep failures; prune successful runs after 30 days."}
  ],
  "semantic": {"databases": [
    {"database": "records", "description": "Synthetic records.", "questions": ["Which synthetic records exist?"], "tables": [
      {"name": "records", "description": "One synthetic record.", "columns": ["id", "value"]}
    ]},
    {"database": "runs", "description": "Synthetic run history and errors.", "questions": ["Which synthetic runs failed?"], "tables": [
      {"name": "runs", "description": "One synthetic run.", "columns": ["id", "error"]}
    ]}
  ]},
  "vector": {"databases": [{"database": "records", "tables": [{
    "name": "records", "id_column": "id", "text_columns": ["value"],
    "chunking": {"max_chars": 2400, "overlap_chars": 240}
  }]}]},
  "verbs": [{"name": "inspect", "description": "Inspect synthetic records.", "capability": "inspect"}],
  "capabilities": [{"name": "inspect", "command": ["inspect"]}]
}`)
	if err := os.WriteFile(filepath.Join(directory, plugin.PackageFilename), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	createManifestDatabase(t, filepath.Join(directory, "records.db"),
		`CREATE TABLE records (id INTEGER PRIMARY KEY, value TEXT); INSERT INTO records (value) VALUES ('cobalt');`)
	createManifestDatabase(t, filepath.Join(directory, "runs.db"),
		`CREATE TABLE runs (id INTEGER PRIMARY KEY, error TEXT); INSERT INTO runs (error) VALUES ('synthetic failure');`)

	descriptors, warnings := plugin.Discover(root)
	if len(warnings) != 0 || len(descriptors) != 2 {
		t.Fatalf("discovery = %+v, warnings = %v", descriptors, warnings)
	}
	var databases []plugin.Database
	for _, descriptor := range descriptors {
		database, err := plugin.Validate(t.Context(), descriptor)
		if err != nil {
			t.Fatal(err)
		}
		databases = append(databases, database)
	}
	if got := []string{databases[0].Schema, databases[1].Schema}; !slices.Equal(got, []string{"synthetic_records", "synthetic_runs"}) {
		t.Fatalf("aliases = %v", got)
	}
	if len(databases[0].VectorTables) != 1 || databases[0].VectorTables[0].IDColumn != "id" ||
		!slices.Equal(databases[0].VectorTables[0].TextColumns, []string{"value"}) ||
		len(databases[1].VectorTables) != 0 {
		t.Fatalf("vector surfaces = %+v, %+v", databases[0].VectorTables, databases[1].VectorTables)
	}

	registry := plugin.ComposeVectorRegistry(databases)
	if len(registry.Databases) != 1 || registry.Databases[0].Plugin != "synthetic" ||
		registry.Databases[0].Database != "records" || registry.Databases[0].Path != "records.db" ||
		registry.Databases[0].Alias != "synthetic_records" {
		t.Fatalf("vector registry = %+v", registry)
	}
	registryPath := plugin.VectorRegistryPath(root)
	if err := plugin.SaveVectorRegistry(registryPath, registry); err != nil {
		t.Fatal(err)
	}
	loadedRegistry, err := plugin.LoadVectorRegistry(registryPath)
	if err != nil || len(loadedRegistry.Databases) != 1 ||
		!slices.Equal(loadedRegistry.Databases[0].Tables[0].TextColumns, []string{"value"}) {
		t.Fatalf("loaded vector registry = %+v, err = %v", loadedRegistry, err)
	}
	invalidRegistry := registry
	invalidRegistry.Databases = slices.Clone(registry.Databases)
	invalidRegistry.Databases[0].Path = "/outside.db"
	if err := plugin.SaveVectorRegistry(registryPath, invalidRegistry); err == nil ||
		!strings.Contains(err.Error(), "invalid path") {
		t.Fatalf("unsafe vector registry error = %v", err)
	}

	hub, err := plugin.OpenHub(t.Context(), databases)
	if err != nil {
		t.Fatal(err)
	}
	defer hub.Close()
	var value, failure string
	if err := hub.QueryRowContext(t.Context(), `SELECT value, error FROM synthetic_records.records CROSS JOIN synthetic_runs.runs`).Scan(&value, &failure); err != nil {
		t.Fatal(err)
	}
	if value != "cobalt" || failure != "synthetic failure" {
		t.Fatalf("hub row = %q, %q", value, failure)
	}
	if _, err := hub.ExecContext(t.Context(), `INSERT INTO synthetic_records.records (value) VALUES ('forbidden')`); err == nil {
		t.Fatal("the in-memory hub wrote through a read-only attachment")
	}

	composed := plugin.Compose(query.Schema{}, databases)
	if len(composed.Tables) != 2 || composed.Tables[0].Name != "synthetic_records.records" ||
		composed.Tables[1].Name != "synthetic_runs.runs" {
		t.Fatalf("composed semantic layer = %+v", composed.Tables)
	}

	loaded, err := plugin.ReadManifest(filepath.Join(directory, plugin.PackageFilename))
	if err != nil {
		t.Fatal(err)
	}
	registrations, err := plugin.Register(loaded)
	if err != nil {
		t.Fatal(err)
	}
	if len(registrations) != 1 || registrations[0].CLI != "inspect" ||
		registrations[0].MCP != "roca_inspect" || registrations[0].Binary != "roca-synthetic" ||
		!slices.Equal(registrations[0].Command, []string{"inspect"}) {
		t.Fatalf("verb registrations = %+v", registrations)
	}
}

func TestManifestAliasCollisionsAreReportedInsteadOfSilentlyRewritten(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"alpha", "beta"} {
		directory := filepath.Join(root, name)
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		raw := strings.ReplaceAll(manifestFixture(`{
  "schema": 1, "name": "NAME", "version": "1", "binary": "roca-NAME",
  "databases": [{"name": "data", "path": "data.db", "alias": "shared_alias", "attachment": "on-demand", "retention": "Plugin managed."}],
  "semantic": {"databases": [{"database": "data", "description": "Synthetic data.", "questions": ["Which synthetic data exists?"], "tables": [{"name": "data", "description": "Synthetic data.", "columns": ["id"]}]}]},
  "verbs": [], "capabilities": []
}`), "NAME", name)
		if err := os.WriteFile(filepath.Join(directory, plugin.PackageFilename), []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
		createManifestDatabase(t, filepath.Join(directory, "data.db"), `CREATE TABLE data (id INTEGER PRIMARY KEY)`)
	}
	found, warnings := plugin.Discover(root)
	if len(found) != 0 || len(warnings) != 1 || !strings.Contains(warnings[0], "shared_alias") ||
		!strings.Contains(warnings[0], "alpha, beta") {
		t.Fatalf("colliding discovery = %+v, warnings = %v", found, warnings)
	}

	// Only the bundled installer records that source, and the alias such a
	// package declared is the kernel's own seat: a third-party claim on it may
	// not take the bundled package out of the catalogue too.
	if err := os.WriteFile(filepath.Join(root, "alpha", plugin.ManifestFilename),
		[]byte(`{"schema":1,"source":"`+plugin.BundledSource+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	found, warnings = plugin.Discover(root)
	if len(found) != 1 || found[0].Name != "alpha" || found[0].Schema != "shared_alias" ||
		len(warnings) != 1 || !strings.Contains(warnings[0], "beta is unavailable") {
		t.Fatalf("bundled seat discovery = %+v, warnings = %v", found, warnings)
	}
}

func TestADerivedAliasYieldsToTheDeclaredAliasItCollidesWith(t *testing.T) {
	root := t.TempDir()
	legacy := filepath.Join(root, "roca_corpus")
	if err := os.Mkdir(legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, plugin.SemanticFilename), []byte(`
version: 1
description: Synthetic legacy records.
questions: ["Which legacy records exist?"]
tables:
  - name: records
    description: Synthetic legacy records.
    columns: [id]
`), 0o600); err != nil {
		t.Fatal(err)
	}
	createManifestDatabase(t, filepath.Join(legacy, "records.db"), `CREATE TABLE records (id INTEGER PRIMARY KEY)`)

	declared := filepath.Join(root, "declared")
	if err := os.Mkdir(declared, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(declared, plugin.PackageFilename), []byte(manifestFixture(`{
  "schema": 1, "name": "declared", "version": "1", "binary": "roca-declared",
  "databases": [{"name": "data", "path": "data.db", "alias": "plugin_roca_corpus", "attachment": "resident", "retention": "Plugin managed."}],
  "semantic": {"databases": [{"database": "data", "description": "Synthetic declared data.", "questions": ["Which declared data exists?"], "tables": [{"name": "data", "description": "Synthetic declared data.", "columns": ["id"]}]}]},
  "verbs": [], "capabilities": []
}`)), 0o600); err != nil {
		t.Fatal(err)
	}
	createManifestDatabase(t, filepath.Join(declared, "data.db"), `CREATE TABLE data (id INTEGER PRIMARY KEY)`)

	found, warnings := plugin.Discover(root)
	if len(found) != 2 || len(warnings) != 0 {
		t.Fatalf("discovery = %+v, warnings = %v", found, warnings)
	}
	aliases := map[string]string{}
	for _, descriptor := range found {
		aliases[descriptor.Name] = descriptor.Schema
	}
	if aliases["declared"] != "plugin_roca_corpus" ||
		!strings.HasPrefix(aliases["roca_corpus"], "plugin_roca_corpus_") {
		t.Fatalf("resolved aliases = %v", aliases)
	}
}

func TestMalformedManifestNeverFallsBackToALegacySemanticFile(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "synthetic")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, plugin.PackageFilename),
		[]byte(`{"schema":1,"databases":[}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, plugin.SemanticFilename), []byte(`
version: 1
description: This valid legacy declaration must not hide the broken manifest.
questions: [Which records exist?]
tables:
  - name: records
    description: Synthetic records.
    columns: [id]
`), 0o600); err != nil {
		t.Fatal(err)
	}
	createManifestDatabase(t, filepath.Join(directory, "records.db"), `CREATE TABLE records (id INTEGER PRIMARY KEY)`)
	found, warnings := plugin.Discover(root)
	if len(found) != 0 || len(warnings) != 1 || !strings.Contains(warnings[0], plugin.PackageFilename) {
		t.Fatalf("malformed discovery = %+v, warnings = %v", found, warnings)
	}
}

func manifestFixture(raw string) string { return strings.TrimSpace(raw) + "\n" }

func replaceLast(raw, old, replacement string) string {
	index := strings.LastIndex(raw, old)
	if index < 0 {
		return raw
	}
	return raw[:index] + replacement + raw[index+len(old):]
}

func replaceVectorFragment(raw, replacement string) string {
	start := strings.Index(raw, `"vector":`)
	if start < 0 {
		return raw
	}
	end := strings.Index(raw[start:], "\n  \"verbs\"")
	if end < 0 {
		return raw
	}
	end += start
	return raw[:start] + replacement + raw[end:]
}

func createManifestDatabase(t *testing.T, path, ddl string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ddl); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}
