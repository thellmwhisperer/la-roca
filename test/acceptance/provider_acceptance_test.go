//go:build acceptance

package acceptance

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/cucumber/godog"
)

func TestProviderAcceptanceSuite(t *testing.T) {
	binary, err := rocaBinary()
	if err != nil {
		t.Fatalf("find the real binary: %v", err)
	}

	suite := godog.TestSuite{
		ScenarioInitializer: func(ctx *godog.ScenarioContext) {
			registerProviderAcceptanceSteps(ctx, binary)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{filepath.Join("..", "..", "features", "provider")},
			Output:   os.Stdout,
			Strict:   true,
			TestingT: t,
		},
	}
	if suite.Run() != 0 {
		t.Fail()
	}
}
