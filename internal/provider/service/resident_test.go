package service

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/distribution/rocacorpus"
	"github.com/thellmwhisperer/la-roca/internal/distribution/rocaops"
	"github.com/thellmwhisperer/la-roca/internal/ingest"
	"github.com/thellmwhisperer/la-roca/internal/provider/plugin"
	"github.com/thellmwhisperer/la-roca/internal/provider/query"
	"github.com/thellmwhisperer/la-roca/internal/store"
	"github.com/thellmwhisperer/la-roca/internal/store/search"
)

func TestResidentInitializationHonorsItsContext(t *testing.T) {
	options := residentTestOptions(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	svc, err := openWithContext(ctx, options)
	if svc != nil {
		svc.Close()
	}
	if err == nil || !strings.Contains(err.Error(), context.Canceled.Error()) {
		t.Fatalf("resident initialization with a canceled context = %v", err)
	}
}

func TestWordProofIncludesAndRebuildsOperationalHistory(t *testing.T) {
	svc := openResident(t, residentTestOptions(t))
	if _, err := svc.Init(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ops.SQL().Exec(`INSERT INTO memories (layer, content, origin)
		VALUES ('handoff', 'harbour lighthouse', 'agent')`); err != nil {
		t.Fatal(err)
	}

	proof := svc.proveWordSearch(t.Context())
	if !proof.Ready || proof.Empty || proof.Word == "" {
		t.Fatalf("ops-only history did not prove ready: %+v", proof)
	}
	if _, err := svc.ops.SQL().Exec(`INSERT INTO memories_fts(memories_fts) VALUES('delete-all')`); err != nil {
		t.Fatal(err)
	}
	if broken := svc.proveWordSearch(t.Context()); broken.Ready || broken.Empty {
		t.Fatalf("broken ops index did not become the aggregate fault: %+v", broken)
	}
	if _, err := svc.rebuildWordSearch(t.Context()); err != nil {
		t.Fatal(err)
	}
	if repaired := svc.proveWordSearch(t.Context()); !repaired.Ready {
		t.Fatalf("ops index was not repaired by the shared rebuild: %+v", repaired)
	}
}

func TestWordProofIncludesBrokenCorpusBesideHealthyCore(t *testing.T) {
	svc := corpusResidentService(t)
	if _, err := svc.Init(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.db.SQL().Exec(`INSERT INTO memories (layer, content, origin)
		VALUES ('fact', 'healthy core lighthouse', 'agent')`); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.corpus.SQL().Exec(`INSERT INTO memories (layer, content, origin)
		VALUES ('fact', 'broken corpus harbour', 'agent')`); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.corpus.SQL().Exec(`INSERT INTO memories_fts(memories_fts) VALUES('delete-all')`); err != nil {
		t.Fatal(err)
	}
	proof := svc.proveWordSearch(t.Context())
	if proof.Ready || proof.Empty || proof.Word == "" {
		t.Fatalf("healthy core masked corpus failure: %+v", proof)
	}
}

func TestInitDoesNotRebuildTokenlessHistory(t *testing.T) {
	var progress []string
	options := residentTestOptions(t)
	options.Progress = func(line string) { progress = append(progress, line) }
	svc := openResident(t, options)
	if _, err := svc.ops.SQL().Exec(`INSERT INTO memories (layer, content, origin)
		VALUES ('handoff', '😀', 'agent')`); err != nil {
		t.Fatal(err)
	}
	result, err := svc.Init(t.Context())
	if err != nil || result.WordSearch == nil || result.WordSearch.Ready || !result.WordSearch.Empty {
		t.Fatalf("tokenless init proof = %+v, err %v", result.WordSearch, err)
	}
	if strings.Contains(strings.Join(progress, "\n"), "rebuilding the full-text index") {
		t.Fatalf("tokenless history triggered a rebuild: %v", progress)
	}
}

func TestStableLayerDatabaseClosesAmbiguousPhysicalSnapshots(t *testing.T) {
	tempRoot := t.TempDir()
	t.Setenv("TMPDIR", tempRoot)
	t.Setenv("TMP", tempRoot)
	t.Setenv("TEMP", tempRoot)
	manifest := &plugin.Manifest{
		Name:  rocaOpsPluginName,
		Verbs: []plugin.Verb{{Name: StoreVerb}},
	}
	var descriptors []plugin.Descriptor
	for index := range 2 {
		path := filepath.Join(t.TempDir(), fmt.Sprintf("ops-%d.db", index))
		database, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := database.Exec(`CREATE TABLE registry (id INTEGER)`); err != nil {
			t.Fatal(err)
		}
		if err := database.Close(); err != nil {
			t.Fatal(err)
		}
		descriptors = append(descriptors, plugin.Descriptor{
			Name: rocaOpsPluginName, Database: path,
			DatabaseName: fmt.Sprintf("ops-%d", index), Schema: fmt.Sprintf("plugin_ops_%d", index),
			Manifest: manifest,
			Semantic: plugin.Semantic{
				Attachment: plugin.AttachmentResident,
				Tables:     []plugin.SemanticTable{{Name: "registry", Columns: []string{"id"}}},
			},
		})
	}
	selected, err := stableLayerDatabase(t.Context(), descriptors, true)
	if selected != nil || err == nil || !strings.Contains(err.Error(), "no single durable layer registry") {
		t.Fatalf("selected = %+v, error = %v", selected, err)
	}
	if err := store.CloseReadOnlySnapshots(); err != nil {
		t.Fatal(err)
	}
	err = filepath.WalkDir(tempRoot, func(_ string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() && strings.HasPrefix(entry.Name(), "roca-read-only-snapshot-") {
			return fmt.Errorf("partial service open left snapshot %q", entry.Name())
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestResidentQueriesAcquireIndependentReadConnections(t *testing.T) {
	svc, err := openWithContext(t.Context(), residentTestOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	first, firstAttached, err := svc.openQueryConnection(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer closeQueryConnection(first, firstAttached)
	second, secondAttached, err := svc.openQueryConnection(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer closeQueryConnection(second, secondAttached)
	if first == second {
		t.Fatal("concurrent resident queries share one serialized connection")
	}
	for _, connection := range []*sql.Conn{first, second} {
		var rows int
		if err := connection.QueryRowContext(t.Context(),
			"SELECT COUNT(*) FROM plugin_roca_ops.memories").Scan(&rows); err != nil {
			t.Fatalf("resident database is unavailable on an acquired connection: %v", err)
		}
	}
}

func TestBundledCorpusIsAlwaysResidentWithoutTheGenericPluginFlag(t *testing.T) {
	svc := corpusResidentService(t)

	if len(svc.resident) != 1 || svc.resident[0].Name != rocacorpus.Name ||
		len(svc.residentWarnings) != 0 {
		t.Fatalf("resident = %+v, warnings = %v; want the bundled corpus",
			svc.resident, svc.residentWarnings)
	}
	route := svc.pluginsForQuestion(t.Context(), "which sessions and exchanges were harvested?")
	if len(route.databases) != 1 || route.databases[0].Name != rocacorpus.Name {
		t.Fatalf("routed databases = %+v, want corpus", route.databases)
	}
	if consulted := route.consulted(); len(consulted) != 2 ||
		consulted[0] != "core" || consulted[1] != "plugin:roca-corpus" {
		t.Fatalf("consulted = %v, want core and corpus", consulted)
	}
}

func TestHermesMemoryDedupReadsReservedOperationalMemories(t *testing.T) {
	options := residentTestOptions(t)
	directory := filepath.Dir(options.DBPath)
	if _, err := rocacorpus.Ensure(options.PluginDir, filepath.Join(directory, "bin"), "v-test"); err != nil {
		t.Fatal(err)
	}
	options.CorpusEnabled = true
	options.Sources = ingest.ResolveRoots(
		ingest.Environment{GOOS: "darwin", Home: directory}, ingest.Settings{},
	)
	memories := make([]string, 9)
	for index := range memories {
		memories[index] = fmt.Sprintf("Synthetic Hermes reserved memory %d.", index+1)
	}
	memoryPath := filepath.Join(directory, ".hermes", "memories", "MEMORY.md")
	if err := os.MkdirAll(filepath.Dir(memoryPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(memoryPath, []byte(strings.Join(memories, "\n§\n")), 0o600); err != nil {
		t.Fatal(err)
	}

	svc, err := openWithContext(t.Context(), options)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	for index, content := range memories {
		if _, err := svc.ops.SQL().Exec(`INSERT INTO memories (id, layer, content, metadata, origin)
			VALUES (?, 'pattern', ?, '{}', 'agent')`,
			int64(1152921504606847051)+int64(index), content); err != nil {
			t.Fatal(err)
		}
	}

	result, err := svc.Ingest(t.Context(), IngestRequest{})
	if err != nil {
		t.Fatal(err)
	}
	var duplicates int
	if err := svc.corpus.SQL().QueryRow(`SELECT COUNT(*) FROM memories`).Scan(&duplicates); err != nil {
		t.Fatal(err)
	}
	if duplicates != 0 || result.Sources["hermes"].MemoriesInserted != 0 {
		t.Fatalf("reserved Hermes memories duplicated into corpus: rows=%d counts=%+v",
			duplicates, result.Sources["hermes"])
	}
}

func TestKeywordSearchReadsExchangesFromTheResidentCorpus(t *testing.T) {
	svc := corpusResidentService(t)
	if _, err := svc.Init(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.corpus.SQL().Exec(`
		INSERT INTO sessions (session_id, source_agent) VALUES ('fixture-session', 'fixture');
		INSERT INTO exchanges (session_id, human_text, agent_text, human_timestamp, agent_timestamp)
		VALUES ('fixture-session', 'where is the cobalt atlas', 'in the perennial corpus',
		        '2026-08-14T08:00:00Z', '2026-08-14T08:00:01Z')`); err != nil {
		t.Fatal(err)
	}
	columns, rows, statement, _, warnings, err := svc.searchByTerm(t.Context(),
		query.Plan{Template: query.TemplateSearchByTerm, Term: "cobalt+atlas", Limit: 10},
		search.MethodLike, 0, true, pluginRoute{databases: svc.resident})
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 || len(rows) != 1 || rows[0]["database"] != "plugin:roca-corpus" {
		t.Fatalf("columns = %v, rows = %+v, warnings = %v, statement = %s",
			columns, rows, warnings, statement)
	}
	if !strings.Contains(statement, "plugin_roca_corpus") {
		t.Fatalf("declared search omits corpus SQL:\n%s", statement)
	}
}

// Read-only can never install the package it would be demanding, so an
// installation that has no bundled corpus yet still opens, answers from core and
// carries the omission into every answer instead of failing every read.
func TestReadOnlyAnswersFromCoreWithoutTheBundledCorpus(t *testing.T) {
	directory := t.TempDir()
	seed, err := openWithContext(t.Context(), Options{
		DBPath: filepath.Join(directory, "roca.db"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := seed.Init(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}

	svc, err := openWithContext(t.Context(), Options{
		DBPath:        filepath.Join(directory, "roca.db"),
		PluginDir:     filepath.Join(directory, "plugins"),
		CorpusEnabled: true,
		ReadOnly:      true,
	})
	if err != nil {
		t.Fatalf("read-only open without the bundled corpus: %v", err)
	}
	defer svc.Close()

	if len(svc.resident) != 0 || len(svc.residentWarnings) != 1 ||
		!strings.Contains(svc.residentWarnings[0], rocacorpus.Name) {
		t.Fatalf("resident = %+v, warnings = %v", svc.resident, svc.residentWarnings)
	}
	route := svc.pluginsForQuestion(t.Context(), "which sessions were harvested?")
	if len(route.databases) != 0 || len(route.warnings) != 1 {
		t.Fatalf("routed databases = %+v, warnings = %v; the omission must travel",
			route.databases, route.warnings)
	}
}

func corpusResidentService(t *testing.T) *Service {
	t.Helper()
	directory := t.TempDir()
	plugins := filepath.Join(directory, "plugins")
	if _, err := rocacorpus.Ensure(plugins, filepath.Join(directory, "bin"), "v-test"); err != nil {
		t.Fatal(err)
	}
	svc, err := openWithContext(t.Context(), Options{
		DBPath: filepath.Join(directory, "roca.db"), PluginDir: plugins, CorpusEnabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { svc.Close() })
	return svc
}

// openResident opens the service with options and registers its cleanup,
// failing the test on error.
func openResident(t *testing.T, options Options) *Service {
	t.Helper()
	svc, err := openWithContext(t.Context(), options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	return svc
}

func residentTestOptions(t *testing.T) Options {
	t.Helper()
	directory := t.TempDir()
	plugins := filepath.Join(directory, "plugins")
	if _, err := rocaops.Ensure(plugins, filepath.Join(directory, "bin"), "v-test"); err != nil {
		t.Fatal(err)
	}
	return Options{
		DBPath: filepath.Join(directory, "roca.db"), PluginDir: plugins, RocaOpsEnabled: true,
	}
}
