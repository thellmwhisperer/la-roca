//go:build acceptance

package acceptance

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cucumber/godog"
	"github.com/thellmwhisperer/la-roca/internal/securefile"
)

func TestPluginStagingAcceptance(t *testing.T) {
	binary, err := rocaBinary()
	if err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile("../../features/distribution/install.feature")
	if err != nil {
		t.Fatal(err)
	}
	runGodogTagged(t, []godog.Feature{{Name: "distribution/install.feature", Contents: contents}},
		"@plugin-staging", func(ctx *godog.ScenarioContext) {
			m := &world{binary: binary}
			ctx.Before(func(c context.Context, _ *godog.Scenario) (context.Context, error) {
				var err error
				m.home, err = os.MkdirTemp("", "plugin-staging-")
				return c, err
			})
			ctx.After(func(c context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
				if m.stagingCleanup != nil {
					if err := m.stagingCleanup(); err != nil {
						return c, err
					}
				}
				return c, os.RemoveAll(m.home)
			})
			ctx.Given(`^a clean HOME with no trace of Roca$`, func() error { return nil })
			ctx.When(`^I run "([^"]*)"$`, m.iRun)
			ctx.Then(`^the command exits with code (\d+)$`, m.itExitsWithCode)
			ctx.Then(`^the command exits with a code other than 0$`, m.itExitsWithNonZeroCode)
			ctx.Then(`^the output contains "([^"]*)"$`, m.outputContains)
			registerPluginStagingSteps(ctx, m)
		})
}

func registerPluginStagingSteps(ctx *godog.ScenarioContext, m *world) {
	registerStagingSweepSteps(ctx, m)
	var release func() error
	var preserved []string
	var fresh string
	root := func() string { return filepath.Join(m.home, ".roca", "plugins") }
	held := func() string { return filepath.Join(root(), ".roca-corpus-bundle-held", ".held") }
	ctx.After(func(c context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
		if release != nil {
			_ = release()
			release = nil
		}
		preserved = nil
		fresh = ""
		return c, nil
	})
	ctx.Given(`^abandoned bundled plugin staging folders$`, func() error {
		names := []string{
			".roca-corpus-schema-preflight-abandoned", ".roca-corpus-bundle-one", ".roca-corpus-bundle-two",
		}
		for _, name := range names {
			directory := filepath.Join(root(), name)
			if err := os.MkdirAll(directory, 0o700); err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(directory, "roca-corpus.db"), []byte("abandoned database copy"), 0o600); err != nil {
				return err
			}
		}
		// One old lock file is unlocked; the others predate the lock protocol.
		if err := os.WriteFile(filepath.Join(root(), ".roca-corpus-bundle-one", ".held"), nil, 0o600); err != nil {
			return err
		}
		old := time.Now().Add(-2 * time.Hour)
		for _, name := range names {
			if err := os.Chtimes(filepath.Join(root(), name, "roca-corpus.db"), old, old); err != nil {
				return err
			}
			if err := os.Chtimes(filepath.Join(root(), name), old, old); err != nil {
				return err
			}
		}
		return os.Chtimes(filepath.Join(root(), ".roca-corpus-bundle-one", ".held"), old, old)
	})
	ctx.Given(`^a just-created "([^"]*)" staging folder without a lock$`, func(kind string) error {
		fresh = filepath.Join(root(), ".roca-corpus-"+kind+"-fresh")
		return os.MkdirAll(fresh, 0o700)
	})
	ctx.Then(`^the just-created staging folder still exists$`, func() error {
		_, err := os.Stat(fresh)
		return err
	})
	ctx.Given(`^the plugin staging filesystem fails at "([^"]*)"$`, func(location string) error {
		if err := os.MkdirAll(filepath.Dir(root()), 0o700); err != nil {
			return err
		}
		switch location {
		case "plugin root":
			return os.WriteFile(root(), nil, 0o600)
		case "lock file":
			return os.MkdirAll(filepath.Join(root(), ".roca-ops-bundle-abandoned", ".held"), 0o700)
		case "stage creation":
			return os.MkdirAll(root(), 0o500)
		}
		return fmt.Errorf("unknown failure location %q", location)
	})
	ctx.Given(`^a bundled plugin staging folder locked by the test process$`, func() error {
		if err := os.MkdirAll(filepath.Dir(held()), 0o700); err != nil {
			return err
		}
		var err error
		release, err = securefile.Lock(held())
		if err != nil {
			return err
		}
		old := time.Now().Add(-2 * time.Hour)
		if err := os.Chtimes(held(), old, old); err != nil {
			return err
		}
		return os.Chtimes(filepath.Dir(held()), old, old)
	})
	ctx.When(`^I install bundled plugins while the staging lock is held$`, func() error {
		// A blocking sweep cannot finish until this step returns and releases
		// the parent's lock. The deadline turns that deadlock into a failure.
		c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return m.record("roca _install-bundled-plugins --json",
			exec.CommandContext(c, m.binaryPath(), "_install-bundled-plugins", "--json"))
	})
	ctx.When(`^the test process releases the staging lock$`, func() error {
		err := release()
		release = nil
		return err
	})
	ctx.Then(`^the held staging folder and lock file still exist$`, func() error {
		for _, path := range []string{filepath.Dir(held()), held()} {
			if _, err := os.Stat(path); err != nil {
				return err
			}
		}
		return nil
	})
	ctx.Then(`^no bundled plugin staging folders remain$`, func() error {
		entries, err := os.ReadDir(root())
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), ".") &&
				(strings.Contains(entry.Name(), "-schema-preflight-") || strings.Contains(entry.Name(), "-bundle-")) {
				return fmt.Errorf("staging folder remains: %s", entry.Name())
			}
		}
		return nil
	})
	ctx.Given(`^installed plugin databases and their sidecar files$`, func() error {
		if err := m.mustRun("roca _install-bundled-plugins --json"); err != nil {
			return err
		}
		// This local plugin has no bundled update owner. Its opaque bytes must
		// survive beside the real bundled databases checked below.
		for _, name := range []string{"roca-corpus", "roca-ops", "local-plugin"} {
			directory := filepath.Join(root(), name)
			if err := os.MkdirAll(directory, 0o700); err != nil {
				return err
			}
			preserved = append(preserved, directory)
			suffixes := []string{""}
			if name == "local-plugin" {
				suffixes = append(suffixes, "-wal", "-shm", "-journal")
			}
			for _, suffix := range suffixes {
				path := filepath.Join(directory, name+".db"+suffix)
				if name == "local-plugin" || suffix != "" {
					if err := os.WriteFile(path, []byte("installed plugin data"), 0o600); err != nil {
						return err
					}
				}
				preserved = append(preserved, path)
			}
		}
		for _, name := range []string{".unrelated-cache", ".roca-corpus-bundle", ".roca-corpus-schema-preflight"} {
			path := filepath.Join(root(), name)
			if err := os.MkdirAll(path, 0o700); err != nil {
				return err
			}
			preserved = append(preserved, path)
		}
		return nil
	})
	ctx.Then(`^every installed plugin directory and data file still exists$`, func() error {
		for _, path := range preserved {
			if _, err := os.Stat(path); err != nil {
				return err
			}
		}
		return nil
	})
}
