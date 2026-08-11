//go:build acceptance

package acceptance

import (
	"os"
	"testing"

	"github.com/cucumber/godog"
)

// runGodog runs one Godog suite over prepared feature contents. The journey
// suite and the per-domain suites reach the same runner, so the suite
// boilerplate lives in one place instead of being copied into every Test*
// function that opens a suite.
func runGodog(t *testing.T, features []godog.Feature, init func(*godog.ScenarioContext)) {
	runGodogTagged(t, features, "", init)
}

func runGodogTagged(t *testing.T, features []godog.Feature, tags string, init func(*godog.ScenarioContext)) {
	suite := godog.TestSuite{
		ScenarioInitializer: init,
		Options: &godog.Options{
			Format:          "pretty",
			FeatureContents: features,
			Output:          os.Stdout,
			TestingT:        t,
			Strict:          true,
			Tags:            tags,
		},
	}
	if suite.Run() != 0 {
		t.Fail()
	}
}
