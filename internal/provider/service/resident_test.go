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
	"github.com/thellmwhisperer/la-roca/internal/provider/query"
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
