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
	"time"

	"github.com/thellmwhisperer/la-roca/internal/distribution/rocacorpus"
	"github.com/thellmwhisperer/la-roca/internal/distribution/rocaops"
	"github.com/thellmwhisperer/la-roca/internal/provider"
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

func TestRocaOpsOffLeavesTheBundledPluginCompletelyInert(t *testing.T) {
	paths := freshPaths(t)
	plugins := ensureRocaOps(t, paths)
	model := answering("fixture", `SELECT content AS text FROM memories LIMIT 5`)
	svc := initialized(t, paths, func(options *service.Options) {
		options.PluginDir = plugins
		options.Providers = cascadeOf(model)
	})
	stored, err := svc.Store(t.Context(), service.StoreRequest{
		Layer: "handoff", Content: "synthetic flag-off marker",
	})
	if err != nil {
		t.Fatal(err)
	}
	var coreRows int
	if err := svc.DB().SQL().QueryRow("SELECT COUNT(*) FROM memories WHERE id = ?", stored.ID).Scan(&coreRows); err != nil {
		t.Fatal(err)
	}
	opsDB := openRocaOps(t, plugins)
	defer opsDB.Close()
	var opsRows int
	if err := opsDB.QueryRow("SELECT COUNT(*) FROM memories").Scan(&opsRows); err != nil {
		t.Fatal(err)
	}
	result, err := svc.Query(t.Context(), service.QueryRequest{Question: "show the synthetic flag off marker"})
	if err != nil {
		t.Fatal(err)
	}
	if coreRows != 1 || opsRows != 0 || len(result.Databases) != 0 ||
		strings.Contains(model.prompt, "plugin_roca_ops") {
		t.Fatalf("flag off changed behavior: core=%d ops=%d databases=%v prompt=%s",
			coreRows, opsRows, result.Databases, model.prompt)
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

	result, err := svc.Query(t.Context(), service.QueryRequest{
		Question: "Which receipts were recorded?", Databases: []string{"well-formed"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(result.Databases, []string{"plugin:well-formed"}) {
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

func TestAResidentPluginIsAvailableWithoutQuestionRouting(t *testing.T) {
	paths := freshPaths(t)
	plugins := filepath.Join(paths.data, "plugins")
	installQueryPlugin(t, plugins, "resident-fixture", `
version: 1
attachment: resident
description: Synthetic resident inventory.
questions:
  - "Which resident inventory exists?"
tables:
  - name: inventory
    description: Synthetic resident records.
    columns: [id, label]
`, `CREATE TABLE inventory (id INTEGER PRIMARY KEY, label TEXT);
INSERT INTO inventory (label) VALUES ('Synthetic resident marker');`)
	model := answering("codex",
		`SELECT label AS text FROM plugin_resident_fixture.inventory LIMIT 5`)
	svc := initialized(t, paths, func(options *service.Options) {
		options.PluginDir = plugins
		options.PluginsEnabled = true
		options.Providers = cascadeOf(model)
	})

	result, err := svc.Query(t.Context(), service.QueryRequest{
		Question: "show the unrelated telescope schedule", Databases: []string{"resident-fixture"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(result.Databases, []string{"plugin:resident-fixture"}) ||
		result.RowCount != 1 || result.Rows[0]["database"] != "plugin:resident-fixture" {
		t.Fatalf("resident result = %+v", result)
	}
	if !strings.Contains(model.prompt, "plugin_resident_fixture.inventory") {
		t.Fatalf("named resident schema is absent from the query prompt:\n%s", model.prompt)
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

func TestExecAddressesAPluginUnderTheSameGateAndNeverHidesACoreSource(t *testing.T) {
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

	seed(t, svc, "project", "synthetic core marker")
	for _, statement := range []string{
		`SELECT title AS text FROM plugin_well_formed.receipts UNION ALL SELECT content AS text FROM memories`,
		`SELECT r.title AS text FROM plugin_well_formed.receipts r, memories m`,
	} {
		mixed, err := svc.Exec(t.Context(), service.ExecRequest{SQL: statement})
		if err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
		for _, row := range mixed.Rows {
			if row["database"] != "core+plugin:well-formed" {
				t.Fatalf("%s: a row of mixed sources claims one of them: %+v", statement, row)
			}
		}
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
		Question: "Which internal plugin paths exist?", Databases: []string{"private-state"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(model.prompt, "plugin_private_state.ingest_file_state") {
		t.Fatalf("hidden plugin table reached the prompt:\n%s", model.prompt)
	}
	if !slices.Equal(result.Databases, []string{"plugin:private-state"}) {
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
		Question: "Which synthetic beacon records exist?", Databases: []string{"all"},
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

func TestRocaOpsRoutesStoresAndQueriesCoreHistoryTogetherWithNewWrites(t *testing.T) {
	paths := freshPaths(t)
	plugins := ensureRocaOps(t, paths)
	model := answering("codex", `
		SELECT 'core' AS "database", id, content AS text FROM memories
		WHERE content = 'synthetic historical handoff'
		UNION ALL
		SELECT 'plugin:roca-ops' AS "database", id, content AS text
		FROM plugin_roca_ops.memories
		WHERE content = 'synthetic resident handoff'
		LIMIT 10`)
	svc := initialized(t, paths, func(options *service.Options) {
		options.PluginDir = plugins
		options.RocaOpsEnabled = true
		options.Providers = cascadeOf(model)
	})
	seed(t, svc, "handoff", "synthetic historical handoff")
	// Core history can contain the same text as a new operational write. Its id
	// is from another namespace and must never be returned for the ops write.
	seed(t, svc, "handoff", "synthetic resident handoff")

	stored, err := svc.Store(t.Context(), service.StoreRequest{
		Layer: "handoff", Content: "synthetic resident handoff",
		Authorship: service.Authorship{Agent: "codex", Model: "gpt-test", Surface: service.SurfaceMCP},
	})
	if err != nil {
		t.Fatal(err)
	}
	if stored.Skipped {
		t.Fatalf("core history suppressed the extracted write: %+v", stored)
	}
	var coreRows int
	if err := svc.DB().SQL().QueryRow(
		"SELECT COUNT(*) FROM memories WHERE content = 'synthetic resident handoff'").Scan(&coreRows); err != nil {
		t.Fatal(err)
	}
	if coreRows != 1 {
		t.Fatalf("the extracted write changed core history: rows = %d, want the one seeded row", coreRows)
	}

	opsDB := openRocaOps(t, plugins)
	defer opsDB.Close()
	var agent, modelID, surface string
	if err := opsDB.QueryRow(`SELECT source_agent, source_model, source_surface
		FROM memories WHERE id = ?`, stored.ID).Scan(&agent, &modelID, &surface); err != nil {
		t.Fatal(err)
	}
	if agent != "codex" || modelID != "gpt-test" || surface != service.SurfaceMCP {
		t.Fatalf("authorship = %s/%s via %s", agent, modelID, surface)
	}

	result, err := svc.Query(t.Context(), service.QueryRequest{
		Question: "show the synthetic handoff history", Databases: []string{"core", "ops"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(result.Databases, []string{"core", "plugin:roca-ops"}) || result.RowCount != 2 {
		t.Fatalf("union result = %+v", result)
	}
	if !strings.Contains(model.prompt, "plugin_roca_ops.memories") {
		t.Fatalf("resident ops schema is absent from the query prompt:\n%s", model.prompt)
	}
}

func TestKeywordRescueSearchesRocaOpsWithoutQuestionRouting(t *testing.T) {
	paths := freshPaths(t)
	plugins := ensureRocaOps(t, paths)
	svc := initialized(t, paths, func(options *service.Options) {
		options.PluginDir = plugins
		options.RocaOpsEnabled = true
		options.Providers = cascadeOf(unavailable("fixture", "offline", "start fixture"))
	})
	if _, err := svc.Store(t.Context(), service.StoreRequest{
		Layer: "handoff", Content: "synthetic zircon falcon handoff",
	}); err != nil {
		t.Fatal(err)
	}

	result, err := svc.Query(t.Context(), service.QueryRequest{
		Question: "what happened with zircon falcon",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Path != service.PathKeyword || !slices.ContainsFunc(result.Rows, func(row map[string]any) bool {
		return row["database"] == "plugin:roca-ops" && strings.Contains(fmt.Sprint(row["text"]), "zircon falcon")
	}) {
		t.Fatalf("resident write was absent from keyword rescue: %+v", result)
	}
	if !strings.Contains(result.SQL, "plugin_roca_ops") {
		t.Fatalf("the declared SQL omits the half that produced the resident rows: %s", result.SQL)
	}
}

func TestRocaOpsDrainOnlyRemovesExplicitlyExpiredRows(t *testing.T) {
	svc, plugins := enabledRocaOps(t)
	for _, testCase := range []struct {
		content   string
		expiresAt string
	}{
		{content: "synthetic expired handoff", expiresAt: "2026-08-12T00:00:00Z"},
		{content: "synthetic future handoff", expiresAt: "2026-08-14T00:00:00Z"},
		{content: "synthetic immortal handoff"},
	} {
		metadata := map[string]any{}
		if testCase.expiresAt != "" {
			metadata["expires_at"] = testCase.expiresAt
		}
		_, err := svc.Store(t.Context(), service.StoreRequest{
			Layer: "handoff", Content: testCase.content, Metadata: metadata,
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	result, err := svc.DrainRocaOps(t.Context(), time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if result.Removed != 1 {
		t.Fatalf("drain = %+v, want one removed row", result)
	}
	opsDB := openRocaOps(t, plugins)
	defer opsDB.Close()
	var remaining int
	if err := opsDB.QueryRow("SELECT COUNT(*) FROM memories").Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 2 {
		t.Fatalf("remaining = %d, want future and immortal rows", remaining)
	}
}

func TestRocaOpsExactStoreGuardIncludesExpiry(t *testing.T) {
	svc, _ := enabledRocaOps(t)
	request := service.StoreRequest{Layer: "handoff", Content: "synthetic exact ops retry",
		Metadata: map[string]any{"expires_at": "2026-08-18T00:00:00Z"}}
	first, err := svc.Store(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := svc.Store(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !retry.Skipped || retry.ID != first.ID {
		t.Fatalf("ops retry = %+v, want canonical %d", retry, first.ID)
	}
	request.Metadata = map[string]any{"expires_at": "2026-08-19T00:00:00Z"}
	near, err := svc.Store(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if near.Skipped || near.ID == first.ID {
		t.Fatal("different ops expiry was coalesced")
	}
}

func enabledRocaOps(t *testing.T) (*service.Service, string) {
	t.Helper()
	paths := freshPaths(t)
	plugins := ensureRocaOps(t, paths)
	svc := initialized(t, paths, func(options *service.Options) {
		options.PluginDir, options.RocaOpsEnabled = plugins, true
	})
	return svc, plugins
}

func ensureRocaOps(t *testing.T, paths testPaths) string {
	t.Helper()
	root := filepath.Join(paths.data, "plugins")
	if _, err := rocaops.Ensure(root, filepath.Join(paths.data, "bin"), "v-test"); err != nil {
		t.Fatal(err)
	}
	return root
}

func openRocaOps(t *testing.T, root string) *sql.DB {
	t.Helper()
	descriptor, err := plugin.Inspect(rocaops.Name, filepath.Join(root, rocaops.Name))
	if err != nil {
		t.Fatal(err)
	}
	return openSQLite(t, descriptor.Database)
}

func openSQLite(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	return db
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
	db := openSQLite(t, filepath.Join(directory, "plugin.db"))
	defer func() {
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
	}()
	if _, err := db.Exec(ddl); err != nil {
		t.Fatalf("create synthetic plugin database: %v", err)
	}
}

func TestQueryScopesTheSQLSeatAndFailsUnknownNames(t *testing.T) {
	paths := freshPaths(t)
	plugins := ensureRocaOps(t, paths)
	if _, err := rocacorpus.Ensure(plugins, filepath.Join(paths.data, "bin"), "v-test"); err != nil {
		t.Fatal(err)
	}
	installQueryPlugin(t, plugins, "well-formed", `
version: 1
description: Synthetic purchase receipts.
questions: [Which receipts were recorded?]
tables:
  - name: receipts
    description: Synthetic receipts.
    columns: [id, title]
`, `CREATE TABLE receipts (id INTEGER PRIMARY KEY, title TEXT);
INSERT INTO receipts (title) VALUES ('Synthetic telescope parts');`)

	for _, tc := range []struct {
		name       string
		databases  []string
		sql        string
		wantDBs    []string
		wantPrompt []string
		hidePrompt []string
		wantErr    string
		widened    bool
	}{
		{
			name:       "default is corpus without ops noise",
			sql:        `SELECT 1 AS answer LIMIT 1`,
			wantDBs:    []string{"core", "plugin:roca-corpus"},
			wantPrompt: []string{"plugin_roca_corpus", "Attached databases not in this pass"},
			hidePrompt: []string{"plugin_roca_ops", "plugin_well_formed"},
		},
		{
			name:       "explicit names widen the seat",
			databases:  []string{"corpus", "ops"},
			sql:        `SELECT 1 AS answer LIMIT 1`,
			wantDBs:    []string{"plugin:roca-corpus", "plugin:roca-ops"},
			wantPrompt: []string{"plugin_roca_corpus", "plugin_roca_ops"},
			hidePrompt: []string{"plugin_well_formed"},
		},
		{
			name:      "unknown names list what is attached",
			databases: []string{"nope"},
			sql:       `SELECT 1 AS answer LIMIT 1`,
			wantErr:   "unknown database \"nope\"; attached databases:",
		},
		{
			name:    "empty scoped pass widens once",
			sql:     `SELECT 1 AS answer WHERE 0 LIMIT 1`,
			wantDBs: []string{"core", "plugin:roca-ops", "plugin:roca-corpus"},
			widened: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model := answering("codex", tc.sql)
			svc := initialized(t, paths, func(options *service.Options) {
				options.PluginDir = plugins
				options.PluginsEnabled = true
				options.RocaOpsEnabled = true
				options.CorpusEnabled = true
				options.Providers = cascadeOf(model)
			})
			result, err := svc.Query(t.Context(), service.QueryRequest{
				Question: "Which receipts were recorded?", Databases: tc.databases,
			})
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				if !strings.Contains(err.Error(), "corpus") || !strings.Contains(err.Error(), "ops") {
					t.Fatalf("error does not list attached names: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(result.Databases, tc.wantDBs) {
				t.Fatalf("databases = %v, want %v", result.Databases, tc.wantDBs)
			}
			if result.Widened != tc.widened {
				t.Fatalf("widened = %v, want %v", result.Widened, tc.widened)
			}
			for _, want := range tc.wantPrompt {
				if !strings.Contains(model.prompt, want) && !strings.Contains(strings.Join(model.prompts, "\n"), want) {
					t.Errorf("prompt lacks %q:\n%s", want, model.prompt)
				}
			}
			for _, hide := range tc.hidePrompt {
				if strings.Contains(model.prompt, hide) {
					t.Errorf("scoped prompt leaked %q:\n%s", hide, model.prompt)
				}
			}
		})
	}
}

func TestSuccessfulWideningDropsTheScopedPassAnswerState(t *testing.T) {
	paths := freshPaths(t)
	plugins := ensureRocaOps(t, paths)
	if _, err := rocacorpus.Ensure(plugins, filepath.Join(paths.data, "bin"), "v-test"); err != nil {
		t.Fatal(err)
	}
	installQueryPlugin(t, plugins, "well-formed", `
version: 1
description: Synthetic purchase receipts.
questions: ["Which receipts were recorded?"]
tables:
  - name: receipts
    description: Synthetic receipts.
    columns: [id, title]
`, `CREATE TABLE receipts (id INTEGER PRIMARY KEY, title TEXT);
INSERT INTO receipts (title) VALUES ('Synthetic telescope parts');`)

	const first = "DELETE FROM memories"
	const empty = "SELECT 1 AS answer WHERE 0 LIMIT 1"
	const widened = "SELECT title AS text FROM plugin_well_formed.receipts LIMIT 1"
	model := &fakeProvider{
		name: "codex", model: "codex-model", ready: provider.Readiness{Ready: true},
		latency: 7, answers: []string{first, empty, widened},
	}
	svc := initialized(t, paths, func(options *service.Options) {
		options.PluginDir = plugins
		options.PluginsEnabled = true
		options.RocaOpsEnabled = true
		options.CorpusEnabled = true
		options.Providers = cascadeOf(model)
	})
	result, err := svc.Query(t.Context(), service.QueryRequest{Question: "Which receipts were recorded?"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Path != service.PathLLM || result.Match != service.MatchFound || result.RowCount != 1 ||
		result.Message != "" || result.Degraded != "" {
		t.Fatalf("widened result retained scoped answer state: %+v", result)
	}
	if !result.Widened || result.ModelSQL != widened || !result.RetriedSQL ||
		result.FirstModelSQL != first || result.LLMLatencyMS != 21 || len(result.Providers) != 2 {
		t.Fatalf("widened result lost cumulative state: %+v", result)
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
