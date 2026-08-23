package vector

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type liftEmbedder struct {
	inputs [][]string
}

func (e *liftEmbedder) Pull(context.Context, string) error { return nil }

func (e *liftEmbedder) Embed(_ context.Context, _ string, input []string) ([][]float32, error) {
	e.inputs = append(e.inputs, append([]string(nil), input...))
	out := make([][]float32, len(input))
	for i, text := range input {
		lower := strings.ToLower(text)
		switch {
		case strings.Contains(lower, "qué se habló") || strings.Contains(lower, "hablamos de salud"):
			out[i] = []float32{1, 0, 0, 0, 0, 0, 0, 0}
		case strings.HasSuffix(lower, "salud mental") && !strings.Contains(lower, "habl"):
			out[i] = normalize8(0.18, 0.983, 0, 0, 0, 0, 0, 0)
		case strings.Contains(lower, "session title about recovery"):
			out[i] = []float32{0, 1, 0, 0, 0, 0, 0, 0}
		default:
			out[i] = []float32{0, 0, 1, 0, 0, 0, 0, 0}
		}
	}
	return out, nil
}

func normalize8(values ...float32) []float32 {
	var sum float32
	for _, value := range values {
		sum += value * value
	}
	scale := float32(math.Sqrt(float64(sum)))
	out := make([]float32, 8)
	for i, value := range values {
		out[i] = value / scale
	}
	return out
}

func TestQueryExpandedLiftsBareNounsAndFloorsWeakNeighbors(t *testing.T) {
	federation, corpusPath, _, _ := federationFixture(t)
	embedder := &liftEmbedder{}
	federation.Embedder = embedder
	mutateSourceDatabase(t, corpusPath,
		`INSERT INTO articles VALUES ('salud-1','salud','Hablamos de salud mental en la terapia','raw-counter')`)
	if _, err := federation.Ingest(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	embedder.inputs = nil

	raw, err := federation.Query(context.Background(), "salud mental", 10, "corpus")
	if err != nil {
		t.Fatal(err)
	}
	var rawScore float64
	for _, hit := range raw.Results {
		if hit.ID == "salud-1" {
			rawScore = hit.Score
		}
	}
	if rawScore >= 0.35 {
		t.Fatalf("bare noun already above the floor: %.3f", rawScore)
	}

	embedder.inputs = nil
	expanded, err := federation.QueryExpanded(context.Background(), "salud mental", 10, "corpus", 0.35)
	if err != nil {
		t.Fatal(err)
	}
	if len(embedder.inputs) != 1 || len(embedder.inputs[0]) != 1+len(QuestionTemplates) {
		t.Fatalf("expanded embedding inputs = %q", embedder.inputs)
	}
	found := false
	for _, hit := range expanded.Results {
		if hit.ID == "salud-1" {
			found = true
			if hit.Score < 0.35 {
				t.Fatalf("template-expanded hit stayed below the floor: %+v", hit)
			}
		}
	}
	if !found {
		t.Fatalf("template expansion did not surface the known document: %+v", expanded.Results)
	}
}

func TestIndexQueryExpandedSupportsTheLegacyBoundary(t *testing.T) {
	corpus := &memoryCorpus{sources: []sourceRow{{
		kind: "memories", sourceID: "salud-1", text: "Hablamos de salud mental en la terapia",
	}}}
	index := Index{Corpus: corpus, VectorPath: filepath.Join(t.TempDir(), "vector.db"),
		Model: DefaultModel, Embedder: &liftEmbedder{}, Database: "corpus"}
	if _, err := index.Ingest(context.Background()); err != nil {
		t.Fatal(err)
	}
	raw, err := index.Query(context.Background(), "salud mental", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) == 0 || raw[0].Score >= 0.35 {
		t.Fatalf("raw legacy result = %+v, want a weak neighbor", raw)
	}
	expanded, err := index.QueryExpanded(context.Background(), "salud mental", 10, 0.35)
	if err != nil {
		t.Fatal(err)
	}
	if len(expanded) == 0 || expanded[0].ID != "salud-1" || expanded[0].Score < 0.35 {
		t.Fatalf("expanded legacy result = %+v", expanded)
	}
}

func TestQueryExpandedResolvesSessionSnippetsFromTheCatalog(t *testing.T) {
	root := t.TempDir()
	pluginDir := filepath.Join(root, "roca-corpus")
	if err := os.MkdirAll(pluginDir, 0o700); err != nil {
		t.Fatal(err)
	}
	corpusPath := filepath.Join(pluginDir, "roca-corpus.db")
	createSourceDatabase(t, corpusPath, `
		CREATE TABLE sessions(session_id TEXT PRIMARY KEY, title TEXT, project TEXT);
		INSERT INTO sessions VALUES ('session-salud','Session title about recovery','therapy');`)
	writeRegistry(t, root, vectorRegistry{Schema: 1, Databases: []vectorDatabase{{
		Plugin: "roca-corpus", Database: "corpus", Path: "roca-corpus.db", Alias: "plugin_roca_corpus",
		Tables: []vectorTable{{Name: "sessions", IDColumn: "session_id",
			TextColumns: []string{"title", "project"}}},
	}}, Routes: []vectorRoute{{
		Plugin: "roca-corpus", Database: "corpus", Alias: "plugin_roca_corpus", Source: "plugin:roca-corpus",
	}}})
	embedder := &liftEmbedder{}
	federation, err := LoadFederation(CoreCLI{Executable: "roca", Run: databaseScopeRunner(
		sqliteExecRunner(t, map[string]string{"plugin_roca_corpus": corpusPath}),
		[]DatabaseSelection{{Source: "plugin:roca-corpus", Database: "corpus"}},
	)}, root, DefaultModel, "v-sessions", embedder, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := federation.Ingest(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	result, err := federation.QueryExpanded(context.Background(),
		"session title about recovery", 10, "corpus", 0.35)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Results) == 0 {
		t.Fatal("sessions query returned no hits")
	}
	hit := result.Results[0]
	if hit.Table != "sessions" || hit.ID != "session-salud" {
		t.Fatalf("sessions hit = %+v", hit)
	}
	if !strings.Contains(hit.Text, "Session title about recovery") ||
		!strings.Contains(hit.Text, "therapy") {
		t.Fatalf("session snippet was not resolved from catalog columns: %q", hit.Text)
	}
}
