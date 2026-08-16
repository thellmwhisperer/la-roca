package vector

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

type recordingEmbedder struct {
	inputs [][]string
}

func (e *recordingEmbedder) Pull(context.Context, string) error { return nil }

func (e *recordingEmbedder) Embed(_ context.Context, _ string, input []string) ([][]float32, error) {
	e.inputs = append(e.inputs, append([]string(nil), input...))
	vectors := make([][]float32, len(input))
	for i, text := range input {
		text = strings.ToLower(text)
		switch {
		case strings.Contains(text, "alpha"):
			vectors[i] = []float32{1, 0, 0, 0, 0, 0, 0, 0}
		case strings.Contains(text, "beta"):
			vectors[i] = []float32{0, 1, 0, 0, 0, 0, 0, 0}
		default:
			vectors[i] = []float32{0, 0, 1, 0, 0, 0, 0, 0}
		}
	}
	return vectors, nil
}

func TestChunksOverlapWithoutRepeatingTerminalChunk(t *testing.T) {
	got := chunks("abcdefghij", 6, 2)
	want := []string{"abcdef", "efghij"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("chunks = %q, want %q", got, want)
	}
	unicode := chunks("a🙂bc", 3, 1)
	if strings.Join(unicode, "|") != "a🙂b|bc" {
		t.Fatalf("unicode chunks = %q", unicode)
	}
}

func TestStableSourceIDsUseCoreNaturalKeys(t *testing.T) {
	memory := sourceRow{kind: "memories", sessionID: "session/a", ordinal: 3, hasOrdinal: true}
	if got, want := memory.stableID(), "memories/session/session%2Fa/3"; got != want {
		t.Fatalf("memory stable id = %q, want %q", got, want)
	}
	exchange := sourceRow{kind: "exchanges", sessionID: "session/a", ordinal: 7, hasOrdinal: true}
	if got, want := exchange.stableID(), "exchanges/session%2Fa/7"; got != want {
		t.Fatalf("exchange stable id = %q, want %q", got, want)
	}
	thinking := sourceRow{kind: "thinking_blocks", sessionID: "session/a", ordinal: 7, hasOrdinal: true, position: "1.5"}
	if got, want := thinking.stableID(), "thinking_blocks/session%2Fa/7/1.5"; got != want {
		t.Fatalf("thinking stable id = %q, want %q", got, want)
	}
	unkeyed := sourceRow{kind: "thinking_blocks", sessionID: "session/a", text: "session reasoning"}
	sibling := sourceRow{kind: "thinking_blocks", sessionID: "session/a", text: "other session reasoning"}
	if unkeyed.stableID() == sibling.stableID() {
		t.Fatalf("un-numbered thinking blocks share stable id %q", unkeyed.stableID())
	}
	unsequenced := sourceRow{kind: "memories", sessionID: "session/a", text: "unsequenced"}
	if got := unsequenced.stableID(); !strings.HasPrefix(got, "memories/direct/") {
		t.Fatalf("memory without a sequence has stable id %q", got)
	}
	unnumbered := sourceRow{kind: "exchanges", sessionID: "session/a", text: "unnumbered"}
	if got := unnumbered.stableID(); !strings.HasPrefix(got, "exchanges/session%2Fa/unkeyed/") {
		t.Fatalf("exchange without a number has stable id %q", got)
	}
	direct := sourceRow{kind: "memories", text: "stored memory", layer: "discovery", origin: "human", createdAt: "2026-08-14"}
	if got := direct.stableID(); !strings.HasPrefix(got, "memories/direct/") || strings.Contains(got, "/id/") {
		t.Fatalf("direct memory stable id = %q", got)
	}
}

