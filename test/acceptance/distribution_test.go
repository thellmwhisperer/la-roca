//go:build acceptance

package acceptance

import (
	"os"
	"testing"

	"github.com/cucumber/godog"
)

func TestDistributionAcceptance(t *testing.T) {
	binary, err := rocaBinary()
	if err != nil {
		t.Fatalf("locate the real binary: %v", err)
	}

	suite := godog.TestSuite{
		ScenarioInitializer: func(ctx *godog.ScenarioContext) {
			registerDistributionSteps(ctx, binary)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/distribution"},
			Output:   os.Stdout,
			TestingT: t,
			Strict:   true,
			Tags:     "~@journey",
		},
	}
	if suite.Run() != 0 {
		t.Fail()
	}
}
