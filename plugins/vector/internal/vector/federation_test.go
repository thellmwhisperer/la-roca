package vector

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func TestFederationBuildsOwnedSidecarsAndGarbageCollectsByDelta(t *testing.T) {
	federation, corpusPath, opsPath, embedder := federationFixture(t)
	ctx := context.Background()

	first, err := federation.Ingest(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if first.Added < 4 || first.Sources != 4 || len(first.Databases) != 2 {
		t.Fatalf("first federation delta = %+v", first)
	}
	corpusSidecar, opsSidecar := SidecarPath(corpusPath), SidecarPath(opsPath)
	for path, owner := range map[string]string{
		corpusSidecar: "roca-corpus/corpus",
		opsSidecar:    "roca-ops/ops",
	} {
		metadata := sidecarMeta(t, path)
		if metadata["owner"] != owner || metadata["model"] != DefaultModel ||
			metadata["dimensions"] != "8" || metadata["version"] != "v-test" ||
			metadata["contract"] == "" || metadata["source_fingerprint"] == "" {
			t.Fatalf("sidecar %s metadata = %+v", owner, metadata)
		}
	}
	allInputs := strings.Join(flattenInputs(embedder.inputs), "\n")
	if strings.Contains(allInputs, "raw-counter") || !strings.Contains(allInputs, "remembered body") ||
		!strings.Contains(allInputs, "Operational decision") {
		t.Fatalf("declared embedding inputs = %q", allInputs)
	}

	embeddingCalls := len(embedder.inputs)
	steady, err := federation.Ingest(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if steady.Added != 0 || steady.Updated != 0 || steady.Removed != 0 ||
		steady.Unchanged != first.Chunks || len(embedder.inputs) != embeddingCalls {
		t.Fatalf("unchanged federation delta = %+v, embedding calls %d -> %d",
			steady, embeddingCalls, len(embedder.inputs))
	}

	mutateSourceDatabase(t, corpusPath,
		`UPDATE articles SET body='A changed remembered body' WHERE id='article-1'`)
	mutateSourceDatabase(t, opsPath, `DELETE FROM memories WHERE id=2`)
	changed, err := federation.Ingest(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if changed.Updated == 0 || changed.Removed == 0 {
		t.Fatalf("changed federation delta = %+v", changed)
	}
	ops := openTestSQLite(t, opsSidecar)
	defer ops.Close()
	var deleted int
	if err := ops.QueryRow(`SELECT COUNT(*) FROM chunks WHERE source_kind='memories' AND source_id='memories/2'`).Scan(&deleted); err != nil {
		t.Fatal(err)
	}
	if deleted != 0 {
		t.Fatalf("deleted declared row retained %d chunks", deleted)
	}
}

func TestChronologicalContractChangeDoesNotReembedExistingChunks(t *testing.T) {
	federation, corpusPath, _, embedder := federationFixture(t)
	ctx := context.Background()
	first, err := federation.Ingest(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	embeddingCalls := len(embedder.inputs)
	mutateSourceDatabase(t, corpusPath, `ALTER TABLE articles ADD COLUMN indexed_at TEXT`)
	mutateSourceDatabase(t, corpusPath, `UPDATE articles SET indexed_at=id`)
	federation.databases[0].Tables[0].TimeColumns = []string{"indexed_at"}

	rescanned, err := federation.Ingest(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if rescanned.Unchanged != first.Chunks || rescanned.Added != 0 || rescanned.Updated != 0 ||
		rescanned.Removed != 0 || len(embedder.inputs) != embeddingCalls {
		t.Fatalf("chronology-only rescan = %+v, embedding calls %d -> %d",
			rescanned, embeddingCalls, len(embedder.inputs))
	}
}

func TestFederatedWorkerBuildsOwnedSidecarsBeforeRetiringLegacyMonolith(t *testing.T) {
	federation, corpusPath, opsPath, _ := federationFixture(t)
	state := t.TempDir()
	legacy := filepath.Join(state, DatabaseFilename)
	if err := os.WriteFile(legacy, []byte("legacy central index"), 0o600); err != nil {
		t.Fatal(err)
	}

	completion := (FederatedWorker{Federation: federation, DataDir: state}).Run(context.Background())
	if completion.ExitStatus != 0 || completion.Error != "" {
		t.Fatalf("federated worker completion = %+v", completion)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatalf("legacy central index remains after successful federation build: %v", err)
	}
	for path, owner := range map[string]string{
		SidecarPath(corpusPath): "roca-corpus/corpus",
		SidecarPath(opsPath):    "roca-ops/ops",
	} {
		if metadata := sidecarMeta(t, path); metadata["owner"] != owner {
			t.Fatalf("migrated sidecar metadata = %+v, want owner %s", metadata, owner)
		}
	}
	t.Logf("worker completion: status=%d added=%d sources=%d chunks=%d",
		completion.ExitStatus, completion.Delta.Added, completion.Delta.Sources, completion.Delta.Chunks)
	t.Log("sidecars: roca-corpus/corpus, roca-ops/ops; legacy central index: removed")
}

func TestFederatedWorkerReusesLegacyMonolithEmbeddingsBeforeRetiringIt(t *testing.T) {
	root := t.TempDir()
	corpusDir, opsDir := filepath.Join(root, "roca-corpus"), filepath.Join(root, "roca-ops")
	for _, directory := range []string{corpusDir, opsDir} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	corpusPath := filepath.Join(corpusDir, "roca-corpus.db")
	opsPath := filepath.Join(opsDir, "roca-ops.db")
	createSourceDatabase(t, corpusPath, `
		CREATE TABLE sessions(session_id TEXT PRIMARY KEY,title TEXT,project TEXT,started_at TEXT);
		CREATE TABLE memories(id INTEGER PRIMARY KEY,content TEXT,project TEXT,created_at TEXT,source_session TEXT);
		CREATE TABLE exchanges(id INTEGER PRIMARY KEY,session_id TEXT,human_text TEXT,agent_text TEXT,human_timestamp TEXT,agent_timestamp TEXT);
		CREATE TABLE thinking_blocks(id INTEGER PRIMARY KEY,session_id TEXT,full_text TEXT);
		INSERT INTO sessions VALUES ('session-1','Release notes','vector-project','2026-03-01');
		INSERT INTO memories VALUES (1,'Corpus memory already embedded','vector-project','2026-03-01','session-1');
		INSERT INTO exchanges VALUES (1,'session-1','Question already embedded','Answer already embedded','2026-03-01','2026-03-01');
		INSERT INTO thinking_blocks VALUES (1,'session-1','Reasoning already embedded');`)
	createSourceDatabase(t, opsPath, `
		CREATE TABLE memories(id INTEGER PRIMARY KEY,content TEXT,project TEXT,created_at TEXT);
		INSERT INTO memories VALUES (1,'Operational memory already embedded','ops','2026-03-01');`)
	writeRegistry(t, root, vectorRegistry{Schema: vectorRegistrySchema, Databases: []vectorDatabase{
		{Plugin: "roca-corpus", Database: "corpus", Path: "roca-corpus.db", Alias: "plugin_roca_corpus",
			Tables: []vectorTable{
				{Name: "sessions", IDColumn: "session_id", TextColumns: []string{"title", "project"},
					TimeColumns: []string{"session_id"}, Columns: []string{"session_id", "title", "project", "started_at"}},
				{Name: "memories", IDColumn: "id", TextColumns: []string{"content"},
					TimeColumns: []string{"id"}, Columns: []string{"id", "content", "project", "created_at", "source_session"}},
				{Name: "exchanges", IDColumn: "id", TextColumns: []string{"human_text", "agent_text"},
					TimeColumns: []string{"id"}, Columns: []string{"id", "session_id", "human_text", "agent_text", "human_timestamp", "agent_timestamp"}},
				{Name: "thinking_blocks", IDColumn: "id", TextColumns: []string{"full_text"},
					TimeColumns: []string{"id"}, Columns: []string{"id", "session_id", "full_text"}},
			}},
		{Plugin: "roca-ops", Database: "ops", Path: "roca-ops.db", Alias: "plugin_roca_ops",
			Tables: []vectorTable{{Name: "memories", IDColumn: "id", TextColumns: []string{"content"},
				TimeColumns: []string{"id"}, Columns: []string{"id", "content", "project", "created_at"}}}},
	}})

	embedder := &recordingEmbedder{}
	state := t.TempDir()
	legacy := filepath.Join(state, DatabaseFilename)
	legacyCorpus := &memoryCorpus{sources: []sourceRow{
		{kind: "sessions", sessionID: "session-1", text: "Release notes\nvector-project"},
		{kind: "memories", text: "Corpus memory already embedded"},
		{kind: "exchanges", sessionID: "session-1", ordinal: 1, hasOrdinal: true,
			text: "Question already embedded\n\nAnswer already embedded"},
		{kind: "thinking_blocks", sessionID: "session-1", ordinal: 1, hasOrdinal: true,
			position: "1", text: "Reasoning already embedded"},
		{kind: "memories", text: "Operational memory already embedded"},
	}}
	legacyIndex := Index{Corpus: legacyCorpus, VectorPath: legacy, Model: DefaultModel, Embedder: embedder}
	if _, err := legacyIndex.Ingest(context.Background()); err != nil {
		t.Fatal(err)
	}
	embedder.inputs = nil

	runner := sqliteExecRunner(t, map[string]string{
		"plugin_roca_corpus": corpusPath,
		"plugin_roca_ops":    opsPath,
	})
	federation, err := LoadFederation(CoreCLI{Executable: "roca", Run: runner}, root,
		DefaultModel, "v-migration", embedder, nil)
	if err != nil {
		t.Fatal(err)
	}
	if partial, err := federation.Ingest(context.Background(), "memories"); err != nil || partial.Added != 2 {
		t.Fatalf("partial sidecar setup = %+v, err=%v", partial, err)
	}
	embedder.inputs = nil
	completion := (FederatedWorker{Federation: federation, DataDir: state}).Run(context.Background())
	if completion.ExitStatus != 0 || completion.Error != "" {
		t.Fatalf("federated worker completion = %+v", completion)
	}
	recordedRaw, err := os.ReadFile(filepath.Join(state, CompletionFilename))
	if err != nil {
		t.Fatal(err)
	}
	var recorded Completion
	if err := json.Unmarshal(recordedRaw, &recorded); err != nil {
		t.Fatal(err)
	}
	if recorded.ExitStatus != 0 || recorded.FinishedAt.IsZero() {
		t.Fatalf("recorded worker completion = %+v", recorded)
	}
	if completion.Delta.Chunks == 0 {
		t.Fatalf("federated migration delta = %+v", completion.Delta)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatalf("legacy central index remains after successful migration: %v", err)
	}
	for path, owner := range map[string]string{
		SidecarPath(corpusPath): "roca-corpus/corpus",
		SidecarPath(opsPath):    "roca-ops/ops",
	} {
		metadata := sidecarMeta(t, path)
		if metadata["owner"] != owner || metadata["model"] != DefaultModel ||
			metadata["dimensions"] != "8" || metadata["source_fingerprint"] == "" {
			t.Fatalf("migrated sidecar %s metadata = %+v", owner, metadata)
		}
	}
	t.Logf("legacy migration: reused=%d embedded=%d sidecars=roca-corpus/corpus,roca-ops/ops completion=ready legacy=removed",
		completion.Delta.Unchanged, len(flattenInputs(embedder.inputs)))
}

func TestLegacySeedReembedsWhenContextChangesEmbeddingInput(t *testing.T) {
	root := t.TempDir()
	pluginDir := filepath.Join(root, "roca-ops")
	if err := os.MkdirAll(pluginDir, 0o700); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(pluginDir, "roca-ops.db")
	const text = "short personal memory"
	createSourceDatabase(t, dbPath, `
		CREATE TABLE memories(id INTEGER PRIMARY KEY, content TEXT, project TEXT, created_at TEXT);
		INSERT INTO memories VALUES (1,'short personal memory','Wellbeing project','2026-03-18');`)
	writeRegistry(t, root, vectorRegistry{Schema: vectorRegistrySchema, Databases: []vectorDatabase{{
		Plugin: "roca-ops", Database: "ops", Path: "roca-ops.db", Alias: "plugin_roca_ops",
		Tables: []vectorTable{{Name: "memories", IDColumn: "id", TextColumns: []string{"content"},
			TimeColumns: []string{"created_at"}, Columns: []string{"id", "content", "project", "created_at"}}},
	}}})

	embedder := &recordingEmbedder{}
	legacy := filepath.Join(t.TempDir(), DatabaseFilename)
	legacyIndex := Index{Corpus: &memoryCorpus{sources: []sourceRow{{kind: "memories", text: text}}},
		VectorPath: legacy, Model: DefaultModel, Embedder: embedder}
	if _, err := legacyIndex.Ingest(context.Background()); err != nil {
		t.Fatal(err)
	}
	legacyStore := openTestSQLite(t, legacy)
	if _, err := legacyStore.Exec(`UPDATE chunks SET fingerprint=?`, embeddingFingerprint("memories", text)); err != nil {
		legacyStore.Close()
		t.Fatal(err)
	}
	if err := legacyStore.Close(); err != nil {
		t.Fatal(err)
	}
	embedder.inputs = nil

	federation, err := LoadFederation(CoreCLI{Executable: "roca", Run: sqliteExecRunner(t,
		map[string]string{"plugin_roca_ops": dbPath})}, root, DefaultModel, "v-test", embedder, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := federation.seedSidecarsFromLegacyMonolith(context.Background(), legacy); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(SidecarPath(dbPath)); !os.IsNotExist(err) {
		t.Fatalf("unheadered legacy vector was published as current: %v", err)
	}
	if _, err := federation.Ingest(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	want := DocumentPrefix + "[Wellbeing project · 2026-03] " + text
	if got := flattenInputs(embedder.inputs); len(got) != 1 || got[0] != want {
		t.Fatalf("current embedding inputs = %q, want %q", got, want)
	}
}

func TestFederatedWorkerKeepsLegacyMonolithUntilEveryDeclaredSidecarCompletes(t *testing.T) {
	federation, corpusPath, opsPath, _ := federationFixture(t)
	federation.databases = append(federation.databases, vectorDatabase{
		Plugin: "fixture", Database: "missing", Path: "missing.db", Alias: "plugin_fixture_missing",
		Tables: []vectorTable{{Name: "records", IDColumn: "id", TextColumns: []string{"content"}, TimeColumns: []string{"id"}}},
	})
	state := t.TempDir()
	legacy := filepath.Join(state, DatabaseFilename)
	if err := os.WriteFile(legacy, []byte("legacy central index"), 0o600); err != nil {
		t.Fatal(err)
	}

	completion := (FederatedWorker{Federation: federation, DataDir: state}).Run(context.Background())
	if completion.ExitStatus == 0 || !strings.Contains(completion.Error, "fixture/missing") {
		t.Fatalf("federated worker completion = %+v, want the missing declared database failure", completion)
	}
	if _, err := os.Stat(legacy); err != nil {
		t.Fatalf("legacy central index was retired before every sidecar completed: %v", err)
	}
	for path, owner := range map[string]string{
		SidecarPath(corpusPath): "roca-corpus/corpus",
		SidecarPath(opsPath):    "roca-ops/ops",
	} {
		if metadata := sidecarMeta(t, path); metadata["owner"] != owner {
			t.Fatalf("completed sidecar metadata = %+v, want owner %s", metadata, owner)
		}
	}
	t.Logf("partial federation failure: completed=roca-corpus/corpus,roca-ops/ops failed=fixture/missing legacy=retained")
}

func TestFederationTargetedDeltaPreservesOtherTablesAndCorpusQueryCompatibility(t *testing.T) {
	federation, corpusPath, _, embedder := federationFixture(t)
	if _, err := federation.Ingest(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	corpusSidecar := SidecarPath(corpusPath)
	store := openTestSQLite(t, corpusSidecar)
	if _, err := store.Exec(`INSERT INTO chunks(source_kind,source_id,chunk_index,fingerprint,locator)
		VALUES ('sentinel','sentinel/1',0,'sentinel','{}')`); err != nil {
		store.Close()
		t.Fatal(err)
	}
	store.Close()

	mutateSourceDatabase(t, corpusPath,
		`UPDATE articles SET title='A revised title' WHERE id='article-1'`)
	if delta, err := federation.Ingest(context.Background(), "articles"); err != nil || delta.Updated == 0 {
		t.Fatalf("targeted declared delta = %+v, err=%v", delta, err)
	}
	store = openTestSQLite(t, corpusSidecar)
	defer store.Close()
	var sentinel int
	if err := store.QueryRow(`SELECT COUNT(*) FROM chunks WHERE source_kind='sentinel'`).Scan(&sentinel); err != nil {
		t.Fatal(err)
	}
	if sentinel != 1 {
		t.Fatal("targeted declared delta removed an unrelated source kind")
	}

	index, err := federation.CorpusIndex()
	if err != nil {
		t.Fatal(err)
	}
	results, err := index.Query(context.Background(), "remembered", 5)
	if err != nil {
		t.Fatal(err)
	}
	foundRemembered := false
	for _, result := range results {
		foundRemembered = foundRemembered || strings.Contains(result.Text, "remembered body")
	}
	if len(results) == 0 || !foundRemembered {
		t.Fatalf("corpus compatibility query results = %+v", results)
	}
	if len(embedder.inputs) == 0 || !strings.HasPrefix(embedder.inputs[len(embedder.inputs)-1][0], QueryPrefix) {
		t.Fatalf("query embedding inputs = %q", embedder.inputs)
	}
}

func TestFederationQueryFansOutWithRoutingAndTaggedMergedHits(t *testing.T) {
	federation, _, opsPath, embedder := federationFixture(t)
	if _, err := federation.Ingest(context.Background(), ""); err != nil {
		t.Fatal(err)
	}

	queryCalls := len(embedder.inputs)
	result, err := federation.Query(context.Background(), "remembered decision", 10, "all")
	if err != nil {
		t.Fatal(err)
	}
	if result.MixedModels || result.Model != DefaultModel || len(result.DatabaseResults) != 0 {
		t.Fatalf("same-model federation result = %+v", result)
	}
	if len(embedder.inputs) != queryCalls+1 {
		t.Fatalf("same-model fan-out made %d query embedding calls, want 1",
			len(embedder.inputs)-queryCalls)
	}
	seen := map[string]bool{}
	for rank, hit := range result.Results {
		if hit.Rank != rank+1 || hit.Database == "" || hit.Table == "" || hit.ID == "" {
			t.Fatalf("untagged or unranked federated hit = %+v", hit)
		}
		seen[hit.Database] = true
	}
	if !seen["corpus"] || !seen["ops"] {
		t.Fatalf("federated databases in merged hits = %v", seen)
	}

	defaultResult, err := federation.Query(context.Background(), "remembered", 10, "")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(defaultResult.Databases, "ops") || !slices.Contains(defaultResult.Databases, "corpus") {
		t.Fatalf("default databases = %v, want the whole federation", defaultResult.Databases)
	}
	opsResult, err := federation.Query(context.Background(), "decision", 10, "ops")
	if err != nil {
		t.Fatal(err)
	}
	for _, hit := range opsResult.Results {
		if hit.Database != "ops" {
			t.Fatalf("explicit ops route returned %+v", hit)
		}
	}
	ftsOnly, err := federation.Query(context.Background(), "journey", 10, "cron")
	if err != nil {
		t.Fatal(err)
	}
	if len(ftsOnly.Results) != 0 || !strings.Contains(strings.Join(ftsOnly.Notices, "\n"),
		"database cron has no vector declaration") {
		t.Fatalf("undeclared database route = %+v", ftsOnly)
	}
	if _, err := federation.Query(context.Background(), "decision", 10, "missing"); err == nil ||
		!strings.Contains(err.Error(), "attached databases: core, corpus, ops, cron") {
		t.Fatalf("unknown vector database = %v", err)
	}

	if err := os.Remove(SidecarPath(opsPath)); err != nil {
		t.Fatal(err)
	}
	fallback, err := federation.Query(context.Background(), "remembered", 10, "all")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(fallback.Notices, "\n"), "database ops") ||
		!strings.Contains(strings.Join(fallback.Notices, "\n"), "FTS-only") {
		t.Fatalf("missing-sidecar notices = %q", fallback.Notices)
	}
	for _, hit := range fallback.Results {
		if hit.Database != "corpus" {
			t.Fatalf("missing-sidecar fallback returned %+v", hit)
		}
	}
}

func TestFederationQueryUsesTheCoreRuntimeInventory(t *testing.T) {
	federation, _, _, _ := federationFixture(t)
	if _, err := federation.Ingest(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	federation.Core.Run = databaseScopeRunner(federation.Core.Run, []DatabaseSelection{
		{Source: "core", Database: "core"},
		{Source: "plugin:roca-corpus", Database: "corpus"},
	})
	result, err := federation.Query(context.Background(), "remembered decision", 10, "all")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(result.Databases, ",") != "core,corpus" {
		t.Fatalf("runtime-routed databases = %v", result.Databases)
	}
	for _, hit := range result.Results {
		if hit.Database != "corpus" {
			t.Fatalf("feature-gated database escaped runtime inventory: %+v", hit)
		}
	}
}

func TestResolveSourcesAlignsUnionArmsAcrossChunkGenerations(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "roca-corpus.db")
	createSourceDatabase(t, dbPath, `
		CREATE TABLE articles(id TEXT PRIMARY KEY, title TEXT, body TEXT);
		CREATE TABLE notes(id TEXT PRIMARY KEY, body TEXT);
		INSERT INTO articles VALUES ('article-new','New policy title','new-policy giraffe unique');
		INSERT INTO notes VALUES ('note-old','old-policy zebra unique');`)
	declared := DeclaredCorpus{
		Core: CoreCLI{Executable: "roca", Run: sqliteExecRunner(t, map[string]string{"plugin_roca_corpus": dbPath})},
		Database: vectorDatabase{Plugin: "roca-corpus", Database: "corpus", Alias: "plugin_roca_corpus",
			Tables: []vectorTable{
				{Name: "articles", IDColumn: "id", TextColumns: []string{"title", "body"}},
				{Name: "notes", IDColumn: "id", TextColumns: []string{"body"}},
			}},
	}
	article := sourceRow{kind: "articles", sourceID: "article-new"}
	note := sourceRow{kind: "notes", sourceID: "note-old"}
	resolved, err := declared.ResolveSources(context.Background(), []sourceLookup{
		{kind: "articles", where: locator{SourceID: article.sourceID, Identity: article.identity()}},
		{kind: "notes", where: locator{SourceID: note.sourceID, Identity: note.identity()}},
	})
	if err != nil {
		t.Fatalf("mixed-policy resolve: %v", err)
	}
	if !strings.Contains(resolved[sourceLookupKey("articles", "article-new")], "giraffe") ||
		!strings.Contains(resolved[sourceLookupKey("notes", "note-old")], "zebra") {
		t.Fatalf("mixed-policy texts = %v", resolved)
	}
}

func TestMixedChunkPolicySidecarStaysQueryable(t *testing.T) {
	root := t.TempDir()
	corpusDir := filepath.Join(root, "roca-corpus")
	if err := os.MkdirAll(corpusDir, 0o700); err != nil {
		t.Fatal(err)
	}
	corpusPath := filepath.Join(corpusDir, "roca-corpus.db")
	createSourceDatabase(t, corpusPath, `
		CREATE TABLE articles(id TEXT PRIMARY KEY, title TEXT, body TEXT);
		CREATE TABLE notes(id TEXT PRIMARY KEY, body TEXT);
		INSERT INTO articles VALUES ('article-new','New policy title','new-policy giraffe unique');
		INSERT INTO notes VALUES ('note-old','old-policy zebra unique');`)
	writeRegistry(t, root, vectorRegistry{Schema: vectorRegistrySchema, Databases: []vectorDatabase{
		{Plugin: "roca-corpus", Database: "corpus", Path: "roca-corpus.db", Alias: "plugin_roca_corpus",
			Tables: []vectorTable{
				{Name: "articles", IDColumn: "id", TextColumns: []string{"title", "body"}, TimeColumns: []string{"id"}},
				{Name: "notes", IDColumn: "id", TextColumns: []string{"body"}, TimeColumns: []string{"id"}},
			}},
	}})
	runner := sqliteExecRunner(t, map[string]string{"plugin_roca_corpus": corpusPath})
	runner = databaseScopeRunner(runner, []DatabaseSelection{
		{Source: "core", Database: "core"},
		{Source: "plugin:roca-corpus", Database: "corpus"},
	})
	embedder := &recordingEmbedder{}
	federation, err := LoadFederation(CoreCLI{Executable: "roca", Run: runner}, root,
		DefaultModel, "v-test", embedder, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := federation.Ingest(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	store := openTestSQLite(t, SidecarPath(corpusPath))
	if _, err := store.Exec(`UPDATE chunks SET text_column='' WHERE source_kind='notes'`); err != nil {
		store.Close()
		t.Fatal(err)
	}
	store.Close()
	result, err := federation.Query(context.Background(), "giraffe zebra", 10, "")
	if err != nil {
		t.Fatalf("mixed-policy query: %v", err)
	}
	seen := map[string]bool{}
	for _, hit := range result.Results {
		seen[hit.Table+"/"+hit.ID] = true
	}
	if !seen["articles/article-new"] || !seen["notes/note-old"] {
		t.Fatalf("mixed-policy hits = %+v", result.Results)
	}
}

func TestFederationQueryFansOutDuplicateCanonicalNamesBySource(t *testing.T) {
	root := t.TempDir()
	firstDir := filepath.Join(root, "fixture-first")
	secondDir := filepath.Join(root, "fixture-second")
	for _, directory := range []string{firstDir, secondDir} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	firstPath := filepath.Join(firstDir, "first.db")
	secondPath := filepath.Join(secondDir, "second.db")
	createSourceDatabase(t, firstPath, `
		CREATE TABLE records(id TEXT PRIMARY KEY,body TEXT);
		INSERT INTO records VALUES ('first-id','alpha first body');`)
	createSourceDatabase(t, secondPath, `
		CREATE TABLE records(id TEXT PRIMARY KEY,body TEXT);
		INSERT INTO records VALUES ('second-id','alpha second body');`)
	writeRegistry(t, root, vectorRegistry{Schema: vectorRegistrySchema, Databases: []vectorDatabase{
		{Plugin: "fixture-first", Database: "shared", Path: "first.db", Alias: "plugin_fixture_first",
			Tables: []vectorTable{{Name: "records", IDColumn: "id", TextColumns: []string{"body"}, TimeColumns: []string{"id"}}}},
		{Plugin: "fixture-second", Database: "shared", Path: "second.db", Alias: "plugin_fixture_second",
			Tables: []vectorTable{{Name: "records", IDColumn: "id", TextColumns: []string{"body"}, TimeColumns: []string{"id"}}}},
	}, Routes: []vectorRoute{
		{Plugin: "fixture-first", Database: "shared", Alias: "plugin_fixture_first", Source: "plugin:fixture-first"},
		{Plugin: "fixture-second", Database: "shared", Alias: "plugin_fixture_second", Source: "plugin:fixture-second"},
	}})
	runner := sqliteExecRunner(t, map[string]string{
		"plugin_fixture_first":  firstPath,
		"plugin_fixture_second": secondPath,
	})
	runner = databaseScopeRunner(runner, []DatabaseSelection{
		{Source: "core", Database: "core"},
		{Source: "plugin:fixture-first", Database: "shared"},
		{Source: "plugin:fixture-second", Database: "shared"},
	})
	federation, err := LoadFederation(CoreCLI{Executable: "roca", Run: runner}, root,
		DefaultModel, "v-test", &recordingEmbedder{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := federation.Ingest(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	result, err := federation.Query(context.Background(), "alpha", 10, "all")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, hit := range result.Results {
		seen[hit.ID] = true
	}
	if strings.Join(result.Databases, ",") != "core,shared,shared" ||
		!seen["first-id"] || !seen["second-id"] {
		t.Fatalf("duplicate-name federated query = %+v", result)
	}
}

func TestFederationQueryKeepsMixedModelsPerDatabaseAndFailsSoftWithoutModel(t *testing.T) {
	federation, _, opsPath, embedder := federationFixture(t)
	if _, err := federation.Ingest(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	ops := openTestSQLite(t, SidecarPath(opsPath))
	if _, err := ops.Exec(`UPDATE meta SET value='synthetic-embed-v2' WHERE key='model'`); err != nil {
		ops.Close()
		t.Fatal(err)
	}
	if err := ops.Close(); err != nil {
		t.Fatal(err)
	}

	queryCalls := len(embedder.inputs)
	result, err := federation.Query(context.Background(), "remembered decision", 10, "all")
	if err != nil {
		t.Fatal(err)
	}
	if !result.MixedModels || !result.VectorExecuted || len(result.Results) != 0 || len(result.DatabaseResults) != 2 {
		t.Fatalf("mixed-model federation result = %+v", result)
	}
	if len(embedder.inputs) != queryCalls+2 {
		t.Fatalf("mixed-model fan-out made %d query embedding calls, want 2",
			len(embedder.inputs)-queryCalls)
	}
	if !strings.Contains(strings.Join(result.Notices, "\n"), "not merged") {
		t.Fatalf("mixed-model notices = %q", result.Notices)
	}
	for _, database := range result.DatabaseResults {
		if database.Database == "" || database.Model == "" || len(database.Results) == 0 {
			t.Fatalf("mixed-model database result = %+v", database)
		}
		for _, hit := range database.Results {
			if hit.Database != database.Database || hit.Table == "" || hit.ID == "" {
				t.Fatalf("mixed-model tagged hit = %+v", hit)
			}
		}
	}

	federation.Embedder = unavailableEmbedder{}
	fallback, err := federation.Query(context.Background(), "remembered decision", 10, "all")
	if err != nil {
		t.Fatal(err)
	}
	if fallback.VectorExecuted || len(fallback.Results) != 0 || len(fallback.DatabaseResults) != 0 ||
		!strings.Contains(strings.Join(fallback.Notices, "\n"), "continuing with FTS-only") {
		t.Fatalf("model-unavailable fallback = %+v", fallback)
	}
}

type unavailableEmbedder struct{}

func (unavailableEmbedder) Pull(context.Context, string) error { return nil }

func (unavailableEmbedder) Embed(context.Context, string, []string) ([][]float32, error) {
	return nil, fmt.Errorf("model is not installed")
}

func TestFederationRejectsSidecarDatabaseCollisionsAndUnownedFiles(t *testing.T) {
	collision := vectorRegistry{Schema: vectorRegistrySchema, Databases: []vectorDatabase{
		{Plugin: "fixture", Database: "records", Path: "records.db", Alias: "records",
			Tables: []vectorTable{{Name: "entries", IDColumn: "id", TextColumns: []string{"body"}, TimeColumns: []string{"id"}}}},
		{Plugin: "fixture", Database: "vectors", Path: "records.vector.db", Alias: "vectors",
			Tables: []vectorTable{{Name: "entries", IDColumn: "id", TextColumns: []string{"body"}, TimeColumns: []string{"id"}}}},
	}}
	if err := validateRegistry(collision); err == nil || !strings.Contains(err.Error(), "collides") {
		t.Fatalf("sidecar database collision passed with %v", err)
	}

	foreign := filepath.Join(t.TempDir(), "foreign.vector.db")
	createSourceDatabase(t, foreign, `CREATE TABLE records(id TEXT PRIMARY KEY, body TEXT);`)
	if err := assertSidecarOwner(foreign, "fixture/records"); err == nil {
		t.Fatalf("unowned foreign database passed with %v", err)
	}

	interrupted := filepath.Join(t.TempDir(), "interrupted.vector.db")
	store := openTestSQLite(t, interrupted)
	if err := ensureBaseSchema(store); err != nil {
		store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := assertSidecarOwner(interrupted, "fixture/records"); err != nil {
		t.Fatalf("interrupted vector build was refused: %v", err)
	}
}

func TestFederationSealsEmptySidecarWithDimensions(t *testing.T) {
	root := t.TempDir()
	pluginDir := filepath.Join(root, "fixture")
	if err := os.MkdirAll(pluginDir, 0o700); err != nil {
		t.Fatal(err)
	}
	databasePath := filepath.Join(pluginDir, "empty.db")
	createSourceDatabase(t, databasePath, `CREATE TABLE entries(id TEXT PRIMARY KEY, body TEXT);`)
	writeRegistry(t, root, vectorRegistry{Schema: vectorRegistrySchema, Databases: []vectorDatabase{{
		Plugin: "fixture", Database: "empty", Path: "empty.db", Alias: "fixture_empty",
		Tables: []vectorTable{{Name: "entries", IDColumn: "id", TextColumns: []string{"body"}, TimeColumns: []string{"id"}}},
	}}})
	embedder := &recordingEmbedder{}
	federation, err := LoadFederation(CoreCLI{Executable: "roca", Run: sqliteExecRunner(t,
		map[string]string{"fixture_empty": databasePath})}, root, DefaultModel, "v-empty", embedder, nil)
	if err != nil {
		t.Fatal(err)
	}
	delta, err := federation.Ingest(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if delta.Sources != 0 || delta.Chunks != 0 || len(embedder.inputs) != 1 ||
		len(embedder.inputs[0]) != 1 || !strings.Contains(embedder.inputs[0][0], "dimension probe") {
		t.Fatalf("empty federation delta = %+v, inputs = %q", delta, embedder.inputs)
	}
	metadata := sidecarMeta(t, SidecarPath(databasePath))
	if metadata["owner"] != "fixture/empty" || metadata["model"] != DefaultModel ||
		metadata["dimensions"] != "8" || metadata["version"] != "v-empty" {
		t.Fatalf("empty sidecar metadata = %+v", metadata)
	}
	store := openTestSQLite(t, SidecarPath(databasePath))
	defer store.Close()
	var chunks int
	if err := store.QueryRow(`SELECT COUNT(*) FROM chunks`).Scan(&chunks); err != nil || chunks != 0 {
		t.Fatalf("empty sidecar chunks = %d, err=%v", chunks, err)
	}
	if _, err := federation.Ingest(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	if len(embedder.inputs) != 1 {
		t.Fatalf("unchanged empty sidecar re-embedded %d batches", len(embedder.inputs))
	}
}

func federationFixture(t *testing.T) (Federation, string, string, *recordingEmbedder) {
	t.Helper()
	root := t.TempDir()
	corpusDir, opsDir := filepath.Join(root, "roca-corpus"), filepath.Join(root, "roca-ops")
	for _, directory := range []string{corpusDir, opsDir} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	corpusPath, opsPath := filepath.Join(corpusDir, "roca-corpus.db"), filepath.Join(opsDir, "roca-ops.db")
	createSourceDatabase(t, corpusPath, `
		CREATE TABLE articles(id TEXT PRIMARY KEY,title TEXT,body TEXT,telemetry TEXT);
		INSERT INTO articles VALUES ('article-1','Remembered title','A remembered body','raw-counter');
		INSERT INTO articles VALUES ('article-2','Second title','Second body','raw-counter');`)
	createSourceDatabase(t, opsPath, `
		CREATE TABLE memories(id INTEGER PRIMARY KEY,content TEXT,status TEXT,project TEXT,created_at TEXT);
		INSERT INTO memories VALUES (1,'Operational decision','active','ops','2026-03-01');
		INSERT INTO memories VALUES (2,'Temporary handoff','active','ops','2026-03-02');`)
	writeRegistry(t, root, vectorRegistry{Schema: vectorRegistrySchema, Databases: []vectorDatabase{
		{Plugin: "roca-corpus", Database: "corpus", Path: "roca-corpus.db", Alias: "plugin_roca_corpus",
			Tables: []vectorTable{{Name: "articles", IDColumn: "id", TextColumns: []string{"title", "body"}, TimeColumns: []string{"id"},
				Chunking: &chunkingHints{MaxChars: intPointer(24), OverlapChars: intPointer(4)}}}},
		{Plugin: "roca-ops", Database: "ops", Path: "roca-ops.db", Alias: "plugin_roca_ops",
			Tables: []vectorTable{{Name: "memories", IDColumn: "id", TextColumns: []string{"content"}, TimeColumns: []string{"id"}}}},
	}, Routes: []vectorRoute{
		{Plugin: "roca-corpus", Database: "corpus", Alias: "plugin_roca_corpus", Source: "plugin:roca-corpus"},
		{Plugin: "roca-ops", Database: "ops", Alias: "plugin_roca_ops", Source: "plugin:roca-ops"},
		{Plugin: "roca-cron", Database: "cron", Alias: "plugin_roca_cron", Source: "plugin:roca-cron"},
	}})

	runner := sqliteExecRunner(t, map[string]string{
		"plugin_roca_corpus": corpusPath,
		"plugin_roca_ops":    opsPath,
	})
	runner = databaseScopeRunner(runner, []DatabaseSelection{
		{Source: "core", Database: "core"},
		{Source: "plugin:roca-corpus", Database: "corpus"},
		{Source: "plugin:roca-ops", Database: "ops"},
		{Source: "plugin:roca-cron", Database: "cron"},
	})
	embedder := &recordingEmbedder{}
	federation, err := LoadFederation(CoreCLI{Executable: "roca", Run: runner}, root,
		DefaultModel, "v-test", embedder, nil)
	if err != nil {
		t.Fatal(err)
	}
	return federation, corpusPath, opsPath, embedder
}

func databaseScopeRunner(next CommandRunner, attached []DatabaseSelection) CommandRunner {
	return func(ctx context.Context, executable string, args ...string) ([]byte, error) {
		command := -1
		for index, argument := range args {
			if argument == "_database-scope" {
				command = index
				break
			}
		}
		if command == -1 {
			return next(ctx, executable, args...)
		}
		raw := ""
		for index := command + 1; index+1 < len(args); index++ {
			if args[index] == "--databases" {
				raw = args[index+1]
				break
			}
		}
		selected := []DatabaseSelection{}
		switch strings.TrimSpace(raw) {
		case "", "all":
			selected = append(selected, attached...)
		default:
			for _, name := range strings.Split(raw, ",") {
				name = strings.TrimSpace(name)
				matched := false
				for _, database := range attached {
					if database.Database != name && database.Source != name {
						continue
					}
					selected = append(selected, database)
					matched = true
					break
				}
				if !matched {
					available := make([]string, 0, len(attached))
					for _, database := range attached {
						if !containsString(available, database.Database) {
							available = append(available, database.Database)
						}
					}
					return nil, fmt.Errorf("unknown database %q; attached databases: %s",
						name, strings.Join(available, ", "))
				}
			}
		}
		databases := make([]string, 0, len(selected))
		for _, database := range selected {
			databases = append(databases, database.Database)
		}
		return json.Marshal(DatabaseScope{Databases: databases, Selected: selected})
	}
}

func writeRegistry(t *testing.T, root string, registry vectorRegistry) {
	t.Helper()
	raw, err := json.Marshal(registry)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, vectorRegistryFilename), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func createSourceDatabase(t *testing.T, path, schema string) {
	t.Helper()
	db := openTestSQLite(t, path)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

func mutateSourceDatabase(t *testing.T, path, statement string) {
	t.Helper()
	db := openTestSQLite(t, path)
	if _, err := db.Exec(statement); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

func sqliteExecRunner(t *testing.T, databases map[string]string) CommandRunner {
	t.Helper()
	db := openTestSQLite(t, ":memory:")
	t.Cleanup(func() { _ = db.Close() })
	for alias, path := range databases {
		if _, err := db.Exec(`ATTACH DATABASE ? AS `+quoteIdentifier(alias), path); err != nil {
			t.Fatal(err)
		}
	}
	return func(_ context.Context, _ string, args ...string) ([]byte, error) {
		statement := args[len(args)-1]
		rows, err := db.Query(statement)
		if err != nil {
			return nil, fmt.Errorf("query %q: %w", statement, err)
		}
		defer rows.Close()
		columns, err := rows.Columns()
		if err != nil {
			return nil, err
		}
		result := make([]map[string]any, 0)
		for rows.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for index := range values {
				pointers[index] = &values[index]
			}
			if err := rows.Scan(pointers...); err != nil {
				return nil, err
			}
			item := make(map[string]any, len(columns))
			for index, column := range columns {
				item[column] = values[index]
			}
			result = append(result, item)
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return json.Marshal(map[string]any{"rows": result})
	}
}

func sidecarMeta(t *testing.T, path string) map[string]string {
	t.Helper()
	db := openTestSQLite(t, path)
	defer db.Close()
	rows, err := db.Query(`SELECT key,value FROM meta`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	result := map[string]string{}
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			t.Fatal(err)
		}
		result[key] = value
	}
	return result
}

func openTestSQLite(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	return db
}

func flattenInputs(batches [][]string) []string {
	var result []string
	for _, batch := range batches {
		result = append(result, batch...)
	}
	return result
}

func intPointer(value int) *int { return &value }
