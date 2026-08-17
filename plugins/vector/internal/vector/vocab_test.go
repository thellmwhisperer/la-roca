package vector

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"testing"
)

func TestVocabTermsFoldAccentsCaseAndSeparators(t *testing.T) {
	cases := []struct {
		name string
		text string
		want []string
	}{
		{"accents fold", "Camión médico SALUD", []string{"camion", "medico", "salud"}},
		{"punctuation separates", "naïve—café; scotland's datasets", []string{"naive", "cafe", "scotland", "datasets"}},
		{"single runes drop", "a I o", nil},
		{"digits are terms", "worktree22 2026 b12", []string{"worktree22", "2026", "b12"}},
		{"opaque decimal identifiers drop", "421867 12345 health", []string{"health"}},
		{"hexadecimal tokens drop", "deadbeef 0123456789abcdef defaced", []string{"defaced"}},
		{"serialized keys and structural identifiers drop",
			`{"source_exchange_fingerprints":["deadbeef"],"health_status":"stable","id":"123e4567-e89b-12d3-a456-426614174000"} clinical`,
			[]string{"stable", "clinical"}},
		{"empty", "", nil},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := vocabTerms(testCase.text)
			if strings.Join(got, "|") != strings.Join(testCase.want, "|") {
				t.Fatalf("vocabTerms(%q) = %q, want %q", testCase.text, got, testCase.want)
			}
		})
	}
}

// vocabEmbedder routes by marker word so a query on "salud" lands halfway
// between the public-health family and the personal-health family, and the
// workshop floor is orthogonal to both.
type vocabEmbedder struct{}

func (vocabEmbedder) Pull(context.Context, string) error { return nil }

func (vocabEmbedder) Embed(_ context.Context, _ string, input []string) ([][]float32, error) {
	vectors := make([][]float32, len(input))
	for index, text := range input {
		switch {
		case strings.Contains(text, "scotland"):
			vectors[index] = []float32{1, 0, 0, 0, 0, 0, 0, 0}
		case strings.Contains(text, "cancer"):
			vectors[index] = []float32{0, 1, 0, 0, 0, 0, 0, 0}
		case strings.Contains(text, "salud"):
			vectors[index] = []float32{1, 1, 0, 0, 0, 0, 0, 0}
		default:
			vectors[index] = []float32{0, 0, 1, 0, 0, 0, 0, 0}
		}
	}
	return vectors, nil
}

// saludCorpus mirrors the recorded field evidence: one corpus where public
// health and personal health are two separate worlds, both surrounded by a
// loud workshop floor and by scotland-flavoured memories and sessions that
// vocabulary discovery must not count.
func saludCorpus() *memoryCorpus {
	sources := []sourceRow{}
	for index := 0; index < 60; index++ {
		text := "scotland public health"
		if index%2 == 0 {
			text += " datasets"
		}
		if index%4 == 0 {
			text += " hospital"
		}
		if index%8 == 0 {
			text += " prescribing"
		}
		if index%16 == 0 {
			text += " statistics"
		}
		if index%10 == 0 {
			text += " standup"
		}
		if index%12 == 0 {
			text += " worktree"
		}
		sources = append(sources, sourceRow{kind: "exchanges", sessionID: "public-session",
			ordinal: int64(index), hasOrdinal: true, text: text})
	}
	for index := 0; index < 40; index++ {
		text := "cancer"
		if index%2 == 0 {
			text += " colesterol"
		} else {
			text += " grasas"
		}
		if index%4 == 0 {
			text += " enfermedad"
		}
		if index%8 == 0 {
			text += " sangre"
		}
		if index%16 == 0 {
			text += " medico"
		}
		kind, session := "exchanges", "personal-session"
		if index >= 30 {
			kind, session = "thinking_blocks", "personal-thinking"
		}
		source := sourceRow{kind: kind, sessionID: session, ordinal: int64(index), hasOrdinal: true, text: text}
		if kind == "thinking_blocks" {
			source.position = "1"
		}
		sources = append(sources, source)
	}
	for index := 0; index < 200; index++ {
		kind, session := "exchanges", "noise-session"
		if index >= 150 {
			kind, session = "thinking_blocks", "noise-thinking"
		}
		source := sourceRow{kind: kind, sessionID: session, ordinal: int64(index), hasOrdinal: true,
			text: fmt.Sprintf("standup worktree exchange semantic projects filler %d", index)}
		if kind == "thinking_blocks" {
			source.position = "1"
		}
		if index >= 180 {
			kind = "memories"
			source = sourceRow{kind: kind, text: fmt.Sprintf("standup worktree exchange semantic projects memory %d", index),
				layer: "discovery", origin: "agent", createdAt: "2026-08-17"}
		}
		sources = append(sources, source)
	}
	sources = append(sources,
		sourceRow{kind: "memories", text: "scotland public health memory decoy", layer: "discovery",
			origin: "agent", createdAt: "2026-08-17"},
		sourceRow{kind: "memories", text: "scotland datasets memory decoy", layer: "discovery",
			origin: "agent", createdAt: "2026-08-16"},
		sourceRow{kind: "sessions", sessionID: "decoy-session",
			text: "scotland public health session decoy\nsynthetic-project\n{}"},
	)
	return &memoryCorpus{sources: sources}
}

