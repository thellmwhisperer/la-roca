package service_test

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/provider/plugin"
	"github.com/thellmwhisperer/la-roca/internal/provider/service"
	_ "modernc.org/sqlite"
)

func TestPluginsOffMakesAnInstalledDirectoryObservableNowhere(t *testing.T) {
	paths := freshPaths(t)
	plugins := filepath.Join(paths.data, "plugins")
	installQueryPlugin(t, plugins, "well-formed", `
version: 1
description: Synthetic purchase receipts.
questions: [Which receipts were recorded?]
tables:
  - name: receipts
    description: Synthetic receipts.
    columns: [id, title]
`, `CREATE TABLE receipts (id INTEGER PRIMARY KEY, title TEXT NOT NULL);
INSERT INTO receipts (title) VALUES ('synthetic hidden-by-flag marker')`)

	model := answering("fixture", `SELECT 1 AS answer LIMIT 1`)
	svc := initialized(t, paths, func(options *service.Options) {
		options.PluginDir = plugins
		options.PluginsEnabled = false
		options.Providers = cascadeOf(model)
	})
	result, err := svc.Query(t.Context(), service.QueryRequest{Question: "Which receipts were recorded?"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Databases) != 0 || len(result.Warnings) != 0 ||
		strings.Contains(model.prompt, "plugin_well_formed") || strings.Contains(model.prompt, "purchase receipts") {
		t.Fatalf("disabled plugin changed query: databases=%v warnings=%v prompt=%s",
			result.Databases, result.Warnings, model.prompt)
	}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"databases"`) || strings.Contains(string(raw), `"database"`) {
		t.Fatalf("disabled plugin added an envelope field: %s", raw)
	}
	executed, err := svc.Exec(t.Context(), service.ExecRequest{SQL: "SELECT 1 AS answer LIMIT 1"})
	if err != nil {
		t.Fatal(err)
	}
	raw, err = json.Marshal(executed)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"databases"`) {
		t.Fatalf("disabled plugin changed exec envelope: %s", raw)
	}
}

