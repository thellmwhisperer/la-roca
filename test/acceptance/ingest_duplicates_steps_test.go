//go:build acceptance

package acceptance

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/cucumber/godog"
)

func registerIngestDuplicateSteps(ctx *godog.ScenarioContext, w *ingestAcceptanceWorld) {
	ctx.Given(`^an extracted Anthropic export with two sibling sessions is ready to ingest$`, func() error {
		export := filepath.Join(w.home, "declared-anthropic-export-duplicates")
		w.exportPath = export
		return copyExportTree(export, "anthropic-export-duplicates")
	})
	ctx.When(`^I run doctor$`, func() error {
		before, err := w.tableCounts()
		if err != nil {
			return err
		}
		result, err := w.runCommand("doctor", "--db-path", w.dbPath, "--json")
		if err != nil {
			return err
		}
		if result.code != 0 || result.doc == nil {
			return fmt.Errorf("doctor code %d: %s%s", result.code, result.stdout, result.stderr)
		}
		after, err := w.tableCounts()
		w.countsBefore, w.countsAfter = before, after
		return err
	})
	ctx.Then(`^doctor warns of 1 extra row in 1 group with the SELECT that lists them$`, func() error {
		warnings := duplicateSessionWarnings(w.last.doc)
		if len(warnings) != 1 || !strings.Contains(warnings[0], "1 extra row in 1 group") {
			return fmt.Errorf("duplicate-sessions warnings = %q", warnings)
		}
		_, listing, ok := strings.Cut(warnings[0], `roca exec `)
		if !ok {
			return fmt.Errorf("warning carries no SELECT: %q", warnings[0])
		}
		other := filepath.Join(w.home, "other-installation")
		if err := writeFixture(filepath.Join(other, "config.toml"), "[layout]\nserving = \"legacy-serving\"\n"); err != nil {
			return err
		}
		command := exec.Command("/bin/sh", "-c", `exec "$0" exec `+listing, w.binary)
		command.Env = []string{
			"HOME=" + w.home,
			"ROCA_DB_PATH=" + filepath.Join(other, "roca.db"),
			"PATH=/usr/bin:/bin:/usr/sbin:/sbin",
			"TMPDIR=" + filepath.Join(w.home, "tmp"),
		}
		if directory := os.Getenv("GOCOVERDIR"); directory != "" {
			command.Env = append(command.Env, "GOCOVERDIR="+directory)
		}
		output, err := command.CombinedOutput()
		for _, id := range []string{"60000000-0000-4000-8000-000000000010", "60000000-0000-4000-8000-000000000011"} {
			if err != nil || !strings.Contains(string(output), id) {
				return fmt.Errorf("listing SELECT does not list %s: %v: %s", id, err, output)
			}
		}
		return nil
	})
	ctx.Then(`^doctor carries no duplicate-sessions warning$`, func() error {
		if warnings := duplicateSessionWarnings(w.last.doc); len(warnings) != 0 {
			return fmt.Errorf("unexpected duplicate-sessions warnings: %q", warnings)
		}
		return nil
	})
	ctx.Then(`^the corpus session and exchange counts are the same before and after doctor$`, func() error {
		if w.countsBefore["sessions"] != 2 || w.countsBefore["exchanges"] != 2 {
			return fmt.Errorf("counts before doctor = %v, want 2 sessions and 2 exchanges", w.countsBefore)
		}
		for _, table := range []string{"sessions", "exchanges"} {
			if w.countsAfter[table] != w.countsBefore[table] {
				return fmt.Errorf("doctor changed %s from %d to %d", table, w.countsBefore[table], w.countsAfter[table])
			}
		}
		return nil
	})
}

func duplicateSessionWarnings(doc map[string]any) []string {
	raw, _ := doc["warnings"].([]any)
	var found []string
	for _, warning := range raw {
		if text, _ := warning.(string); strings.HasPrefix(text, "duplicate sessions:") {
			found = append(found, text)
		}
	}
	return found
}