func TestVocabDiscoversTwoAvenuesAndPenalizesWorkshopNoise(t *testing.T) {
	ctx := context.Background()
	index := Index{Corpus: saludCorpus(), VectorPath: filepath.Join(t.TempDir(), "vector.db"),
		Model: DefaultModel, Embedder: vocabEmbedder{}}
	if _, err := index.Ingest(ctx); err != nil {
		t.Fatal(err)
	}
	report, err := index.Vocab(ctx, "salud")
	if err != nil {
		t.Fatal(err)
	}
	if report.Concept != "salud" || report.TopK != 100 {
		t.Fatalf("concept/top-k = %q/%d", report.Concept, report.TopK)
	}
	if report.Hits != 100 || report.HitsByKind["exchanges"] != 90 ||
		report.HitsByKind["thinking_blocks"] != 10 || len(report.HitsByKind) != 2 {
		t.Fatalf("hits = %d, by kind = %v", report.Hits, report.HitsByKind)
	}
	if report.CensusDocuments != 302 {
		t.Fatalf("census documents = %d, want 302 (sessions are not censused)", report.CensusDocuments)
	}
	publicAvenue := avenueContaining(t, report, "scotland")
	personalAvenue := avenueContaining(t, report, "colesterol")
	if publicAvenue == personalAvenue {
		t.Fatalf("scotland and colesterol share avenue %d", publicAvenue)
	}
	assertAvenueHas(t, report, publicAvenue, "prescribing", "hospital", "datasets")
	assertAvenueHas(t, report, personalAvenue, "grasas", "enfermedad")
	for _, noise := range []string{"standup", "worktree", "exchange", "semantic", "projects", "filler", "number"} {
		for _, avenue := range report.Avenues {
			for _, term := range avenue.Terms {
				if term.Term == noise {
					t.Fatalf("workshop term %q survived as an avenue (via %d, score %.2f)",
						noise, avenue.Rank, term.Score)
				}
			}
		}
	}
	previous := math.Inf(1)
	for _, term := range report.Avenues[0].Terms {
		if term.Score > previous+0.000001 {
			t.Fatalf("first avenue is not ranked by descending score: %+v", report.Avenues[0].Terms)
		}
		previous = term.Score
	}
	repeat, err := index.Vocab(ctx, "salud")
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprintf("%#v", repeat) != fmt.Sprintf("%#v", report) {
		t.Fatalf("vocab is not reproducible:\n%#v\n%#v", repeat, report)
	}
}

func TestVocabRanksEligibleSourcesBeforeApplyingTopK(t *testing.T) {
	sources := make([]sourceRow, 0, 1000)
	for index := 0; index < 900; index++ {
		sources = append(sources, sourceRow{kind: "memories", text: fmt.Sprintf("salud decoy %d", index),
			layer: "discovery", origin: "agent", createdAt: "2026-08-17"})
	}
	for index := 0; index < 100; index++ {
		sources = append(sources, sourceRow{kind: "exchanges", sessionID: "eligible",
			ordinal: int64(index), hasOrdinal: true, text: fmt.Sprintf("scotland topic %d", index)})
	}
	index := Index{Corpus: &memoryCorpus{sources: sources}, VectorPath: filepath.Join(t.TempDir(), "vector.db"),
		Model: DefaultModel, Embedder: vocabEmbedder{}}
	if _, err := index.Ingest(context.Background()); err != nil {
		t.Fatal(err)
	}
	report, err := index.Vocab(context.Background(), "salud")
	if err != nil {
		t.Fatal(err)
	}
	if report.Hits != vocabTopK || len(report.HitsByKind) != 1 || report.HitsByKind["exchanges"] != vocabTopK {
		t.Fatalf("hits = %d, by kind = %v", report.Hits, report.HitsByKind)
	}
}

