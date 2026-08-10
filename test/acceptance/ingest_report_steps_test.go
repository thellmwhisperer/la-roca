//go:build acceptance

package acceptance

import (
	"fmt"
	"path/filepath"

	"github.com/cucumber/godog"
)

func registerIngestReportSteps(ctx *godog.ScenarioContext, w *ingestAcceptanceWorld) {
	ctx.Given(`^Claude and Codex artefacts are ready to ingest$`, w.seedClaudeAndCodex)
	ctx.Given(`^one unchanged session and one malformed session are ready to ingest$`, w.seedSkippedAndMalformed)
	ctx.When(`^I run ingest as a dry-run$`, func() error {
		hash, err := w.databaseBytes()
		if err != nil {
			return err
		}
		w.databaseHash = hash
		return w.runIngest(true)
	})
	ctx.Then(`^each seeded source has counts$`, func() error {
		sources, ok := w.last.doc["sources"].(map[string]any)
		if !ok {
			return fmt.Errorf("sources is not an object: %v", w.last.doc)
		}
		for _, family := range []string{"claude", "codex"} {
			counts, ok := sources[family].(map[string]any)
			if !ok || len(counts) == 0 {
				return fmt.Errorf("source %q has no counts: %v", family, sources)
			}
		}
		return nil
	})
	ctx.Then(`^the total delta equals the normalized rows added$`, func() error {
		mapping := map[string]string{
			"memories": "memories", "sessions": "sessions", "exchanges": "exchanges",
			"thinking_blocks": "thinking_blocks", "tool_uses": "tool_uses",
		}
		for field, table := range mapping {
			got, err := ingestJSONNumber(w.last.doc, "delta", field)
			if err != nil {
				return err
			}
			want := w.countsAfter[table] - w.countsBefore[table]
			if got != want {
				return fmt.Errorf("delta.%s=%d, normalized rows added=%d", field, got, want)
			}
		}
		return nil
	})
	ctx.Then(`^the summary counts every skipped file and error detail$`, func() error {
		skipped, err := ingestJSONNumber(w.last.doc, "files_skipped")
		if err != nil {
			return err
		}
		errors, err := ingestJSONNumber(w.last.doc, "errors")
		if err != nil {
			return err
		}
		details, _ := w.last.doc["error_details"].([]any)
		if skipped != 1 || errors != 1 || len(details) != errors {
			return fmt.Errorf("skipped=%d errors=%d details=%d, want 1/1/1", skipped, errors, len(details))
		}
		return nil
	})
	ctx.Then(`^pending files are reported$`, func() error {
		read, err := ingestJSONNumber(w.last.doc, "files_read")
		if err != nil {
			return err
		}
		if read < 1 || w.last.doc["dry_run"] != true {
			return fmt.Errorf("dry_run=%v files_read=%d, want true and at least one", w.last.doc["dry_run"], read)
		}
		return nil
	})
	ctx.Then(`^the database is byte-for-byte unchanged$`, func() error {
		after, err := w.databaseBytes()
		if err != nil {
			return err
		}
		if after != w.databaseHash {
			return fmt.Errorf("database bytes changed during dry-run")
		}
		return nil
	})
}

func (w *ingestAcceptanceWorld) seedClaudeAndCodex() error {
	if err := w.seedClaudeSession("report", 1, false, ""); err != nil {
		return err
	}
	if err := writeIngestFixture(filepath.Join(w.home, ".claude", "projects", encodeAgentPath(filepath.Join(w.home, "workspace", "report")), "memory", "report.md"), "report memory\n"); err != nil {
		return err
	}
	if err := w.seedCodexSession(""); err != nil {
		return err
	}
	return writeIngestFixture(filepath.Join(w.home, ".codex", "memories", "report.md"), "codex report memory\n")
}

func (w *ingestAcceptanceWorld) seedSkippedAndMalformed() error {
	if err := w.seedClaudeSession("report-errors", 1, false, ""); err != nil {
		return err
	}
	if err := w.runIngest(false); err != nil {
		return err
	}
	id := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	path := filepath.Join(w.home, ".claude", "projects", encodeAgentPath(filepath.Join(w.home, "workspace", "report-errors")), id+".jsonl")
	return writeIngestFixture(path, "malformed acceptance input\n")
}
