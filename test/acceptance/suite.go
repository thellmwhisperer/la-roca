//go:build acceptance

package acceptance

import (
	"os"
	"testing"

	"github.com/cucumber/godog"
)

// runGodog runs one godog suite over prepared feature contents. The consecrated
// suite and the per-domain suites reach the same runner, so the suite
// boilerplate lives in one place instead of being copied into every Test*
// function that opens a suite.
func runGodog(t *testing.T, features []godog.Feature, init func(*godog.ScenarioContext)) {
	suite := godog.TestSuite{
		ScenarioInitializer: init,
		Options: &godog.Options{
			Format:          "pretty",
			FeatureContents: features,
			Output:          os.Stdout,
			TestingT:        t,
			Strict:          true,
		},
	}
	if suite.Run() != 0 {
		t.Fail()
	}
}