func TestVocabCensusFollowsTheCorpusExactly(t *testing.T) {
	ctx := context.Background()
	corpus := &memoryCorpus{sources: []sourceRow{
		{kind: "exchanges", sessionID: "s", ordinal: 1, hasOrdinal: true, text: "camión médico salud"},
		{kind: "exchanges", sessionID: "s", ordinal: 2, hasOrdinal: true, text: "salud pública"},
		{kind: "thinking_blocks", sessionID: "s", ordinal: 1, hasOrdinal: true, position: "1", text: "CAMIÓN"},
		{kind: "memories", text: "medico de cabecera", layer: "discovery", origin: "human", createdAt: "2026-08-17"},
		{kind: "sessions", sessionID: "s", text: "salud session\nsynthetic-project\n{}"},
	}}
	vectorPath := filepath.Join(t.TempDir(), "vector.db")
	index := Index{Corpus: corpus, VectorPath: vectorPath, Model: DefaultModel, Embedder: vocabEmbedder{}}
	if _, err := index.Ingest(ctx); err != nil {
		t.Fatal(err)
	}
	assertCensus(t, vectorPath, 4, map[string]int64{"camion": 2, "salud": 2, "medico": 2, "publica": 1, "cabecera": 1})
	corpus.sources = []sourceRow{
		corpus.sources[0],
		{kind: "exchanges", sessionID: "s", ordinal: 2, hasOrdinal: true, text: "otra cosa pública"},
		{kind: "memories", text: "medico de cabecera", layer: "discovery", origin: "human", createdAt: "2026-08-17"},
		{kind: "memories", text: "camión nuevo", layer: "discovery", origin: "agent", createdAt: "2026-08-17"},
		corpus.sources[4],
	}
	if _, err := index.Ingest(ctx); err != nil {
		t.Fatal(err)
	}
	assertCensus(t, vectorPath, 4, map[string]int64{"camion": 2, "salud": 1, "medico": 2, "publica": 1,
		"otra": 1, "cosa": 1, "nuevo": 1, "cabecera": 1})
}

type secondBatchFailureEmbedder struct {
	calls int
}

func (*secondBatchFailureEmbedder) Pull(context.Context, string) error { return nil }

func (e *secondBatchFailureEmbedder) Embed(ctx context.Context, model string, input []string) ([][]float32, error) {
	e.calls++
	if e.calls == 2 {
		return nil, fmt.Errorf("synthetic second-batch failure")
	}
	return vocabEmbedder{}.Embed(ctx, model, input)
}

func TestFailedIngestLeavesCensusUnavailable(t *testing.T) {
	ctx := context.Background()
	corpus := &memoryCorpus{sources: []sourceRow{{kind: "exchanges", sessionID: "old",
		ordinal: 1, hasOrdinal: true, text: "scotland old"}}}
	index := Index{Corpus: corpus, VectorPath: filepath.Join(t.TempDir(), "vector.db"),
		Model: DefaultModel, Embedder: vocabEmbedder{}}
	if _, err := index.Ingest(ctx); err != nil {
		t.Fatal(err)
	}
	corpus.sources = nil
	for ordinal := 0; ordinal < defaultBatchSize+1; ordinal++ {
		corpus.sources = append(corpus.sources, sourceRow{kind: "exchanges", sessionID: "new",
			ordinal: int64(ordinal), hasOrdinal: true, text: fmt.Sprintf("cancer changed %d", ordinal)})
	}
	index.Embedder = &secondBatchFailureEmbedder{}
	if _, err := index.Ingest(ctx); err == nil || !strings.Contains(err.Error(), "synthetic second-batch failure") {
		t.Fatalf("failed ingest = %v", err)
	}
	index.Embedder = vocabEmbedder{}
	if _, err := index.Vocab(ctx, "salud"); err == nil ||
		!strings.Contains(err.Error(), "vector census is not built") {
		t.Fatalf("vocab after failed ingest = %v, want the ingest hint", err)
	}
}

