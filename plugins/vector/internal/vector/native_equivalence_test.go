//go:build cgo && native_equivalence && !windows

package vector

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

type goldenVectors struct {
	Reference        string         `json:"reference"`
	MaxAbsoluteDelta float64        `json:"max_absolute_delta"`
	Vectors          []goldenVector `json:"vectors"`
}

type goldenVector struct {
	Input  string    `json:"input"`
	Vector []float32 `json:"vector"`
}

func TestNativeEngineMatchesPublicEquivalenceFixture(t *testing.T) {
	inputs := publicEquivalenceInputs(t)
	dataDir := os.Getenv("ROCA_VECTOR_EQUIVALENCE_DATA_DIR")
	if dataDir == "" {
		dataDir = t.TempDir()
	}
	embedder := ConfiguredEmbedder(dataDir, t.TempDir(), nil, nil, false)
	if closer, ok := embedder.(*Native); ok {
		t.Cleanup(closer.Close)
	}
	vectors, err := embedder.Embed(context.Background(), DefaultModel, inputs)
	if err != nil {
		t.Fatal(err)
	}
	var golden goldenVectors
	decodeFixture(t, "public-golden-vectors.json", &golden)
	if golden.Reference != "previous-production-engine" || golden.MaxAbsoluteDelta <= 0 ||
		len(golden.Vectors) != len(inputs) {
		t.Fatalf("invalid golden vector contract: reference=%q tolerance=%g vectors=%d inputs=%d",
			golden.Reference, golden.MaxAbsoluteDelta, len(golden.Vectors), len(inputs))
	}
	want := make(map[string][]float32, len(golden.Vectors))
	for _, fixture := range golden.Vectors {
		want[fixture.Input] = fixture.Vector
	}
	for inputIndex, input := range inputs {
		reference, ok := want[input]
		if !ok {
			t.Errorf("public input %q has no reference vector", input)
			continue
		}
		if len(vectors[inputIndex]) != len(reference) {
			t.Errorf("public input %q dimensions = %d, want %d", input, len(vectors[inputIndex]), len(reference))
			continue
		}
		maxDelta := 0.0
		maxElement := 0
		for element := range reference {
			delta := math.Abs(float64(vectors[inputIndex][element] - reference[element]))
			if delta > maxDelta {
				maxDelta = delta
				maxElement = element
			}
		}
		if maxDelta > golden.MaxAbsoluteDelta {
			t.Errorf("public input %q maximum delta at element %d = %.9g, limit %.9g",
				input, maxElement, maxDelta, golden.MaxAbsoluteDelta)
		}
	}
	nulVectors, err := embedder.Embed(context.Background(), DefaultModel,
		[]string{QueryPrefix + "before", QueryPrefix + "before\x00after"})
	if err != nil {
		t.Fatal(err)
	}
	if len(nulVectors) != 2 || slices.Equal(nulVectors[0], nulVectors[1]) {
		t.Fatal("embedded NUL truncated the input")
	}
}

func publicEquivalenceInputs(t *testing.T) []string {
	t.Helper()
	var corpus []struct {
		Text string `json:"text"`
	}
	var queries []struct {
		Query string `json:"query"`
	}
	decodeFixture(t, "public-corpus.json", &corpus)
	decodeFixture(t, "public-queries.json", &queries)
	inputs := make([]string, 0, len(corpus)+len(queries))
	for _, fixture := range corpus {
		inputs = append(inputs, DocumentPrefix+fixture.Text)
	}
	for _, fixture := range queries {
		inputs = append(inputs, QueryPrefix+fixture.Query)
	}
	return inputs
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
