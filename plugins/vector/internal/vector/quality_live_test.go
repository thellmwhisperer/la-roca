package vector

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestLiveRetrievalQuality is opt-in because it calls the operator's local
// Ollama runtime. It is the repeatable release measurement for the 87.5%
// hit@5 / 0% zero-result bar established by the original vector lab.
func TestLiveRetrievalQuality(t *testing.T) {
	if os.Getenv("ROCA_VECTOR_LIVE_EVAL") != "1" {
		t.Skip("set ROCA_VECTOR_LIVE_EVAL=1 to run the local retrieval measurement")
	}
	model := os.Getenv("ROCA_VECTOR_EVAL_MODEL")
	if model == "" {
		model = DefaultModel
	}
	targets := []struct {
		query string
		text  string
	}{
		{"What was the latest illness and which medical tests were ordered?", "The synthetic patient is monitoring Crohn's treatment with adalimumab; the latest follow-up ordered a colonoscopy and a calprotectin assay."},
		{"How was the failed software release recovered?", "The synthetic deployment was restored by rolling back the canary, pinning the prior image digest, and replaying the database migration backup."},
		{"What must be prepared for the quarterly tax filing?", "The synthetic company must reconcile purchase receipts, sales invoices, and the VAT control account before sending the quarter to its accountant."},
		{"Which tree limbs should be removed during the cold season?", "The synthetic orchard plan marks crossing branches, one water sprout, and the damaged limb above the graft union for winter pruning."},
		{"Why did the spacecraft miss its observation window?", "The synthetic telescope lost its slot because a reaction-wheel fault delayed the slew toward the exoplanet transit."},
		{"What changed after the credential incident?", "The synthetic security response rotated the signing key, revoked every active session, and shortened access-token lifetime."},
		{"How will the delayed shipment reach the island?", "The synthetic logistics plan moves the crates by rail to the coastal depot and books the final leg on the Friday ferry."},
		{"What did the ensemble rehearse before the concert?", "The synthetic quartet practised the quiet transition into the final movement and corrected the cello entrance after the fermata."},
	}
	distractors := []string{
		"The synthetic bakery adjusted sourdough hydration after changing flour suppliers.",
		"The synthetic classroom replaced paper attendance with a barcode scanner.",
		"The synthetic weather station calibrated its rain gauge before the storm season.",
		"The synthetic library moved biographies to the western shelves.",
		"The synthetic bicycle workshop ordered new brake cables and chain lubricant.",
		"The synthetic theatre painted the backdrop blue for the afternoon performance.",
		"The synthetic café changed its espresso grinder setting after roasting darker beans.",
		"The synthetic apartment installed thicker curtains to reduce street noise.",
		"The synthetic photographer catalogued portrait lenses separately from tripods.",
		"The synthetic swimming pool lowered chlorine dosage after retesting the water.",
		"The synthetic museum repaired a cracked frame around a landscape painting.",
		"The synthetic gym moved the rowing machines beside the ventilation ducts.",
		"The synthetic radio producer shortened the opening theme by twelve seconds.",
		"The synthetic tailor replaced brass buttons on a wool coat.",
		"The synthetic greenhouse added shade cloth above the tomato beds.",
		"The synthetic kitchen stored ceramic plates above the preparation counter.",
	}
	corpus := &memoryCorpus{}
	expected := make([]string, len(targets))
	for index, target := range targets {
		source := qualitySource(index, target.text)
		corpus.sources = append(corpus.sources, source)
		expected[index] = source.stableID()
	}
	for index, text := range distractors {
		corpus.sources = append(corpus.sources, qualitySource(index+len(targets), text))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	index := Index{Corpus: corpus, VectorPath: filepath.Join(t.TempDir(), "vector.db"),
		Model: model, Embedder: Ollama{BaseURL: os.Getenv("OLLAMA_HOST")}}
	if _, err := index.Ingest(ctx); err != nil {
		t.Fatal(err)
	}
	hits, zero := 0, 0
	for caseIndex, target := range targets {
		results, err := index.Query(ctx, target.query, 5)
		if err != nil {
			t.Fatalf("case %d: %v", caseIndex+1, err)
		}
		if len(results) == 0 {
			zero++
		}
		for _, result := range results {
			if result.SourceID == expected[caseIndex] {
				hits++
				break
			}
		}
	}
	hitRate := float64(hits) * 100 / float64(len(targets))
	zeroRate := float64(zero) * 100 / float64(len(targets))
	t.Logf("vector quality: hit@5 %.1f%% (%d/%d), zero-result %.1f%% (%d/%d), model %s",
		hitRate, hits, len(targets), zeroRate, zero, len(targets), model)
	if hitRate < 87.5 || zero != 0 {
		t.Fatalf("quality below lab bar: hit@5 %.1f%%, zero-result %.1f%%", hitRate, zeroRate)
	}
}

func qualitySource(index int, text string) sourceRow {
	return sourceRow{kind: "memories", text: text, layer: "discovery", origin: "fixture",
		createdAt: time.Date(2026, 1, index+1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339)}
}
