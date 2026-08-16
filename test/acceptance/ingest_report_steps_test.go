//go:build acceptance

package acceptance

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/cucumber/godog"
)

func registerIngestReportSteps(ctx *godog.ScenarioContext, w *ingestAcceptanceWorld) {
	ctx.Given(`^Claude and Codex artefacts are ready to ingest$`, w.seedClaudeAndCodex)
	ctx.Given(`^one unchanged session and one malformed session are ready to ingest$`, w.seedSkippedAndMalformed)
	ctx.Given(`^a Claude session with (\d+) malformed records is ready to ingest$`, w.seedMalformedRecords)
	ctx.When(`^I run ingest as a dry-run$`, func() error {
		hash, err := w.databaseBytes()
		if err != nil {
			return err
		}
		w.databaseHash = hash
		return w.runIngest(true)
	})
	ctx.When(`^I run ingest for a human$`, w.runHumanIngest)
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
		skipped, errors, details, err := w.reportFileCounts()
		if err != nil {
			return err
		}
		if skipped != 1 || errors != 1 || details != errors {
			return fmt.Errorf("skipped=%d errors=%d details=%d, want 1/1/1", skipped, errors, details)
		}
		return nil
	})
	ctx.Then(`^the summary reports (\d+) record discards with reasons$`, func(want int) error {
		got, err := ingestJSONNumber(w.last.doc, "records_discarded")
		if err != nil {
			return err
		}
		details, _ := w.last.doc["discard_details"].([]any)
		if got != want || len(details) != want {
			return fmt.Errorf("records_discarded=%d details=%d, want %d", got, len(details), want)
		}
		for _, raw := range details {
			detail, _ := raw.(map[string]any)
			if detail["path"] == "" || detail["parser"] == "" || detail["reason"] == "" {
				return fmt.Errorf("discard has no path, parser or reason: %v", detail)
			}
		}
		return nil
	})
	ctx.Then(`^each seeded source is summarized once with its elapsed time$`, func() error {
		for _, source := range []string{"claude-code", "codex"} {
			pattern := regexp.MustCompile(`(?m)^  ✓ ` + source + ` .* · [^·]+(?:ms|s)$`)
			if matches := pattern.FindAllString(w.last.stdout, -1); len(matches) != 1 {
				return fmt.Errorf("source %q summary count=%d:\n%s", source, len(matches), w.last.stdout)
			}
		}
		return nil
	})
	ctx.Then(`^index time and the product total are each reported once$`, func() error {
		for _, prefix := range []string{"index:", "total:"} {
			if count := countLinesWithPrefix(w.last.stdout, prefix); count != 1 {
				return fmt.Errorf("%s line count=%d:\n%s", prefix, count, w.last.stdout)
			}
		}
		return nil
	})
	ctx.Then(`^one coverage block closes the file accounting and explains the manifest skip$`, func() error {
		pattern := regexp.MustCompile(`(?m)^coverage: ([0-9,]+) seen · ([0-9,]+) claimed · ([0-9,]+) ingested · ([0-9,]+) skipped$`)
		matches := pattern.FindAllStringSubmatch(w.last.stdout, -1)
		if len(matches) != 1 {
			return fmt.Errorf("coverage block count=%d:\n%s", len(matches), w.last.stdout)
		}
		var seen, claimed, ingested, skipped int
		if _, err := fmt.Sscanf(strings.ReplaceAll(strings.Join(matches[0][1:], " "), ",", ""),
			"%d %d %d %d", &seen, &claimed, &ingested, &skipped); err != nil {
			return err
		}
		if seen != ingested+skipped || claimed > seen {
			return fmt.Errorf("coverage does not close: seen=%d claimed=%d ingested=%d skipped=%d",
				seen, claimed, ingested, skipped)
		}
		if !strings.Contains(w.last.stdout,
			"skipped: Claude memory completeness manifest is not corpus content · 1") {
			return fmt.Errorf("coverage does not explain the manifest skip:\n%s", w.last.stdout)
		}
		return nil
	})
	ctx.Then(`^the Claude source summary reports (\d+) discarded records$`, func(want int) error {
		needle := fmt.Sprintf(" · %d discarded · ", want)
		for _, line := range strings.Split(w.last.stdout, "\n") {
			if strings.HasPrefix(line, "  ✓ claude-code ") && strings.Contains(line, needle) {
				return nil
			}
		}
		return fmt.Errorf("Claude summary has no %q:\n%s", needle, w.last.stdout)
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

func (w *ingestAcceptanceWorld) runHumanIngest() error {
	before, err := w.tableCounts()
	if err != nil {
		return err
	}
	w.countsBefore = before
	result, err := w.runCommand("ingest", "--db-path", w.dbPath)
	if err != nil {
		return err
	}
	after, countErr := w.tableCounts()
	w.countsAfter = after
	if countErr != nil {
		return countErr
	}
	if result.code != 0 {
		return fmt.Errorf("human ingest exited %d: %s", result.code, result.stderr)
	}
	return nil
}

func countLinesWithPrefix(output, prefix string) int {
	count := 0
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, prefix) {
			count++
		}
	}
	return count
}

