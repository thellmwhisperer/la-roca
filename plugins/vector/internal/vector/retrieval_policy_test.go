package vector

import (
	"context"
	"hash/fnv"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode"
)

func TestPerColumnChunkingLiftsShortHumanTextAboveConcatenation(t *testing.T) {
	human := "personal exhaustion after the night shift"
	agent := strings.Repeat("kubernetes replica rollout canary digest ", 80)
	query := "personal exhaustion after the night shift"
	concat := memoryCorpus{sources: []sourceRow{{
		kind: "exchanges", sourceID: "122300", text: human + "\n\n" + agent,
		rowText: human + "\n\n" + agent,
	}}}
	split := memoryCorpus{sources: []sourceRow{
		{kind: "exchanges", sourceID: "122300", column: "human_text", text: human, rowText: human + "\n\n" + agent},
		{kind: "exchanges", sourceID: "122300", column: "agent_text", text: agent, rowText: human + "\n\n" + agent},
	}}
	before := maxScore(t, &concat, query)
	after := maxScore(t, &split, query)
	t.Logf("drowning cosine before=%.3f after=%.3f", before, after)
	if after <= before {
		t.Fatalf("per-column retrieval did not improve: before=%.3f after=%.3f", before, after)
	}
	if after < 0.8 {
		t.Fatalf("human-only chunk cosine = %.3f, want a near match", after)
	}
}

func TestGoldenVideoQueriesKeepTopRank(t *testing.T) {
	if os.Getenv("ROCA_VECTOR_LAB") != "1" {
		t.Skip("set ROCA_VECTOR_LAB=1 to measure the current embedding engine")
	}
	docs := []sourceRow{
		{kind: "memories", sourceID: "doc-private-first",
			text: "Never publish a short video as private first. The recommendation system will not test the thumbnail or the title while it stays hidden."},
		{kind: "memories", sourceID: "doc-title-change",
			text: "Changing the title of a dead short did not revive it. The first impression is spent on the original packaging."},
		{kind: "memories", sourceID: "doc-bridge",
			text: "The bridge from short videos to long-form videos works when the short promises a specific payoff the long video actually delivers."},
		{kind: "memories", sourceID: "doc-lessons",
			text: "Lessons about making short videos: hook in the first second, readable title, and never burn the first public impression on a private upload."},
		{kind: "memories", sourceID: "doc-unrelated",
			text: "A garden journal notes that tomatoes prefer consistent watering and afternoon shade in late summer."},
	}
	cases := []struct {
		query string
		id    string
	}{
		{"why should I never upload a short as private first", "memories/doc-private-first"},
		{"what happened when I changed the title of a dead short", "memories/doc-title-change"},
		{"does the bridge from shorts to long-form videos actually work", "memories/doc-bridge"},
		{"what lessons did I learn about making YouTube shorts for my channel", "memories/doc-lessons"},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	index := Index{Corpus: &memoryCorpus{sources: docs}, VectorPath: filepath.Join(t.TempDir(), "vector.db"),
		Model: DefaultModel, Embedder: Ollama{BaseURL: os.Getenv("OLLAMA_HOST")}}
	if _, err := index.Ingest(ctx); err != nil {
		t.Fatal(err)
	}
	for _, item := range cases {
		results, err := index.Query(ctx, item.query, 5)
		if err != nil {
			t.Fatalf("%s: %v", item.query, err)
		}
		if len(results) == 0 || results[0].SourceID != item.id {
			t.Fatalf("video query %q rank1=%+v, want %s", item.query, results, item.id)
		}
	}
}

func maxScore(t *testing.T, corpus *memoryCorpus, query string) float64 {
	t.Helper()
	index := Index{Corpus: corpus, VectorPath: filepath.Join(t.TempDir(), "vector.db"),
		Model: DefaultModel, Embedder: lexicalEmbedder{}}
	if _, err := index.Ingest(context.Background()); err != nil {
		t.Fatal(err)
	}
	results, err := index.Query(context.Background(), query, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		return 0
	}
	return results[0].Score
}

type lexicalEmbedder struct{}

func (lexicalEmbedder) Pull(context.Context, string) error { return nil }

func (lexicalEmbedder) Embed(_ context.Context, _ string, input []string) ([][]float32, error) {
	out := make([][]float32, len(input))
	for i, text := range input {
		out[i] = lexicalVector(text)
	}
	return out, nil
}

func lexicalVector(text string) []float32 {
	const dim = 256
	vec := make([]float32, dim)
	text = strings.TrimPrefix(text, DocumentPrefix)
	text = strings.TrimPrefix(text, QueryPrefix)
	if start := strings.Index(text, "] "); start >= 0 && strings.HasPrefix(strings.TrimSpace(text), "[") {
		text = text[start+2:]
	}
	text = strings.ToLower(text)
	var tokens []string
	var token strings.Builder
	flush := func() {
		if token.Len() == 0 {
			return
		}
		tokens = append(tokens, token.String())
		token.Reset()
	}
	for _, r := range text {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			token.WriteRune(r)
			continue
		}
		flush()
	}
	flush()
	add := func(word string) {
		sum := fnv.New32a()
		_, _ = sum.Write([]byte(word))
		vec[int(sum.Sum32())%dim]++
	}
	synonyms := map[string]string{"shorts": "short", "youtube": "video", "videos": "video"}
	for i, word := range tokens {
		add(word)
		if canon, ok := synonyms[word]; ok {
			add(canon)
		}
		if i+1 < len(tokens) {
			add(word + " " + tokens[i+1])
			next := tokens[i+1]
			if canon, ok := synonyms[next]; ok {
				next = canon
			}
			left := word
			if canon, ok := synonyms[word]; ok {
				left = canon
			}
			add(left + " " + next)
		}
	}
	var norm float64
	for _, value := range vec {
		norm += float64(value) * float64(value)
	}
	if norm == 0 {
		return vec
	}
	scale := float32(1 / math.Sqrt(norm))
	for i := range vec {
		vec[i] *= scale
	}
	return vec
}
