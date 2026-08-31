//go:build acceptance

package acceptance

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/cucumber/godog"
)

func registerIngestDetectionSteps(ctx *godog.ScenarioContext, w *ingestAcceptanceWorld) {
	ctx.Given(`^these supported agent families are present:$`, func(table *godog.Table) error {
		w.expected = nil
		for _, row := range table.Rows[1:] {
			family := row.Cells[0].Value
			if err := w.seedPresentFamily(family); err != nil {
				return err
			}
			w.expected = append(w.expected, family)
		}
		return nil
	})
	ctx.Given(`^only the supported agent family "([^"]*)" is present$`, func(family string) error {
		w.expected = []string{family}
		return w.seedPresentFamily(family)
	})
	ctx.When(`^I inspect ingest without writing$`, func() error { return w.runIngest(true) })
	ctx.Then(`^exactly those agent families are detected$`, func() error {
		found, err := w.reportedFamilies("detected_agents")
		if err != nil {
			return err
		}
		if !slices.Equal(found, w.expected) {
			return fmt.Errorf("detected families = %v, want %v", found, w.expected)
		}
		return nil
	})
	ctx.Then(`^the supported agent family "([^"]*)" is reported as not found$`, func(family string) error {
		missing, err := w.reportedFamilies("agents_not_found")
		if err != nil {
			return err
		}
		if !slices.Contains(supportedIngestFamilies, family) {
			return fmt.Errorf("%q is not in the supported family roster", family)
		}
		if !slices.Contains(missing, family) {
			return fmt.Errorf("absent family %q is missing from agents_not_found: %v", family, missing)
		}
		return nil
	})
	ctx.Then(`^ingest reports no errors$`, func() error {
		errors, err := ingestJSONNumber(w.last.doc, "errors")
		if err != nil {
			return err
		}
		if errors != 0 || w.last.code != 0 {
			return fmt.Errorf("errors=%d code=%d: %s", errors, w.last.code, w.last.stderr)
		}
		return nil
	})
}

func (w *ingestAcceptanceWorld) seedPresentFamily(family string) error {
	var path string
	switch family {
	case "claude":
		path = filepath.Join(w.home, ".claude", "projects")
	case "claude-desktop":
		path = filepath.Join(w.appSupportPath(), "claude-code-sessions")
	case "cowork":
		path = filepath.Join(w.appSupportPath(), "local-agent-mode-sessions")
	case "codex":
		path = filepath.Join(w.home, ".codex")
	case "pi":
		path = filepath.Join(w.home, ".pi", "agent", "sessions")
	case "opencode":
		return writeFixture(filepath.Join(w.home, ".local", "share", "opencode", "opencode.db"), "route marker")
	case "zcode":
		return writeFixture(filepath.Join(w.home, ".zcode", "cli", "db", "db.sqlite"), "route marker")
	case "hermes":
		return writeFixture(filepath.Join(w.home, ".hermes", "state.db"), "route marker")
	case "grok":
		path = filepath.Join(w.home, ".grok", "sessions")
	default:
		return fmt.Errorf("unsupported fixture family %q", family)
	}
	return os.MkdirAll(path, 0o700)
}

func (w *ingestAcceptanceWorld) reportedFamilies(field string) ([]string, error) {
	raw, ok := w.last.doc[field].([]any)
	if !ok {
		return nil, fmt.Errorf("%s is not a list: %v", field, w.last.doc)
	}
	families := make([]string, 0, len(raw))
	for _, value := range raw {
		families = append(families, fmt.Sprint(value))
	}
	return families, nil
}
