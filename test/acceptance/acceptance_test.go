//go:build acceptance

// Package acceptance runs the Gherkin catalog against the real binary.
//
// It is black box by construction: not one symbol of the product is imported
// here. The only thing this package knows how to do is prepare a toy HOME, run
// `roca` as a subprocess and read its output.
//
// Domain suites exercise the domain vocabulary. Cross-surface journeys use the
// shared black-box world, and both paths discover their scenarios from tags.
package acceptance

import (
	"testing"

	"github.com/cucumber/godog"
)

func TestJourneyAcceptanceSuite(t *testing.T) {
	features, err := loadCatalogFeatures("../../features")
	if err != nil {
		t.Fatalf("prepare the features: %v", err)
	}
	if len(features) == 0 {
		t.Fatal("no scenario selected")
	}

	binary, err := rocaBinary()
	if err != nil {
		t.Fatalf("I cannot find the binary: %v", err)
	}

	runGodogTagged(t, features, "@journey", func(ctx *godog.ScenarioContext) {
		registerSteps(ctx, binary)
	})
}