func TestARelevantPluginIsQualifiedValidatedAndMarkedInEveryResultRow(t *testing.T) {
	paths := freshPaths(t)
	plugins := filepath.Join(paths.data, "plugins")
	installQueryPlugin(t, plugins, "well-formed", `
version: 1
description: Synthetic purchase receipts and their totals.
questions:
  - "Which receipts were recorded?"
tables:
  - name: receipts
    description: One row per synthetic receipt.
    columns: [id, title, amount_cents]
`, `CREATE TABLE receipts (id INTEGER PRIMARY KEY, title TEXT, amount_cents INTEGER);
INSERT INTO receipts (title, amount_cents) VALUES ('Synthetic telescope parts', 4200);`)
	svc, model := pluginModelService(t, paths, plugins,
		`SELECT 'core' AS "database", title AS text FROM plugin_well_formed.receipts LIMIT 5`)

	result, err := svc.Query(t.Context(), service.QueryRequest{Question: "Which receipts were recorded?"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(result.Databases, []string{"core", "plugin:well-formed"}) {
		t.Fatalf("databases = %v", result.Databases)
	}
	if result.RowCount != 1 || result.Rows[0]["database"] != "plugin:well-formed" {
		t.Fatalf("result = %+v", result)
	}
	for _, want := range []string{"plugin_well_formed.receipts", "Synthetic purchase receipts", "database"} {
		if !strings.Contains(model.prompt, want) {
			t.Errorf("plugin prompt lacks %q:\n%s", want, model.prompt)
		}
	}
}

func TestALyingSemanticLayerDegradesWithAWarningAndIsNotQueryable(t *testing.T) {
	paths := freshPaths(t)
	plugins := filepath.Join(paths.data, "plugins")
	installQueryPlugin(t, plugins, "lying", `
version: 1
description: Synthetic outstanding invoices.
questions:
  - "Which synthetic invoices are outstanding?"
tables:
  - name: invoices
    description: One row per synthetic invoice.
    columns: [id, label, outstanding_cents]
`, `CREATE TABLE invoices (id INTEGER PRIMARY KEY, label TEXT, paid_cents INTEGER);`)
	svc, model := pluginModelService(t, paths, plugins,
		`SELECT content AS text FROM memories LIMIT 1`)
	seed(t, svc, "project", "synthetic core fallback")

	result, err := svc.Query(t.Context(), service.QueryRequest{
		Question: "Which synthetic invoices are outstanding?",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(result.Databases, []string{"core"}) {
		t.Fatalf("databases = %v", result.Databases)
	}
	if len(result.Warnings) != 1 || !strings.Contains(result.Warnings[0], "outstanding_cents") {
		t.Fatalf("warnings = %v", result.Warnings)
	}
	if strings.Contains(model.prompt, "plugin_lying") {
		t.Fatalf("the lying schema reached the model prompt:\n%s", model.prompt)
	}
}

func TestExecCanAddressAnInstalledPluginUnderTheSameGate(t *testing.T) {
	paths := freshPaths(t)
	plugins := filepath.Join(paths.data, "plugins")
	installQueryPlugin(t, plugins, "well-formed", `
version: 1
description: Synthetic purchase receipts.
questions:
  - "Which receipts were recorded?"
tables:
  - name: receipts
    description: One row per synthetic receipt.
    columns: [id, title]
`, `CREATE TABLE receipts (id INTEGER PRIMARY KEY, title TEXT);
INSERT INTO receipts (title) VALUES ('Synthetic observatory pass');`)
	svc := initialized(t, paths, func(options *service.Options) {
		options.PluginDir = plugins
		options.PluginsEnabled = true
	})

	result, err := svc.Exec(t.Context(), service.ExecRequest{
		SQL: `SELECT title AS text FROM plugin_well_formed.receipts LIMIT 2`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(result.Databases, []string{"core", "plugin:well-formed"}) ||
		result.Rows[0]["database"] != "plugin:well-formed" {
		t.Fatalf("result = %+v", result)
	}
	if _, err := svc.Exec(t.Context(), service.ExecRequest{
		SQL: `SELECT missing FROM plugin_well_formed.receipts LIMIT 2`,
	}); err == nil {
		t.Fatal("the plugin escaped column validation")
	}
}

func TestAPluginCannotMakeACoreHiddenTableVisible(t *testing.T) {
	paths := freshPaths(t)
	plugins := filepath.Join(paths.data, "plugins")
	installQueryPlugin(t, plugins, "private-state", `
version: 1
description: Synthetic internal plugin state.
questions:
  - "Which internal plugin paths exist?"
tables:
  - name: ingest_file_state
    description: Internal plugin bookkeeping, never queryable.
    columns: [path]
`, `CREATE TABLE ingest_file_state (path TEXT);`)
	svc, model := pluginModelService(t, paths, plugins, `SELECT 1 AS answer LIMIT 1`)
	result, err := svc.Query(t.Context(), service.QueryRequest{
		Question: "Which internal plugin paths exist?",
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(model.prompt, "plugin_private_state.ingest_file_state") {
		t.Fatalf("hidden plugin table reached the prompt:\n%s", model.prompt)
	}
	if !slices.Equal(result.Databases, []string{"core", "plugin:private-state"}) {
		t.Fatalf("databases = %v", result.Databases)
	}
}

func TestTheAttachmentLimitKeepsTheMostRelevantAndDeclaresTheOmission(t *testing.T) {
	paths := freshPaths(t)
	plugins := filepath.Join(paths.data, "plugins")
	for index := 0; index < plugin.MaxAttached+1; index++ {
		name := fmt.Sprintf("candidate-%02d", index)
		installQueryPlugin(t, plugins, name, fmt.Sprintf(`
version: 1
description: Synthetic beacon records number %d.
questions:
  - "Which synthetic beacon records exist?"
tables:
  - name: records
    description: Synthetic beacon records.
    columns: [id, body]
`, index), `CREATE TABLE records (id INTEGER PRIMARY KEY, body TEXT);`)
	}
	svc, _ := pluginModelService(t, paths, plugins, `SELECT 1 AS answer LIMIT 1`)

	result, err := svc.Query(t.Context(), service.QueryRequest{
		Question: "Which synthetic beacon records exist?",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Databases) != plugin.MaxAttached+1 || len(result.OmittedDatabases) != 1 {
		t.Fatalf("consulted = %v, omitted = %v", result.Databases, result.OmittedDatabases)
	}
	if !slices.ContainsFunc(result.Warnings, func(warning string) bool {
		return strings.Contains(warning, "attachment limit")
	}) {
		t.Fatalf("warnings = %v", result.Warnings)
	}
}

func installQueryPlugin(t *testing.T, root, name, semantic, ddl string) {
	t.Helper()
	directory := filepath.Join(root, name)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, plugin.SemanticFilename), []byte(semantic), 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(directory, "plugin.db"))
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

func pluginModelService(t *testing.T, paths testPaths, plugins, statement string) (*service.Service, *fakeProvider) {
	t.Helper()
	model := answering("codex", statement)
	svc := initialized(t, paths, func(options *service.Options) {
		options.PluginDir = plugins
		options.PluginsEnabled = true
		options.Providers = cascadeOf(model)
	})
	return svc, model
}
