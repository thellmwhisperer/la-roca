//go:build acceptance

package acceptance

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/cucumber/godog"
)

func TestCoverDiffAcceptanceEnvironments(t *testing.T) {
	if os.Getenv("GOCOVERDIR") == "" {
		t.Skip("requires the coverage-instrumented make build from make e2e-cover")
	}
	binary, err := rocaBinary()
	if err != nil {
		t.Fatal(err)
	}
	for _, builder := range []string{"world", "distribution", "ingest", "oracle"} {
		t.Run(builder, func(t *testing.T) {
			home, err := acceptanceTempDir("cover-environment-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.RemoveAll(home) })
			coverage := filepath.Join(home, "coverage")
			if err := os.MkdirAll(coverage, 0o700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("GOCOVERDIR", coverage)
			runGodog(t, []godog.Feature{{Name: "coverage.feature", Contents: []byte(`Feature: Real binary coverage
  Scenario: The acceptance environment preserves coverage
    When the real roca version command runs
    Then the process leaves coverage counters
`)}}, func(ctx *godog.ScenarioContext) {
				ctx.When(`^the real roca version command runs$`, func() error {
					var code int
					var stderr string
					switch builder {
					case "world":
						w := &world{binary: binary, home: home}
						r, err := w.runWith("roca version", []string{"version"})
						if err != nil {
							return err
						}
						code, stderr = r.code, r.stderr
					case "distribution":
						w := &distributionWorld{}
						r := w.runAt(home, binary, "version")
						code, stderr = r.code, r.stderr
					case "ingest":
						w := &ingestAcceptanceWorld{binary: binary, home: home}
						r, err := w.runCommand("version")
						if err != nil {
							return err
						}
						code, stderr = r.code, r.stderr
					case "oracle":
						w := &oracleRunner{binary: binary, home: home}
						r, _ := w.runCLI("version", "version", "version")
						code, stderr = r.ExitCode, r.Stderr
					}
					if code != 0 {
						return fmt.Errorf("roca version: exit %d: %s", code, stderr)
					}
					return nil
				})
				ctx.Then(`^the process leaves coverage counters$`, func() error {
					files, err := filepath.Glob(filepath.Join(coverage, "covcounters.*"))
					if err != nil || len(files) == 0 {
						return fmt.Errorf("%s left %d counter files: %v", builder, len(files), err)
					}
					return nil
				})
			})
		})
	}
}