func TestVocabRequiresTheCensus(t *testing.T) {
	ctx := context.Background()
	index := Index{Corpus: saludCorpus(), VectorPath: filepath.Join(t.TempDir(), "vector.db"),
		Model: DefaultModel, Embedder: vocabEmbedder{}}
	if _, err := index.Ingest(ctx); err != nil {
		t.Fatal(err)
	}
	store, err := sql.Open("sqlite", index.VectorPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Exec(`DROP TABLE census; DROP TABLE census_totals;`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := index.Vocab(ctx, "salud"); err == nil ||
		!strings.Contains(err.Error(), "vector census is not built") {
		t.Fatalf("missing census = %v, want the ingest hint", err)
	}
}

// invalidatingCorpus makes the concurrency deterministic: midway through
// discovery it wipes the census totals exactly the way a concurrent ingest
// does before its first index mutation.
type invalidatingCorpus struct {
	inner          *memoryCorpus
	path           string
	resolves       int
	invalidateFrom int
}

func (c *invalidatingCorpus) WalkSources(
	ctx context.Context, sourceKind string, visit func(sourceRow) error,
) error {
	return c.inner.WalkSources(ctx, sourceKind, visit)
}

func (c *invalidatingCorpus) ResolveSource(ctx context.Context, kind string, where locator) (string, error) {
	text, err := c.inner.ResolveSource(ctx, kind, where)
	c.resolves++
	if c.resolves == c.invalidateFrom {
		store, err := sql.Open("sqlite", c.path)
		if err != nil {
			return "", err
		}
		defer store.Close()
		if _, err := store.Exec(`DELETE FROM census_totals`); err != nil {
			return "", err
		}
	}
	return text, err
}

func TestVocabRefusesAReportRankedAgainstAStolenCensus(t *testing.T) {
	ctx := context.Background()
	corpus := saludCorpus()
	vectorPath := filepath.Join(t.TempDir(), "vector.db")
	index := Index{Corpus: corpus, VectorPath: vectorPath,
		Model: DefaultModel, Embedder: vocabEmbedder{}}
	if _, err := index.Ingest(ctx); err != nil {
		t.Fatal(err)
	}
	index.Corpus = &invalidatingCorpus{inner: corpus, path: vectorPath, invalidateFrom: 10}
	if _, err := index.Vocab(ctx, "salud"); err == nil ||
		!strings.Contains(err.Error(), "vector census changed during discovery") {
		t.Fatalf("vocab beside a concurrent ingest = %v, want the refusal", err)
	}
}

func TestVocabRejectsAnEmptyConcept(t *testing.T) {
	index := Index{Corpus: saludCorpus(), VectorPath: filepath.Join(t.TempDir(), "vector.db"),
		Model: DefaultModel, Embedder: vocabEmbedder{}}
	if _, err := index.Vocab(context.Background(), "   "); err == nil ||
		!strings.Contains(err.Error(), "concept is empty") {
		t.Fatalf("empty concept = %v, want the empty-concept refusal", err)
	}
}

func avenueContaining(t *testing.T, report VocabReport, term string) int {
	t.Helper()
	for _, avenue := range report.Avenues {
		for _, candidate := range avenue.Terms {
			if candidate.Term == term {
				return avenue.Rank
			}
		}
	}
	t.Fatalf("term %q appears in no avenue: %+v", term, report.Avenues)
	return 0
}

func assertAvenueHas(t *testing.T, report VocabReport, rank int, terms ...string) {
	t.Helper()
	for _, term := range terms {
		if avenueContaining(t, report, term) != rank {
			t.Fatalf("term %q is not in avenue %d", term, rank)
		}
	}
}

func assertCensus(t *testing.T, path string, documents int64, want map[string]int64) {
	t.Helper()
	store, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var got int64
	if err := store.QueryRow(`SELECT documents FROM census_totals WHERE key='documents'`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != documents {
		t.Fatalf("census documents = %d, want %d", got, documents)
	}
	for term, docs := range want {
		var got int64
		if err := store.QueryRow(`SELECT docs FROM census WHERE term=?`, term).Scan(&got); err != nil {
			t.Fatalf("census term %q: %v", term, err)
		}
		if got != docs {
			t.Fatalf("census term %q = %d, want %d", term, got, docs)
		}
	}
}
