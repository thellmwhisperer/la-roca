package service

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/distribution/rocacorpus"
	"github.com/thellmwhisperer/la-roca/internal/distribution/rocaops"
	"github.com/thellmwhisperer/la-roca/internal/store"
	"github.com/thellmwhisperer/la-roca/internal/store/search"
	_ "modernc.org/sqlite"
)

func TestCutoverServesLegacyCoreReadsFromTheHubWithoutOpeningRocaDB(t *testing.T) {
	fixture := newHubFixture(t)
	seedHubCoreMemory(t, fixture.plugins, 42, "Synthetic cutover quartz marker")
	svc := openHubService(t, fixture, LayoutCutover, nil)
	result := executeHubSQL(t, svc, `SELECT id, content FROM memories LIMIT 5`)
	if result.RowCount != 1 || fmt.Sprint(result.Rows[0]["id"]) != "42" ||
		result.Rows[0]["database"] != "core" {
		t.Fatalf("hub result = %+v", result)
	}
	if _, err := os.Stat(fixture.corePath); !os.IsNotExist(err) {
		t.Fatalf("hub mode touched roca.db: %v", err)
	}
}

func TestCutoverPreservesLegacyFTSIdentityAndRankShape(t *testing.T) {
	fixture := newHubFixture(t)
	seedHubCoreMemory(t, fixture.plugins, 101, "Quartz quartz synthetic observatory")
	svc := openHubService(t, fixture, LayoutCutover, nil)
	if err := svc.ensureHubSearchViews(t.Context()); err != nil {
		t.Fatal(err)
	}
	result := executeHubSQL(t, svc, hubFTSStatement)
	if result.RowCount != 1 || fmt.Sprint(result.Rows[0]["id"]) != "101" ||
		result.Rows[0]["rank"] == nil {
		t.Fatalf("hub FTS result = %+v", result)
	}
}

func TestCutoverPreservesSessionSourceSurface(t *testing.T) {
	fixture := newHubFixture(t)
	seedHubCoreMemory(t, fixture.plugins, 109, "Synthetic session surface marker")
	seedHubCoreSession(t, fixture.plugins, "synthetic-opencode", "opencode", "opencode-cli")
	result := executeHubSQL(t, openHubService(t, fixture, LayoutCutover, nil),
		`SELECT source_agent, source_surface FROM sessions WHERE session_id = 'synthetic-opencode'`)
	if result.RowCount != 1 || result.Rows[0]["source_agent"] != "opencode" ||
		result.Rows[0]["source_surface"] != "opencode-cli" {
		t.Fatalf("hub session provenance = %+v", result.Rows)
	}
}

func TestCutoverWritersRemainAuthoritativeInPlugins(t *testing.T) {
	fixture := newHubFixture(t)
	seedHubCoreMemory(t, fixture.plugins, 7, "Synthetic historical marker")
	svc := openHubService(t, fixture, LayoutCutover, nil)

	stored, err := svc.Store(t.Context(), StoreRequest{
		Layer:      "handoff",
		Content:    "Synthetic post-cutover write\nbranch: fixture\ndone: recorded\nstate: stored\nnext: continue\n",
		Authorship: Authorship{Agent: "claude", Model: "sonnet", Surface: SurfaceCLI},
	})
	if err != nil {
		t.Fatal(err)
	}
	ops := openSQLite(t, filepath.Join(fixture.plugins, rocaops.Name, rocaops.DatabaseFilename))
	defer ops.Close()
	var content string
	if err := ops.QueryRow(`SELECT content FROM memories WHERE id = ?`, stored.ID).Scan(&content); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(content, "Synthetic post-cutover write") {
		t.Fatalf("stored content = %q", content)
	}
}

