package vector

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

type recordingEmbedder struct {
	inputs [][]string
}

type compactFixtureEmbedder struct {
	calls int
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

func (e *compactFixtureEmbedder) Pull(context.Context, string) error { return nil }

func (e *compactFixtureEmbedder) Embed(_ context.Context, _ string, input []string) ([][]float32, error) {
	e.calls++
	vectors := make([][]float32, len(input))
	for index, text := range input {
		var ordinal int
		if _, err := fmt.Sscanf(text, DocumentPrefix+"synthetic record %d", &ordinal); err == nil {
			vectors[index] = []float32{1, float32(ordinal+1) / 2300, 0, 0, 0, 0, 0, 0}
		} else {
			vectors[index] = []float32{1, 0, 0, 0, 0, 0, 0, 0}
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
	if got, want := memory.stableID(), "memories/session/session%2Fa/3/"+memory.identity(); got != want {
		t.Fatalf("memory stable id = %q, want %q", got, want)
	}
	exchange := sourceRow{kind: "exchanges", sessionID: "session/a", ordinal: 7, hasOrdinal: true}
	if got, want := exchange.stableID(), "exchanges/session%2Fa/7/"+exchange.identity(); got != want {
		t.Fatalf("exchange stable id = %q, want %q", got, want)
	}
	session := sourceRow{kind: "sessions", sessionID: "session/a", text: "first title"}
	if got, want := session.stableID(), "sessions/session%2Fa/"+session.identity(); got != want {
		t.Fatalf("session stable id = %q, want %q", got, want)
	}
	thinking := sourceRow{kind: "thinking_blocks", sessionID: "session/a", ordinal: 7,
		hasOrdinal: true, position: "1.5", text: "first reasoning"}
	if got, want := thinking.stableID(), "thinking_blocks/session%2Fa/7/1.5/"+thinking.identity(); got != want {
		t.Fatalf("thinking stable id = %q, want %q", got, want)
	}
	thinkingSibling := thinking
	thinkingSibling.text = "second reasoning"
	if thinking.stableID() == thinkingSibling.stableID() {
		t.Fatalf("divergent thinking blocks sharing a locator have stable id %q", thinking.stableID())
	}
	for _, pair := range [][2]sourceRow{
		{session, {kind: "sessions", sessionID: "session/a", text: "second title"}},
		{exchange, {kind: "exchanges", sessionID: "session/a", ordinal: 7, hasOrdinal: true, text: "second exchange"}},
		{memory, {kind: "memories", sessionID: "session/a", ordinal: 3, hasOrdinal: true, text: "second memory"}},
	} {
		if pair[0].stableID() == pair[1].stableID() {
			t.Fatalf("divergent %s rows sharing a locator have stable id %q", pair[0].kind, pair[0].stableID())
		}
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

func TestDivergentFederatedVersionsStaySeparateAndIdempotent(t *testing.T) {
	sources := []sourceRow{
		{kind: "sessions", sessionID: "shared", text: "alpha session"},
		{kind: "sessions", sessionID: "shared", text: "beta session"},
		{kind: "exchanges", sessionID: "shared", ordinal: 1, hasOrdinal: true, text: "alpha exchange"},
		{kind: "exchanges", sessionID: "shared", ordinal: 1, hasOrdinal: true, text: "beta exchange"},
		{kind: "thinking_blocks", sessionID: "shared", ordinal: 1, hasOrdinal: true, position: "1", text: "alpha thinking"},
		{kind: "thinking_blocks", sessionID: "shared", ordinal: 1, hasOrdinal: true, position: "1", text: "beta thinking"},
		{kind: "memories", sessionID: "shared", ordinal: 1, hasOrdinal: true, layer: "discovery", origin: "agent", text: "alpha memory"},
		{kind: "memories", sessionID: "shared", ordinal: 1, hasOrdinal: true, layer: "discovery", origin: "agent", text: "beta memory"},
	}
	index := Index{Corpus: &memoryCorpus{sources: sources}, VectorPath: filepath.Join(t.TempDir(), "vector.db"),
		Model: DefaultModel, Embedder: &recordingEmbedder{}}
	first, err := index.Ingest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if first.Added != len(sources) || first.Chunks != len(sources) {
		t.Fatalf("first delta = %+v, want %d distinct chunks", first, len(sources))
	}
	second, err := index.Ingest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if second.Unchanged != len(sources) || second.Added != 0 || second.Updated != 0 || second.Removed != 0 {
		t.Fatalf("repeated delta = %+v", second)
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
	if changed.Updated != 0 || changed.Removed != 4 || changed.Added != 1 {
		t.Fatalf("changed delta = %+v", changed)
	}
	assertVectorStoreHasNoCorpusText(t, vectorPath)
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
	if err == nil {
		_, err = db.Exec(`DELETE FROM meta WHERE key=?`, deprecatedLayerReconciliationKey)
	}
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
		Model: DefaultModel, Embedder: &recordingEmbedder{}, Database: "corpus",
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
		if result.Database != "corpus" || result.Table == "" || result.ID == "" {
			t.Fatalf("legacy result has incomplete provenance: %+v", result)
		}
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

func TestNearestCandidateLimitRefillsForRetiredChunks(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE chunks(source_kind TEXT, locator TEXT)`); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 30; index++ {
		if _, err := db.Exec(`INSERT INTO chunks(source_kind,locator) VALUES ('exchanges','{}')`); err != nil {
			t.Fatal(err)
		}
	}
	for index := 0; index < 5; index++ {
		if _, err := db.Exec(`INSERT INTO chunks(source_kind,locator) VALUES ('memories',?)`, `{"layer":"rocodata_legacy"}`); err != nil {
			t.Fatal(err)
		}
	}
	limit, err := nearestCandidateLimit(context.Background(), db, 2)
	if err != nil {
		t.Fatal(err)
	}
	if limit != 21 {
		t.Fatalf("candidate limit = %d, want 21", limit)
	}
}

func TestCompactRebuildsDenseEquivalentStoreAndRefusesAnActiveIngest(t *testing.T) {
	t.Run("dense equivalent store", func(t *testing.T) {
		ctx := context.Background()
		sources := make([]sourceRow, 0, 2300)
		for index := range 2300 {
			text := fmt.Sprintf("synthetic record %04d", index)
			switch index % 4 {
			case 0:
				sources = append(sources, sourceRow{kind: "sessions",
					sessionID: fmt.Sprintf("synthetic-session-%04d", index), text: text})
			case 1:
				sources = append(sources, sourceRow{kind: "memories", text: text,
					layer: "synthetic", origin: "test", createdAt: fmt.Sprintf("2026-01-%02d", index%28+1)})
			case 2:
				sources = append(sources, sourceRow{kind: "exchanges", sessionID: "synthetic-session",
					ordinal: int64(index), hasOrdinal: true, text: text})
			case 3:
				sources = append(sources, sourceRow{kind: "thinking_blocks", sessionID: "synthetic-session",
					ordinal: int64(index), hasOrdinal: true, position: "1", text: text})
			}
		}
		corpus := &memoryCorpus{sources: sources}
		vectorPath := filepath.Join(t.TempDir(), "vector.db")
		embedder := &compactFixtureEmbedder{}
		index := Index{Corpus: corpus, VectorPath: vectorPath, Model: DefaultModel, Embedder: embedder}
		if delta, err := index.Ingest(ctx); err != nil || delta.Added != len(sources) {
			t.Fatalf("initial delta = %+v, err=%v", delta, err)
		}
		kept := append([]sourceRow(nil), sources[:10]...)
		kept = append(kept, sources[2290:]...)
		corpus.sources = kept
		if delta, err := index.Ingest(ctx); err != nil || delta.Removed != 2280 ||
			delta.Unchanged != len(kept) || delta.Added != 0 || delta.Updated != 0 {
			t.Fatalf("churn delta = %+v, err=%v", delta, err)
		}
		kindsBefore := chunkCountsByKind(t, vectorPath)
		resultsBefore, err := index.Query(ctx, "synthetic compact query", 5)
		if err != nil {
			t.Fatal(err)
		}
		embeddingCalls := embedder.calls

		report, err := Compact(ctx, vectorPath)
		if err != nil {
			t.Fatal(err)
		}
		if embedder.calls != embeddingCalls {
			t.Fatalf("compact made %d embedding calls", embedder.calls-embeddingCalls)
		}
		if report.LiveChunks != int64(len(kept)) || report.PagesBefore != 3 || report.PagesAfter != 1 {
			t.Fatalf("compact report = %+v", report)
		}
		if report.BytesReclaimed <= 0 || report.BytesAfter >= report.BytesBefore {
			t.Fatalf("compact did not reclaim bytes: %+v", report)
		}
		if kindsAfter := chunkCountsByKind(t, vectorPath); !maps.Equal(kindsBefore, kindsAfter) {
			t.Fatalf("kind counts changed from %v to %v", kindsBefore, kindsAfter)
		}
		db, err := sql.Open("sqlite", vectorPath)
		if err != nil {
			t.Fatal(err)
		}
		var integrity string
		if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&integrity); err != nil {
			db.Close()
			t.Fatal(err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		if integrity != "ok" {
			t.Fatalf("integrity check = %q", integrity)
		}
		resultsAfter, err := index.Query(ctx, "synthetic compact query", 5)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(resultsBefore, resultsAfter) {
			t.Fatalf("query changed from %+v to %+v", resultsBefore, resultsAfter)
		}
		embeddingCalls = embedder.calls
		steady, err := index.Ingest(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if steady.Added != 0 || steady.Updated != 0 || steady.Removed != 0 ||
			steady.Unchanged != len(kept) || embedder.calls != embeddingCalls {
			t.Fatalf("post-compact delta = %+v, embedding batches=%d", steady, embedder.calls-embeddingCalls)
		}
	})

	t.Run("active ingest lock", func(t *testing.T) {
		vectorPath := filepath.Join(t.TempDir(), "vector.db")
		index := Index{Corpus: &memoryCorpus{sources: []sourceRow{{
			kind: "sessions", sessionID: "synthetic-session", text: "alpha session",
		}}}, VectorPath: vectorPath, Model: DefaultModel, Embedder: &recordingEmbedder{}}
		if _, err := index.Ingest(context.Background()); err != nil {
			t.Fatal(err)
		}
		release, err := lockFile(vectorPath + ".index.lock")
		if err != nil {
			t.Fatal(err)
		}
		defer release()
		if _, err := Compact(context.Background(), vectorPath); err == nil ||
			!strings.Contains(err.Error(), "another ingest holds") {
			t.Fatalf("compact under active ingest lock = %v", err)
		}
	})

	t.Run("post-checkpoint byte baseline", func(t *testing.T) {
		vectorPath := filepath.Join(t.TempDir(), "vector.db")
		index := Index{Corpus: &memoryCorpus{sources: []sourceRow{{
			kind: "sessions", sessionID: "synthetic-session", text: "alpha session",
		}}}, VectorPath: vectorPath, Model: DefaultModel, Embedder: &recordingEmbedder{}}
		if _, err := index.Ingest(context.Background()); err != nil {
			t.Fatal(err)
		}
		db, err := sql.Open("sqlite", vectorPath)
		if err != nil {
			t.Fatal(err)
		}
		db.SetMaxOpenConns(1)
		defer db.Close()
		if _, err := db.Exec(`PRAGMA wal_autocheckpoint=0`); err != nil {
			t.Fatal(err)
		}
		tx, err := db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		for n := range 1024 {
			term := fmt.Sprintf("synthetic-wal-term-%04d-%s", n, strings.Repeat("x", 1024))
			if _, err := tx.Exec(`INSERT INTO meta(key,value) VALUES(?,?)`, term, term); err != nil {
				tx.Rollback()
				t.Fatal(err)
			}
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		stale, err := os.Stat(vectorPath)
		if err != nil {
			t.Fatal(err)
		}
		checkpointed, err := checkpointedSourceInfo(db, vectorPath)
		if err != nil {
			t.Fatal(err)
		}
		if checkpointed.Size() <= stale.Size() {
			t.Fatalf("checkpointed size = %d, pre-checkpoint size = %d", checkpointed.Size(), stale.Size())
		}
	})
}

func TestRetiredCensusTablesAreDroppedByIngestAndCompact(t *testing.T) {
	for _, operation := range []struct {
		name string
		run  func(context.Context, Index, string) error
	}{
		{
			name: "ingest",
			run: func(ctx context.Context, index Index, _ string) error {
				_, err := index.Ingest(ctx)
				return err
			},
		},
		{
			name: "compact",
			run: func(ctx context.Context, _ Index, path string) error {
				_, err := Compact(ctx, path)
				return err
			},
		},
	} {
		t.Run(operation.name, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "vector.db")
			index := Index{
				Corpus: &memoryCorpus{sources: []sourceRow{{
					kind: "sessions", sessionID: "synthetic-session", text: "retired census migration",
				}}},
				VectorPath: path, Model: DefaultModel, Embedder: &recordingEmbedder{},
			}
			if _, err := index.Ingest(ctx); err != nil {
				t.Fatal(err)
			}
			db, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`CREATE TABLE census(term TEXT PRIMARY KEY, docs INTEGER NOT NULL);
				CREATE TABLE census_totals(key TEXT PRIMARY KEY, documents INTEGER NOT NULL);`); err != nil {
				db.Close()
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}

			if err := operation.run(ctx, index, path); err != nil {
				t.Fatal(err)
			}
			db, err = sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			var retired int
			if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_schema
				WHERE type='table' AND name IN ('census','census_totals')`).Scan(&retired); err != nil {
				t.Fatal(err)
			}
			if retired != 0 {
				t.Fatalf("%s left %d retired census tables", operation.name, retired)
			}
			t.Logf("%s: retired census tables remaining = %d", operation.name, retired)
		})
	}
}

func chunkCountsByKind(t *testing.T, path string) map[string]int64 {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query(`SELECT source_kind,COUNT(*) FROM chunks GROUP BY source_kind`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	counts := map[string]int64{}
	for rows.Next() {
		var kind string
		var count int64
		if err := rows.Scan(&kind, &count); err != nil {
			t.Fatal(err)
		}
		counts[kind] = count
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return counts
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