func (w *ingestAcceptanceWorld) seedClaudeAndCodex() error {
	if err := w.seedClaudeSession("report", 1, false, ""); err != nil {
		return err
	}
	if err := writeFixture(filepath.Join(w.home, ".claude", "projects", encodeAgentPath(filepath.Join(w.home, "workspace", "report")), "memory", "report.md"), "report memory\n"); err != nil {
		return err
	}
	if err := writeFixture(filepath.Join(w.home, ".claude", "projects", encodeAgentPath(filepath.Join(w.home, "workspace", "report")), "memory", "MEMORY.md"), "- [report](report.md)\n"); err != nil {
		return err
	}
	if err := w.seedCodexSession(""); err != nil {
		return err
	}
	return writeFixture(filepath.Join(w.home, ".codex", "memories", "report.md"), "codex report memory\n")
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
	return writeFixture(path, "malformed acceptance input\n")
}

func (w *ingestAcceptanceWorld) seedMalformedRecords(count int) error {
	if err := w.seedClaudeSession("record-discards", 1, false, ""); err != nil {
		return err
	}
	raw, err := os.ReadFile(w.fixturePath)
	if err != nil {
		return err
	}
	for i := 0; i < count; i++ {
		raw = append(raw, []byte(fmt.Sprintf("malformed record %d\n", i+1))...)
	}
	return os.WriteFile(w.fixturePath, raw, 0o600)
}

// The rollout an operator really has: the conversation plus the runtime
// machinery around it, which this build reads part of and leaves the rest of out
// by design.
func (w *ingestAcceptanceWorld) seedCodexMachineryRollout() error {
	cwd := filepath.Join(w.home, "workspace", "codex-provenance")
	body := fmt.Sprintf(`{"type":"session_meta","timestamp":"2026-08-01T11:00:00Z","payload":{"id":%q,"cwd":%q,"timestamp":"2026-08-01T11:00:00Z","model_provider":"acceptance-provider"}}
{"type":"turn_context","timestamp":"2026-08-01T11:00:01Z","payload":{"turn_id":"turn-1","model":"acceptance-model","effort":"high","summary":"auto"}}
{"type":"event_msg","timestamp":"2026-08-01T11:00:02Z","payload":{"type":"task_started","turn_id":"turn-1"}}
{"type":"event_msg","timestamp":"2026-08-01T11:00:03Z","payload":{"type":"user_message","message":"question"}}
{"type":"event_msg","timestamp":"2026-08-01T11:00:04Z","payload":{"type":"agent_reasoning","text":"the machinery is not the conversation"}}
{"type":"event_msg","timestamp":"2026-08-01T11:00:05Z","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":40,"output_tokens":6,"reasoning_output_tokens":3}}}}
{"type":"event_msg","timestamp":"2026-08-01T11:00:06Z","payload":{"type":"task_complete","last_agent_message":"answer"}}
`, codexAcceptanceSession, cwd)
	return w.writeCodexRollout(body)
}

func registerIngestProvenanceSteps(ctx *godog.ScenarioContext, w *ingestAcceptanceWorld) {
	ctx.Given(`^a Codex rollout with runtime machinery is ready to ingest$`, w.seedCodexMachineryRollout)
	ctx.Then(`^the summary names the exclusions by reason and prints no absolute path$`, func() error {
		if !strings.Contains(w.last.stdout, "left out by design") {
			return fmt.Errorf("the summary does not name its exclusions:\n%s", w.last.stdout)
		}
		if strings.Contains(w.last.stdout, w.fixturePath) {
			return fmt.Errorf("the default summary printed an artefact path:\n%s", w.last.stdout)
		}
		return nil
	})
	ctx.Then(`^the exchange carries the model, the provider and the token counts of the rollout$`, func() error {
		db, err := w.openDB()
		if err != nil {
			return err
		}
		defer db.Close()
		var model, provider string
		var in, out, reasoning int
		err = db.QueryRow(`SELECT model, provider, tokens_in, tokens_out, tokens_reasoning
			FROM exchanges WHERE session_id = ?`, w.sessionID).
			Scan(&model, &provider, &in, &out, &reasoning)
		if err != nil {
			return fmt.Errorf("read the provenance of %s: %w", w.sessionID, err)
		}
		if model != "acceptance-model" || provider != "acceptance-provider" {
			return fmt.Errorf("model/provider = %q/%q", model, provider)
		}
		if in != 40 || out != 6 || reasoning != 3 {
			return fmt.Errorf("tokens in/out/reasoning = %d/%d/%d, want 40/6/3", in, out, reasoning)
		}
		return nil
	})
}
