//go:build acceptance

package acceptance

import (
	"path/filepath"
	"testing"

	"github.com/cucumber/godog"
)

func TestIngestDomainSuite(t *testing.T) {
	binary, err := rocaBinary()
	if err != nil {
		t.Fatalf("locate the real binary: %v", err)
	}
	features, err := loadDomainFeatures(filepath.Join("..", "..", "features", "ingest"))
	if err != nil {
		t.Fatalf("load ingest features: %v", err)
	}
	runGodogTagged(t, features, "~@journey", func(ctx *godog.ScenarioContext) {
		registerIngestDomainSteps(ctx, binary)
	})
}

func registerIngestDomainSteps(ctx *godog.ScenarioContext, binary string) {
	w := &ingestAcceptanceWorld{binary: binary}
	w.registerLifecycle(ctx)
	registerIngestDetectionSteps(ctx, w)
	registerIngestIncrementalSteps(ctx, w)
	registerIngestParsingSteps(ctx, w)
	registerIngestAttributionSteps(ctx, w)
	registerIngestReportSteps(ctx, w)
	registerIngestProvenanceSteps(ctx, w)
}
