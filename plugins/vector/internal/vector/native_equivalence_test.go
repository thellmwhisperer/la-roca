//go:build cgo && native_equivalence && !windows

package vector

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"
)

func TestNativeEngineMatchesPublicEquivalenceFixture(t *testing.T) {
	var corpus []struct {
		ID   string `json:"id"`
		Text string `json:"text"`
	}
	var queries []struct {
		Query  string `json:"query"`
		WantID string `json:"want_id"`
	}
	decodeFixture(t, "public-corpus.json", &corpus)
	decodeFixture(t, "public-queries.json", &queries)
	embedder := ConfiguredEmbedder(t.TempDir(), t.TempDir(), nil, nil)
	if closer, ok := embedder.(*Native); ok {
		t.Cleanup(closer.Close)
	}
	documents := make([]string, len(corpus))
	for index := range corpus {
		documents[index] = DocumentPrefix + corpus[index].Text
	}
	documentVectors, err := embedder.Embed(context.Background(), DefaultModel, documents)
	if err != nil {
		t.Fatal(err)
	}
	for _, fixture := range queries {
		queryVectors, err := embedder.Embed(context.Background(), DefaultModel,
			[]string{QueryPrefix + fixture.Query})
		if err != nil {
			t.Fatal(err)
		}
		bestID, bestScore := "", -math.MaxFloat64
		for index, vector := range documentVectors {
			score := cosine(queryVectors[0], vector)
			if score > bestScore {
				bestID, bestScore = corpus[index].ID, score
			}
		}
		if bestID != fixture.WantID {
			t.Errorf("query %q ranked %q first, want %q", fixture.Query, bestID, fixture.WantID)
		}
	}
}

func decodeFixture(t *testing.T, name string, target any) {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(body, target); err != nil {
		t.Fatal(err)
	}
}

func cosine(left, right []float32) float64 {
	var dot, leftNorm, rightNorm float64
	for index := range left {
		l, r := float64(left[index]), float64(right[index])
		dot += l * r
		leftNorm += l * l
		rightNorm += r * r
	}
	return dot / math.Sqrt(leftNorm*rightNorm)
}
