//go:build acceptance

// Package acceptance runs the frozen Gherkin suite against the real binary.
//
// It is black box by construction: not one symbol of the product is imported
// here. The only thing this package knows how to do is prepare a toy HOME, run
// `roca` as a subprocess and read its output.
//
// The runner executes only the explicitly selected scenarios, so a green result
// cannot hide unimplemented contracts.
package acceptance

import (
	"testing"

	"github.com/cucumber/godog"
)

// selectedScenarios are the implemented contracts exercised by this build.
var selectedScenarios = []string{
	"D-4b ", // DDL formatting noise never blocks
	"F07-01 ", "F07-03 ", "F07-07 ",
	"F07-10 ", "F07-11 ", "F07-12 ",
	"F02-04 ",
	"F08-01 ", "F08-02 ", "F08-03 ", "F08-04 ", "F08-05 ",
	"F08-07 ", "F08-08 ",
	"F01-01 ",
	"F01-10 ", "F01-11 ", "F01-12 ",
	"F02-01 ", "F02-07 ", "F02-08 ",
}

func TestAcceptanceSuite(t *testing.T) {
	features, err := selectScenarios("../../features", selectedScenarios)
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

	runGodog(t, features, func(ctx *godog.ScenarioContext) {
		registerSteps(ctx, binary)
	})
}