func TestRocaOpsLayerRepairUsesTheOperationalOwner(t *testing.T) {
	options := residentTestOptions(t)
	svc := openResident(t, options)
	if _, err := svc.EnsureSchema(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ops.SQL().Exec(`INSERT INTO memories (layer, content, origin)
		VALUES ('knowledge', 'Synthetic operational drift', 'agent')`); err != nil {
		t.Fatal(err)
	}

	report, err := svc.Doctor(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	wantRemedy := "roca layers add 'knowledge' --db-path '" + options.DBPath + "'"
	if len(report.LayerRepairs) != 1 || report.LayerRepairs[0] != wantRemedy {
		t.Fatalf("layer repairs = %v, want %q", report.LayerRepairs, wantRemedy)
	}
	before, err := svc.Health(t.Context(), HealthRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if before.Checks["runtime_layers_not_in_registry"].Status != HealthFail {
		t.Fatalf("health before repair = %+v", before)
	}
	migrated, err := svc.MigrateLayer(t.Context(), "knowledge", "discovery")
	if err != nil {
		t.Fatal(err)
	}
	if migrated.Migrated != 1 {
		t.Fatalf("migration = %+v", migrated)
	}
	after, err := svc.Health(t.Context(), HealthRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if after.Checks["runtime_layers_not_in_registry"].Status != HealthPass {
		t.Fatalf("health after repair = %+v", after)
	}
	var coreRows int
	if err := svc.db.SQL().QueryRow("SELECT COUNT(*) FROM memories").Scan(&coreRows); err != nil {
		t.Fatal(err)
	}
	if coreRows != 0 {
		t.Fatalf("core memories changed = %d", coreRows)
	}
}

func TestLayerRegistryOwnerSurvivesRocaOpsActivation(t *testing.T) {
	options := residentTestOptions(t)
	options.RocaOpsEnabled = false
	legacy, err := openWithContext(t.Context(), options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.AddLayer(t.Context(), "knowledge"); err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Store(t.Context(), StoreRequest{
		Layer: "knowledge", Content: "Synthetic pre-activation write",
	}); err != nil {
		t.Fatal(err)
	}
	var copied int
	if err := legacy.db.SQL().QueryRow(
		"SELECT COUNT(*) FROM layers WHERE name = 'knowledge'").Scan(&copied); err != nil {
		t.Fatal(err)
	}
	if copied != 0 {
		t.Fatalf("custom registry rows copied into core = %d", copied)
	}
	for range 2 {
		read, err := legacy.Exec(t.Context(), ExecRequest{
			SQL: "SELECT name FROM layers WHERE name = 'knowledge'",
		})
		if err != nil {
			t.Fatal(err)
		}
		if read.RowCount != 1 || read.Rows[0]["name"] != "knowledge" {
			t.Fatalf("legacy layer registry read = %+v", read)
		}
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}

	options.RocaOpsEnabled = true
	operational, err := openWithContext(t.Context(), options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = operational.Close() })
	if _, err := operational.Store(t.Context(), StoreRequest{
		Layer: "knowledge", Content: "Synthetic post-activation write",
	}); err != nil {
		t.Fatal(err)
	}
	var registered int
	if err := operational.ops.SQL().QueryRow(
		"SELECT COUNT(*) FROM layers WHERE name = 'knowledge'").Scan(&registered); err != nil {
		t.Fatal(err)
	}
	if registered != 1 {
		t.Fatalf("stable custom registry rows = %d", registered)
	}
	report, err := operational.Health(t.Context(), HealthRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if report.Checks["runtime_layers_not_in_registry"].Status != HealthPass {
		t.Fatalf("health after activation = %+v", report)
	}
}

func TestCutoverHubLoadsTheDurableCustomLayerRegistry(t *testing.T) {
	fixture := newHubFixture(t)
	seedHubCoreMemory(t, fixture.plugins, 8, "Synthetic custom layer marker")
	svc := openHubService(t, fixture, LayoutCutover, nil)
	added, err := svc.AddLayer(t.Context(), "knowledge")
	if err != nil {
		t.Fatal(err)
	}
	if !added.Added {
		t.Fatal("custom layer was not added")
	}
	if err := svc.Close(); err != nil {
		t.Fatal(err)
	}

	reopened := openHubService(t, fixture, LayoutCutover, nil)
	var registered int
	if err := reopened.db.SQL().QueryRow(
		"SELECT COUNT(*) FROM layers WHERE name = 'knowledge'").Scan(&registered); err != nil {
		t.Fatal(err)
	}
	if registered != 1 {
		t.Fatalf("hub custom layer count = %d", registered)
	}
	if _, err := reopened.Store(t.Context(), StoreRequest{
		Layer: "knowledge", Content: "Synthetic registered cutover write",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(fixture.corePath); !os.IsNotExist(err) {
		t.Fatalf("layer repair touched roca.db: %v", err)
	}
}

func TestCutoverReopenFailureRollsBackTheMarker(t *testing.T) {
	fixture := newHubFixture(t)
	seedHubCoreMemory(t, fixture.plugins, 15, "Synthetic reopen marker")
	seedLegacyCore(t, fixture, nil)
	ops := openSQLite(t, filepath.Join(fixture.plugins, rocaops.Name, rocaops.DatabaseFilename))
	if _, err := ops.Exec(`DROP VIEW memory_compatibility`); err != nil {
		t.Fatal(err)
	}
	ops.Close()

	rolledBack := false
	svc := openHubService(t, fixture, LayoutCutover, func(options *Options) {
		options.RollbackLayout = func(reason error) error {
			rolledBack = strings.Contains(reason.Error(), "cutover reopen failed")
			return nil
		}
	})
	if !rolledBack || svc.readLayout != LayoutLegacyServing {
		t.Fatalf("serving layout = %q, rolled back = %v", svc.readLayout, rolledBack)
	}
}

func TestInitUnderCutoverReportsTheServedDatabaseWithoutWritingTheHub(t *testing.T) {
	fixture := newHubFixture(t)
	seedHubCoreMemory(t, fixture.plugins, 108, "Synthetic quartz init marker")
	svc := openHubService(t, fixture, LayoutCutover, nil)
	result, err := svc.Init(t.Context())
	if err != nil {
		t.Fatalf("Init under cutover: %v", err)
	}
	if result.Database != "adopted" || result.Verdict != string(store.VerdictCurrent) {
		t.Fatalf("init result = %+v", result)
	}
	if _, err := os.Stat(fixture.corePath); !os.IsNotExist(err) {
		t.Fatalf("init under cutover touched roca.db: %v", err)
	}
}

const hubFTSStatement = `SELECT m.id, m.content, f.rank FROM
	(SELECT rowid AS row_id, bm25(memories_fts) AS rank FROM memories_fts
	 WHERE memories_fts MATCH '"quartz"') AS f
	JOIN memories AS m ON m.id = f.row_id ORDER BY f.rank, m.id LIMIT 10`

type hubFixture struct {
	directory string
	plugins   string
	corePath  string
}

type hubMemory struct {
	id      int64
	content string
}

func newHubFixture(t *testing.T) hubFixture {
	t.Helper()
	directory := t.TempDir()
	return hubFixture{
		directory: directory,
		plugins:   filepath.Join(directory, "plugins"),
		corePath:  filepath.Join(directory, "roca.db"),
	}
}

func openHubService(t *testing.T, fixture hubFixture, layout ReadLayout, configure func(*Options)) *Service {
	t.Helper()
	options := Options{
		DBPath: fixture.corePath, BackupDir: filepath.Join(fixture.directory, "backups"),
		PluginDir: fixture.plugins, RocaOpsEnabled: true, CorpusEnabled: true, ReadLayout: layout,
	}
	if configure != nil {
		configure(&options)
	}
	svc, err := openWithContext(t.Context(), options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	return svc
}

func seedLegacyCore(t *testing.T, fixture hubFixture, seed func(*store.DB)) {
	t.Helper()
	core, err := store.Open(fixture.corePath)
	if err != nil {
		t.Fatal(err)
	}
	defer core.Close()
	if _, err := store.Adopt(t.Context(), core, filepath.Join(fixture.directory, "backups")); err != nil {
		t.Fatal(err)
	}
	if seed != nil {
		seed(core)
	}
}

func seedLegacyMemories(t *testing.T, fixture hubFixture, memories []hubMemory) {
	t.Helper()
	seedLegacyCore(t, fixture, func(core *store.DB) {
		for _, memory := range memories {
			if _, err := core.SQL().Exec(`INSERT INTO memories
				(id, layer, content, metadata, origin, status, created_at)
				VALUES (?, 'project', ?, '{}', 'agent', 'active', '2026-08-15T10:00:00Z')`,
				memory.id, memory.content); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := search.Index(t.Context(), core, nil); err != nil {
			t.Fatal(err)
		}
	})
}

func executeHubSQL(t *testing.T, svc *Service, statement string) ExecResult {
	t.Helper()
	route := svc.pluginsForSQL(t.Context(), statement)
	defer route.CloseOnDemand()
	gate, closeGate, err := svc.GateFor(route.IncludeCore, route.Databases)
	if err != nil {
		t.Fatal(err)
	}
	defer closeGate()
	validated, err := gate.Validate(statement)
	if err != nil {
		t.Fatal(err)
	}
	columns, rows, err := svc.executeWithPluginsBudget(t.Context(), validated, "", DefaultMaxChars, route.Databases, execBudget{})
	if err != nil {
		t.Fatal(err)
	}
	return ExecResult{SQL: validated, Columns: columns, Rows: rows, RowCount: len(rows)}
}

func seedHubCoreMemory(t *testing.T, plugins string, legacyID int64, content string) {
	t.Helper()
	if _, err := rocaops.Ensure(plugins, t.TempDir(), "v-test"); err != nil {
		t.Fatal(err)
	}
	if _, err := rocacorpus.Ensure(plugins, t.TempDir(), "v-test"); err != nil {
		t.Fatal(err)
	}
	db := openSQLite(t, filepath.Join(plugins, rocaops.Name, rocaops.DatabaseFilename))
	defer db.Close()
	digest := strings.Repeat("a", 64)
	result, err := db.Exec(`INSERT INTO memory_records
		(canonical_digest, provenance, layer, content, metadata, origin, status, created_at)
		VALUES (?, 'core', 'project', ?, '{}', 'agent', 'active', '2026-08-15T10:00:00Z')`, digest, content)
	if err != nil {
		t.Fatal(err)
	}
	physicalID, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	batchID := fmt.Sprintf("fixture-batch-%d", legacyID)
	if _, err := db.Exec(`INSERT INTO migration_batches
		(migration, batch_id, destination_table, source_database, source_table,
		 row_count, canonical_digest, high_water_mark)
		VALUES ('data2-memory-custody', ?, 'memory_records', 'core', 'memories', 1, ?, ?)`,
		batchID, digest, fmt.Sprint(legacyID)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO custody_memberships
		(migration, source_database, source_table, source_key, destination_table,
		 destination_key, canonical_digest, batch_id)
		VALUES ('data2-memory-custody', 'core', 'memories', ?, 'memory_records', ?, ?, ?)`,
		fmt.Sprint(legacyID), fmt.Sprint(physicalID), digest, batchID); err != nil {
		t.Fatal(err)
	}
}

func seedHubCoreSession(t *testing.T, plugins, sessionID, agent, surface string) {
	t.Helper()
	db := openSQLite(t, filepath.Join(plugins, rocacorpus.Name, rocacorpus.DatabaseFilename))
	defer db.Close()
	digest := strings.Repeat("b", 64)
	batchID := "fixture-session-batch"
	if _, err := db.Exec(`INSERT INTO sessions
		(session_id, source_agent, source_surface, title, metadata)
		VALUES (?, ?, ?, 'Synthetic OpenCode session', '{}')`,
		sessionID, agent, surface); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO session_versions
		(version_digest, session_id, source_agent, source_surface)
		VALUES (?, ?, ?, ?)`,
		digest, sessionID, agent, surface); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO migration_batches
		(migration, batch_id, destination_table, source_database, source_table,
		 row_count, canonical_digest, high_water_mark)
		VALUES ('corpus-archive-sessions', ?, 'session_versions', 'core', 'sessions', 1, ?, ?)`,
		batchID, digest, sessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO corpus_source_rows
		(source_database, source_table, source_key, destination_table, version_digest,
		 source_row_id, session_id)
		VALUES ('core', 'sessions', ?, 'session_versions', ?, 1, ?)`,
		sessionID, digest, sessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO custody_memberships
		(migration, source_database, source_table, source_key, destination_table,
		 destination_key, canonical_digest, batch_id)
		VALUES ('corpus-archive-sessions', 'core', 'sessions', ?,
		        'session_versions', ?, ?, ?)`, sessionID, digest, digest, batchID); err != nil {
		t.Fatal(err)
	}
}

func openSQLite(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	return db
}
