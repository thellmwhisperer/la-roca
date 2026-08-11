//go:build acceptance

package acceptance

import (
	"fmt"
	"os"

	"github.com/cucumber/godog"
)

func registerIngestIncrementalSteps(ctx *godog.ScenarioContext, w *ingestAcceptanceWorld) {
	ctx.Given(`^a Claude session has already been ingested$`, func() error {
		if err := w.seedClaudeSession("incremental", 1, false, ""); err != nil {
			return err
		}
		return w.runIngest(false)
	})
	ctx.Given(`^the unchanged session file can no longer be opened$`, func() error {
		return os.Chmod(w.fixturePath, 0)
	})
	ctx.Given(`^a Claude session is ready to ingest$`, func() error {
		return w.seedClaudeSession("repeat", 1, false, "")
	})
	ctx.Given(`^a Claude session with one exchange has already been ingested$`, func() error {
		if err := w.seedClaudeSession("growing", 1, false, ""); err != nil {
			return err
		}
		return w.runIngest(false)
	})
	ctx.When(`^I run ingest again$`, func() error { return w.runIngest(false) })
	ctx.When(`^I run ingest twice in a row$`, func() error {
		if err := w.runIngest(false); err != nil {
			return err
		}
		return w.runIngest(false)
	})
	ctx.When(`^a second exchange is appended and I run ingest again$`, func() error {
		if err := w.appendClaudeExchange(); err != nil {
			return err
		}
		return w.runIngest(false)
	})
	ctx.Then(`^the file is skipped by fingerprint without an error$`, func() error {
		skipped, errors, _, err := w.reportFileCounts()
		if err != nil {
			return err
		}
		if skipped != 1 || errors != 0 {
			return fmt.Errorf("files_skipped=%d errors=%d, want 1 and 0", skipped, errors)
		}
		return nil
	})
	ctx.Then(`^the second ingest has a zero delta$`, func() error {
		return w.expectDelta(map[string]int{})
	})
	ctx.Then(`^the normalized row counts are unchanged$`, func() error {
		for table, before := range w.countsBefore {
			if w.countsAfter[table] != before {
				return fmt.Errorf("%s changed from %d to %d", table, before, w.countsAfter[table])
			}
		}
		return nil
	})
	ctx.Then(`^exactly one exchange is added$`, func() error {
		return w.expectDelta(map[string]int{"exchanges": 1})
	})
	ctx.Then(`^no session, thinking block, tool call or memory is added$`, func() error {
		return w.expectDelta(map[string]int{
			"sessions": 0, "exchanges": 1, "thinking_blocks": 0, "tool_uses": 0, "memories": 0,
		})
	})
}

func (w *ingestAcceptanceWorld) expectDelta(want map[string]int) error {
	delta, ok := w.last.doc["delta"].(map[string]any)
	if !ok {
		return fmt.Errorf("delta is not an object: %v", w.last.doc)
	}
	for _, key := range []string{"memories", "sessions", "exchanges", "thinking_blocks", "tool_uses"} {
		// An unchecked assertion here panicked inside the step, which reads as a
		// broken harness rather than the report field that is missing.
		number, ok := delta[key].(float64)
		if !ok {
			return fmt.Errorf("delta.%s is missing or not a number: %v", key, delta[key])
		}
		if got, expected := int(number), want[key]; got != expected {
			return fmt.Errorf("delta.%s=%d, want %d", key, got, expected)
		}
	}
	return nil
}
