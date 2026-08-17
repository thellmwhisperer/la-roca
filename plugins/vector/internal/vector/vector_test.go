package vector

import (
	"context"
	"database/sql"
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
	if results[0].Source != "memories" || results[0].Text != "alpha memory" {
		t.Fatalf("first result = %+v", results[0])
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

func TestTargetedSessionDeltaInvalidatesOldTextOnce(t *testing.T) {
	ctx := context.Background()
	const clean = "Public health research\nhealth-project"
	corpus := &memoryCorpus{sources: []sourceRow{
		{kind: "sessions", sessionID: "session-clean", text: clean},
		{kind: "exchanges", sessionID: "session-clean", ordinal: 1, hasOrdinal: true,
			text: "A useful question\n\nA useful answer"},
	}}
	vectorPath := filepath.Join(t.TempDir(), "vector.db")
	embedder := &recordingEmbedder{}
	index := Index{Corpus: corpus, VectorPath: vectorPath, Model: DefaultModel, Embedder: embedder}
	if delta, err := index.Ingest(ctx); err != nil || delta.Added != 2 {
		t.Fatalf("initial delta = %+v, err=%v", delta, err)
	}

	db, err := sql.Open("sqlite", vectorPath)
	if err != nil {
		t.Fatal(err)
	}
	contaminated := clean + `\n{"source_exchange_fingerprints":["0123456789abcdef0123456789abcdef"]}`
	if _, err := db.Exec(`UPDATE chunks SET fingerprint=? WHERE source_kind='sessions'`, fingerprint(contaminated)); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	embedder.inputs = nil
	changed, err := index.IngestSource(ctx, "sessions")
	if err != nil {
		t.Fatal(err)
	}
	if changed.Updated != 1 || changed.Added != 0 || changed.Removed != 0 ||
		changed.Unchanged != 0 || changed.Sources != 1 || changed.Chunks != 1 {
		t.Fatalf("targeted session delta = %+v", changed)
	}
	if len(embedder.inputs) != 1 || len(embedder.inputs[0]) != 1 ||
		embedder.inputs[0][0] != DocumentPrefix+clean {
		t.Fatalf("targeted embedding inputs = %q", embedder.inputs)
	}

	observer, err := sql.Open("sqlite", vectorPath)
	if err != nil {
		t.Fatal(err)
	}
	defer observer.Close()
	var dataVersionBefore int
	if err := observer.QueryRow(`PRAGMA data_version`).Scan(&dataVersionBefore); err != nil {
		t.Fatal(err)
	}

	steady, err := index.IngestSource(ctx, "sessions")
	if err != nil {
		t.Fatal(err)
	}
	if steady.Updated != 0 || steady.Unchanged != 1 || steady.Sources != 1 || len(embedder.inputs) != 1 {
		t.Fatalf("repeated targeted delta = %+v, embedding batches=%d", steady, len(embedder.inputs))
	}
	var dataVersionAfter int
	if err := observer.QueryRow(`PRAGMA data_version`).Scan(&dataVersionAfter); err != nil {
		t.Fatal(err)
	}
	if dataVersionAfter != dataVersionBefore {
		t.Fatalf("repeated targeted delta changed database version from %d to %d", dataVersionBefore, dataVersionAfter)
	}
	if _, err := index.IngestSource(ctx, "unknown"); err == nil || !strings.Contains(err.Error(), "unknown vector source") {
		t.Fatalf("unknown source error = %v", err)
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

func (m *memoryCorpus) WalkSources(_ context.Context, sourceKind string, visit func(sourceRow) error) error {
	for _, source := range m.sources {
		if sourceKind != "" && source.kind != sourceKind {
			continue
		}
		if err := visit(source); err != nil {
			return err
		}
	}
	return nil
}

func (m *memoryCorpus) ResolveSource(_ context.Context, kind string, where locator) (string, error) {
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
		{kind: "sessions", sessionID: "s1", text: "gamma session\nsynthetic-project"},
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
