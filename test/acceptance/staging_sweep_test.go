//go:build acceptance

package acceptance

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/cucumber/godog"
)

func registerStagingSweepSteps(ctx *godog.ScenarioContext, m *world) {
	var folder, protected string
	m.stagingCleanup = func() error {
		m.readOnly = false
		if protected != "" {
			if err := os.Chmod(protected, 0o700); err != nil {
				return err
			}
		}
		folder, protected = "", ""
		return nil
	}
	ctx.Given(`^an initialized home for the staging sweep$`, func() error {
		return m.mustRun(m.initCommand(true))
	})
	ctx.Given(`^an old "(unremovable|removable)" plugin staging folder$`, func(kind string) error {
		folder = filepath.Join(m.home, ".roca", "plugins", ".roca-corpus-bundle-leftover")
		sub := filepath.Join(folder, "sub")
		if err := os.MkdirAll(sub, 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(sub, "payload"), []byte("12345"), 0o600); err != nil {
			return err
		}
		if kind == "unremovable" {
			protected = sub
			if err := os.Chmod(sub, 0o500); err != nil {
				return err
			}
		}
		old := time.Now().Add(-2 * time.Hour)
		for _, path := range []string{filepath.Join(sub, "payload"), sub, folder} {
			if err := os.Chtimes(path, old, old); err != nil {
				return err
			}
		}
		return nil
	})
	ctx.Given(`^an old plugin staging folder holding a file written a moment ago$`, func() error {
		folder = filepath.Join(m.home, ".roca", "plugins", ".roca-corpus-bundle-writing")
		if err := os.MkdirAll(folder, 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(folder, "payload"), []byte("12345"), 0o600); err != nil {
			return err
		}
		old := time.Now().Add(-2 * time.Hour)
		return os.Chtimes(folder, old, old)
	})
	ctx.Given(`^the old staging folder has an unlocked lock file$`, func() error {
		path := filepath.Join(folder, ".held")
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			return err
		}
		old := time.Now().Add(-2 * time.Hour)
		for _, path := range []string{path, folder} {
			if err := os.Chtimes(path, old, old); err != nil {
				return err
			}
		}
		return nil
	})
	ctx.Given(`^staging commands use read-only mode$`, func() { m.readOnly = true })
	ctx.Then(`^the old staging folder still exists$`, func() error {
		_, err := os.Stat(folder)
		return err
	})
	ctx.Then(`^the staging query prints the memory count$`, func() error {
		for _, line := range strings.Split(m.last.stdout, "\n") {
			value, database, _ := strings.Cut(strings.TrimSpace(line), ",")
			if count, err := strconv.Atoi(value); err == nil && count == 0 && database == `"plugin:roca-ops"` {
				return nil
			}
		}
		return fmt.Errorf("query did not print the empty home's count: %q", m.last.stdout)
	})
	ctx.Then(`^doctor lists the old staging folder with 5 bytes and cause "([^"]*)"$`, func(cause string) error {
		var report struct {
			PluginStaging []struct {
				Path  string `json:"path"`
				Bytes int64  `json:"bytes"`
				Cause string `json:"cause"`
			} `json:"plugin_staging"`
		}
		if err := json.Unmarshal([]byte(m.last.stdout), &report); err != nil {
			return err
		}
		if len(report.PluginStaging) != 1 {
			return fmt.Errorf("plugin_staging = %+v, want one leftover", report.PluginStaging)
		}
		got := report.PluginStaging[0]
		if got.Path != folder || got.Bytes != 5 || !strings.Contains(got.Cause, cause) {
			return fmt.Errorf("plugin_staging = %+v, want %s, 5 bytes, %s", got, folder, cause)
		}
		return nil
	})
	ctx.Then(`^doctor lists no plugin staging folder$`, func() error {
		var report struct {
			PluginStaging []json.RawMessage `json:"plugin_staging"`
		}
		if err := json.Unmarshal([]byte(m.last.stdout), &report); err != nil {
			return err
		}
		if len(report.PluginStaging) != 0 {
			return fmt.Errorf("plugin_staging = %s, want none", report.PluginStaging)
		}
		return nil
	})
	ctx.Then(`^doctor prints the old staging folder and "([^"]*)" on one line$`, func(cause string) error {
		for _, line := range strings.Split(m.last.stdout, "\n") {
			if strings.Contains(line, folder) && strings.Contains(line, cause) {
				return nil
			}
		}
		return fmt.Errorf("doctor did not print %s with %s: %s", folder, cause, m.last.stdout)
	})
}