func TestDeltaIndexIsIdempotentAndMapsResultsBackToCore(t *testing.T) {
	ctx := context.Background()
	corpus := createCoreFixture(t)
	vectorPath := filepath.Join(t.TempDir(), "vector.db")
	embedder := &recordingEmbedder{}
	index := Index{Corpus: corpus, VectorPath: vectorPath, Model: DefaultModel, Embedder: embedder}

	first, err := index.Ingest(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if first.Added != 8 || first.Updated != 0 || first.Removed != 0 {
		t.Fatalf("first delta = %+v", first)
	}
	second, err := index.Ingest(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if second.Added != 0 || second.Updated != 0 || second.Removed != 0 || second.Unchanged != 8 {
		t.Fatalf("idempotent delta = %+v", second)
	}

	results, err := index.Query(ctx, "alpha topic", 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 {
		t.Fatalf("results = %d, want 3", len(results))
	}
	if results[0].Source != "memories" || results[0].Text != "alpha memory" ||
		results[0].Locator.Layer != "discovery" || results[0].Locator.Identity == "" {
		t.Fatalf("first result = %+v", results[0])
	}
	encoded, err := json.Marshal(results[0])
	if err != nil || !strings.Contains(string(encoded), `"locator"`) {
		t.Fatalf("result JSON does not expose its locator: %s", encoded)
	}
	if math.Abs(results[0].Score-1) > 0.0001 {
		t.Fatalf("first score = %f, want 1", results[0].Score)
	}
	if !strings.HasPrefix(embedder.inputs[len(embedder.inputs)-1][0], QueryPrefix) {
		t.Fatalf("query was not embedded with %q: %q", QueryPrefix, embedder.inputs[len(embedder.inputs)-1][0])
	}

	kept := corpus.sources[:0]
	for _, source := range corpus.sources {
		if source.kind == "exchanges" && source.ordinal == 2 {
			source.text = "alpha changed"
		}
		if source.kind != "thinking_blocks" {
			kept = append(kept, source)
		}
	}
	corpus.sources = kept

	changed, err := index.Ingest(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if changed.Updated != 1 || changed.Removed != 3 || changed.Added != 0 {
		t.Fatalf("changed delta = %+v", changed)
	}
	assertVectorStoreHasNoCorpusText(t, vectorPath)
}

func TestIngestFlushesStagedSourcesWhenWalkFails(t *testing.T) {
	sources := make([]sourceRow, defaultBatchSize/2)
	for index := range sources {
		sources[index] = sourceRow{kind: "memories", text: fmt.Sprintf("synthetic progress %d", index),
			layer: "discovery", origin: "agent", createdAt: "2026-01-01"}
	}
	path := filepath.Join(t.TempDir(), "vector.db")
	index := Index{Corpus: &failingCorpus{sources: sources}, VectorPath: path,
		Model: DefaultModel, Embedder: &recordingEmbedder{}}
	if _, err := index.Ingest(context.Background()); err == nil {
		t.Fatal("walk failure was not returned")
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM chunks`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != len(sources) {
		t.Fatalf("completed chunks = %d, want %d", count, len(sources))
	}
}

func TestIngestRetainsMissingChunksUntilWalkCompletes(t *testing.T) {
	longText := strings.Repeat("alpha old ", defaultChunkSize/4)
	initial := sourceRow{kind: "exchanges", sessionID: "synthetic-session", ordinal: 1, hasOrdinal: true, text: longText}
	path := filepath.Join(t.TempDir(), "vector.db")
	index := Index{Corpus: &memoryCorpus{sources: []sourceRow{initial}}, VectorPath: path,
		Model: DefaultModel, Embedder: &recordingEmbedder{}}
	if _, err := index.Ingest(context.Background()); err != nil {
		t.Fatal(err)
	}

	changed := initial
	changed.text = "alpha short"
	index.Corpus = &failingCorpus{sources: []sourceRow{changed}}
	if _, err := index.Ingest(context.Background()); err == nil {
		t.Fatal("walk failure was not returned")
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM chunks`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != len(chunks(longText, defaultChunkSize, defaultOverlap)) {
		t.Fatalf("chunks after interrupted replacement = %d, want %d", count, len(chunks(longText, defaultChunkSize, defaultOverlap)))
	}
}

func TestIngestRetriesTheInFlightSourceAfterEmbeddingFailure(t *testing.T) {
	sources := make([]sourceRow, defaultBatchSize)
	for index := range sources[:defaultBatchSize-1] {
		sources[index] = sourceRow{kind: "memories", text: fmt.Sprintf("synthetic progress %d", index),
			layer: "discovery", origin: "agent", createdAt: "2026-01-01"}
	}
	sources[defaultBatchSize-1] = sourceRow{kind: "memories", text: strings.Repeat("alpha current ", 17000),
		layer: "discovery", origin: "agent", createdAt: "2026-01-01"}
	embedder := &failOnceEmbedder{}
	path := filepath.Join(t.TempDir(), "vector.db")
	index := Index{Corpus: &failingCorpus{sources: sources}, VectorPath: path,
		Model: DefaultModel, Embedder: embedder}
	if _, err := index.Ingest(context.Background()); err == nil {
		t.Fatal("walk failure was not returned")
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM chunks`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	want := defaultBatchSize - 1 + len(chunks(sources[defaultBatchSize-1].text, defaultChunkSize, defaultOverlap))
	if count != want {
		t.Fatalf("chunks after source retry = %d, want %d", count, want)
	}
	if embedder.maxInput > defaultBatchSize+1 {
		t.Fatalf("retried embedding batch grew to %d inputs", embedder.maxInput)
	}
}

func TestDeltaRefreshesStaleLocatorWithoutReembedding(t *testing.T) {
	source := sourceRow{kind: "memories", text: "same canonical memory", layer: "discovery",
		origin: "agent", createdAt: "2026-01-01", cronSource: "synthetic-agent", filePath: "memory.md"}
	corpus := &memoryCorpus{sources: []sourceRow{source}}
	embedder := &recordingEmbedder{}
	path := filepath.Join(t.TempDir(), "vector.db")
	index := Index{Corpus: corpus, VectorPath: path, Model: DefaultModel, Embedder: embedder}
	if _, err := index.Ingest(context.Background()); err != nil {
		t.Fatal(err)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`UPDATE chunks SET locator=json_set(locator, '$.layer', 'rocodata_legacy') WHERE source_kind='memories' AND source_id=?`, source.stableID())
	if closeErr := db.Close(); err == nil && closeErr != nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}

	embedCalls := len(embedder.inputs)
	report, err := index.Ingest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.Added != 0 || report.Updated != 1 || report.Unchanged != 0 || report.Removed != 0 {
		t.Fatalf("locator refresh delta = %+v", report)
	}
	if len(embedder.inputs) != embedCalls {
		t.Fatalf("locator refresh re-embedded %d batches", len(embedder.inputs)-embedCalls)
	}

	readOnly := index
	readOnly.ReadOnly = true
	results, err := readOnly.Query(context.Background(), "canonical memory", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Locator.Layer != "discovery" {
		t.Fatalf("refreshed result = %+v", results)
	}
}

func TestDuplicateSourcesKeepTheFinalLocatorForChangedText(t *testing.T) {
	initial := sourceRow{kind: "memories", text: "alpha old canonical", layer: "discovery",
		origin: "agent", createdAt: "2026-01-01", cronSource: "synthetic-agent", filePath: "memory.md"}
	corpus := &memoryCorpus{sources: []sourceRow{initial}}
	index := Index{Corpus: corpus, VectorPath: filepath.Join(t.TempDir(), "vector.db"),
		Model: DefaultModel, Embedder: &recordingEmbedder{}}
	if _, err := index.Ingest(context.Background()); err != nil {
		t.Fatal(err)
	}

	corpus.sources = []sourceRow{
		{kind: "memories", text: "alpha old canonical", layer: "handoff", origin: "agent",
			createdAt: "2026-01-01", cronSource: "synthetic-agent", filePath: "memory.md"},
		{kind: "memories", text: "alpha new canonical", layer: "discovery", origin: "agent",
			createdAt: "2026-01-01", cronSource: "synthetic-agent", filePath: "memory.md"},
	}
	if _, err := index.Ingest(context.Background()); err != nil {
		t.Fatal(err)
	}

	results, err := index.Query(context.Background(), "alpha new canonical", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Locator.Layer != "discovery" || results[0].Text != "alpha new canonical" {
		t.Fatalf("duplicate-source result = %+v", results)
	}
}

func TestDuplicateSourcesCoalesceBeforeDiffing(t *testing.T) {
	t.Run("later persisted row wins", func(t *testing.T) {
		initial := sourceRow{kind: "memories", text: "alpha old canonical", layer: "discovery",
			origin: "agent", createdAt: "2026-01-01", cronSource: "synthetic-agent", filePath: "memory.md"}
		corpus := &memoryCorpus{sources: []sourceRow{initial}}
		index := Index{Corpus: corpus, VectorPath: filepath.Join(t.TempDir(), "vector.db"),
			Model: DefaultModel, Embedder: &recordingEmbedder{}}
		if _, err := index.Ingest(context.Background()); err != nil {
			t.Fatal(err)
		}

		corpus.sources = []sourceRow{
			{kind: "memories", text: "alpha new canonical", layer: "handoff", origin: "agent",
				createdAt: "2026-01-01", cronSource: "synthetic-agent", filePath: "memory.md"},
			initial,
		}
		if _, err := index.Ingest(context.Background()); err != nil {
			t.Fatal(err)
		}

		results, err := index.Query(context.Background(), "alpha old canonical", 1)
		if err != nil {
			t.Fatal(err)
		}
		if len(results) != 1 || results[0].Locator.Layer != "discovery" || results[0].Text != initial.text {
			t.Fatalf("coalesced result = %+v", results)
		}
	})

	t.Run("later shorter row removes tail", func(t *testing.T) {
		longText := strings.Repeat("alpha old ", defaultChunkSize/4)
		initial := sourceRow{kind: "memories", text: longText, layer: "discovery",
			origin: "agent", createdAt: "2026-01-01", cronSource: "synthetic-agent", filePath: "memory.md"}
		corpus := &memoryCorpus{sources: []sourceRow{initial}}
		index := Index{Corpus: corpus, VectorPath: filepath.Join(t.TempDir(), "vector.db"),
			Model: DefaultModel, Embedder: &recordingEmbedder{}}
		if _, err := index.Ingest(context.Background()); err != nil {
			t.Fatal(err)
		}

		corpus.sources = []sourceRow{
			{kind: "memories", text: strings.Repeat("alpha changed ", defaultChunkSize/4), layer: "handoff",
				origin: "agent", createdAt: "2026-01-01", cronSource: "synthetic-agent", filePath: "memory.md"},
			{kind: "memories", text: "alpha short", layer: "discovery", origin: "agent",
				createdAt: "2026-01-01", cronSource: "synthetic-agent", filePath: "memory.md"},
		}
		report, err := index.Ingest(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if report.Updated != 1 || report.Removed != len(chunks(longText, defaultChunkSize, defaultOverlap))-1 {
			t.Fatalf("shorter duplicate delta = %+v", report)
		}
	})
}

func TestDeprecatedMemoryLayersStayOutOfTheIndexAndResults(t *testing.T) {
	corpus := createCoreFixture(t)
	deprecated := sourceRow{kind: "memories", text: "alpha deprecated memory", layer: "RocoData_legacy",
		origin: "agent", createdAt: "2026-08-14"}
	corpus.sources = append(corpus.sources, deprecated)
	path := filepath.Join(t.TempDir(), "vector.db")
	index := Index{Corpus: corpus, VectorPath: path, Model: DefaultModel, Embedder: &recordingEmbedder{}}
	report, err := index.Ingest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.Added != 8 || report.Sources != 8 {
		t.Fatalf("deprecated memory entered the index: %+v", report)
	}

	active := sourceRow{kind: "memories", text: "alpha memory", layer: "discovery", origin: "agent", createdAt: "2026-01-01"}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`UPDATE chunks SET locator=json_set(locator, '$.layer', 'rocodata_legacy') WHERE source_kind='memories' AND source_id=?`, active.stableID())
	if closeErr := db.Close(); err == nil && closeErr != nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}

	results, err := index.Query(context.Background(), "alpha", 20)
	if err != nil {
		t.Fatal(err)
	}
	for _, result := range results {
		if result.SourceID == deprecated.stableID() || deprecatedMemoryLayer(result.Locator.Layer) {
			t.Fatalf("deprecated memory returned: %+v", result)
		}
	}
	db, err = sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	var retired int
	err = db.QueryRow(`SELECT COUNT(*) FROM chunks WHERE source_kind='memories' AND lower(COALESCE(json_extract(locator,'$.layer'),'')) LIKE 'rocodata\_%' ESCAPE '\'`).Scan(&retired)
	if closeErr := db.Close(); err == nil && closeErr != nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}
	if retired != 0 {
		t.Fatalf("retired chunks left after query reconciliation: %d", retired)
	}
}

func TestReadOnlyQueryLeavesRetiredChunksUntouched(t *testing.T) {
	corpus := createCoreFixture(t)
	path := filepath.Join(t.TempDir(), "vector.db")
	index := Index{Corpus: corpus, VectorPath: path, Model: DefaultModel, Embedder: &recordingEmbedder{}}
	if _, err := index.Ingest(context.Background()); err != nil {
		t.Fatal(err)
	}
	readOnly := index
	readOnly.ReadOnly = true
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM meta WHERE key=?`, deprecatedLayerReconciliationKey); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := readOnly.Query(context.Background(), "alpha", 5); err != nil {
		t.Fatalf("clean unreconciled read-only query: %v", err)
	}

	db, err = sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	active := sourceRow{kind: "memories", text: "alpha memory", layer: "discovery", origin: "agent", createdAt: "2026-01-01"}
	if _, err := db.Exec(`UPDATE chunks SET locator=json_set(locator, '$.layer', 'rocodata_legacy') WHERE source_kind='memories' AND source_id=?`, active.stableID()); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := readOnly.Query(context.Background(), "alpha", 5); err == nil ||
		!strings.Contains(err.Error(), "contains retired chunks") {
		t.Fatalf("read-only query error = %v", err)
	}
	db, err = sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	var retired int
	err = db.QueryRow(`SELECT COUNT(*) FROM chunks WHERE source_kind='memories' AND lower(COALESCE(json_extract(locator,'$.layer'),'')) LIKE 'rocodata\_%' ESCAPE '\'`).Scan(&retired)
	if closeErr := db.Close(); err == nil && closeErr != nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}
	if retired != 1 {
		t.Fatalf("read-only query changed retired chunks: %d", retired)
	}
}

func TestSourcesWithoutNaturalKeysStaySeparateAndResolve(t *testing.T) {
	ctx := context.Background()
	index := Index{Corpus: createCoreFixture(t), VectorPath: filepath.Join(t.TempDir(), "vector.db"),
		Model: DefaultModel, Embedder: &recordingEmbedder{}}
	if _, err := index.Ingest(ctx); err != nil {
		t.Fatal(err)
	}
	results, err := index.Query(ctx, "unkeyed sources", 20)
	if err != nil {
		t.Fatal(err)
	}
	resolved := map[string]bool{}
	for _, result := range results {
		resolved[result.Text] = true
	}
	for _, want := range []string{"delta session reasoning", "zeta session reasoning",
		"epsilon unsequenced memory", "eta question\n\neta answer"} {
		if !resolved[want] {
			t.Fatalf("source %q was indexed but never resolved: %+v", want, results)
		}
	}
}

func TestQueryDeduplicatesChunksByStableSource(t *testing.T) {
	corpus := createCoreFixture(t)
	long := strings.Repeat("alpha ", defaultChunkSize/3)
	for index := range corpus.sources {
		if corpus.sources[index].kind == "memories" && corpus.sources[index].text == "alpha memory" {
			corpus.sources[index].text = long
		}
	}
	stale := sourceRow{kind: "memories", text: strings.Repeat("alpha stale ", defaultChunkSize/4),
		layer: "discovery", origin: "agent", createdAt: "2026-01-02", cronSource: "synthetic-agent",
		filePath: "stale.md"}
	corpus.sources = append(corpus.sources, stale)
	index := Index{
		Corpus: corpus, VectorPath: filepath.Join(t.TempDir(), "vector.db"),
		Model: DefaultModel, Embedder: &recordingEmbedder{},
	}
	if _, err := index.Ingest(context.Background()); err != nil {
		t.Fatal(err)
	}
	corpus.sources = corpus.sources[:len(corpus.sources)-1]
	results, err := index.Query(context.Background(), "alpha", 20)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, result := range results {
		if seen[result.SourceID] {
			t.Fatalf("source %q returned more than once", result.SourceID)
		}
		seen[result.SourceID] = true
	}
	if calls := corpus.resolves[stale.locator().Identity]; calls != 1 {
		t.Fatalf("source that no longer resolves was resolved %d times, want 1", calls)
	}
}

func TestQueryStopsWalkingAnIndexTheCorpusMovedUnder(t *testing.T) {
	corpus := createCoreFixture(t)
	for ordinal := 0; ordinal < 200; ordinal++ {
		corpus.sources = append(corpus.sources, sourceRow{kind: "exchanges", sessionID: "s2",
			ordinal: int64(ordinal), hasOrdinal: true,
			text: fmt.Sprintf("alpha question %d\n\nalpha answer %d", ordinal, ordinal)})
	}
	index := Index{
		Corpus: corpus, VectorPath: filepath.Join(t.TempDir(), "vector.db"),
		Model: DefaultModel, Embedder: &recordingEmbedder{},
	}
	if _, err := index.Ingest(context.Background()); err != nil {
		t.Fatal(err)
	}
	corpus.sources = nil
	corpus.resolves = map[string]int{}
	results, err := index.Query(context.Background(), "alpha", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Fatalf("an index nothing resolves under answered %d results", len(results))
	}
	if len(corpus.resolves) > maxUnresolvedCandidates {
		t.Fatalf("stale index cost %d resolutions, want at most %d",
			len(corpus.resolves), maxUnresolvedCandidates)
	}
}

type memoryCorpus struct {
	sources  []sourceRow
	resolves map[string]int
}

type failingCorpus struct {
	sources []sourceRow
}

type failOnceEmbedder struct {
	recordingEmbedder
	failed bool
	maxInput int
}

func (e *failOnceEmbedder) Pull(context.Context, string) error { return nil }

func (e *failOnceEmbedder) Embed(ctx context.Context, model string, input []string) ([][]float32, error) {
	if len(input) > e.maxInput {
		e.maxInput = len(input)
	}
	if !e.failed {
		e.failed = true
		return nil, fmt.Errorf("synthetic embedding failure")
	}
	return e.recordingEmbedder.Embed(ctx, model, input)
}

func (f *failingCorpus) WalkSources(_ context.Context, visit func(sourceRow) error) error {
	for _, source := range f.sources {
		if err := visit(source); err != nil {
			return err
		}
	}
	return fmt.Errorf("synthetic walk failure")
}

func (f *failingCorpus) ResolveSource(_ context.Context, kind string, where Locator) (string, error) {
	for _, source := range f.sources {
		if source.kind == kind && source.locator().Identity == where.Identity {
			return source.text, nil
		}
	}
	return "", nil
}

func (m *memoryCorpus) WalkSources(_ context.Context, visit func(sourceRow) error) error {
	for _, source := range m.sources {
		if err := visit(source); err != nil {
			return err
		}
	}
	return nil
}

func (m *memoryCorpus) ResolveSource(_ context.Context, kind string, where Locator) (string, error) {
	if m.resolves == nil {
		m.resolves = map[string]int{}
	}
	m.resolves[where.Identity]++
	for _, source := range m.sources {
		if source.kind == kind && source.locator().Identity == where.Identity {
			return source.text, nil
		}
	}
	return "", nil
}

func createCoreFixture(t *testing.T) *memoryCorpus {
	t.Helper()
	return &memoryCorpus{sources: []sourceRow{
		{kind: "sessions", sessionID: "s1", text: "gamma session\nsynthetic-project\n{}"},
		{kind: "memories", text: "alpha memory", layer: "discovery", origin: "agent", createdAt: "2026-01-01", cronSource: "synthetic-agent"},
		{kind: "memories", text: "epsilon unsequenced memory", sessionID: "s1", layer: "discovery", origin: "agent", createdAt: "2026-01-01", cronSource: "synthetic-agent"},
		{kind: "exchanges", sessionID: "s1", ordinal: 2, hasOrdinal: true, text: "beta question\n\ngamma answer"},
		{kind: "exchanges", sessionID: "s1", text: "eta question\n\neta answer"},
		{kind: "thinking_blocks", sessionID: "s1", ordinal: 2, hasOrdinal: true, position: "1.5", text: "beta reasoning"},
		{kind: "thinking_blocks", sessionID: "s1", text: "delta session reasoning"},
		{kind: "thinking_blocks", sessionID: "s1", text: "zeta session reasoning"},
	}}
}

func assertVectorStoreHasNoCorpusText(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query("PRAGMA table_info(chunks)")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notnull, pk int
		var name, kind string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &kind, &notnull, &defaultValue, &pk); err != nil {
			t.Fatal(err)
		}
		if name == "text" || name == "content" {
			t.Fatalf("vector store duplicates corpus in column %q", name)
		}
	}
}
