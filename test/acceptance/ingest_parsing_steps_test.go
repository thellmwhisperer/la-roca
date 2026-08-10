//go:build acceptance

package acceptance

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/cucumber/godog"
)

const instructionSentinel = "instruction sentinel must never enter memory"

func registerIngestParsingSteps(ctx *godog.ScenarioContext, w *ingestAcceptanceWorld) {
	ctx.Given(`^a Claude session with reasoning and a tool call is ready to ingest$`, func() error {
		return w.seedClaudeSession("parsing", 1, true, "")
	})
	ctx.Given(`^a "([^"]*)" memory file with durable content is ready to ingest$`, w.seedMemoryFile)
	ctx.Given(`^a Codex session is ready to ingest$`, func() error { return w.seedCodexSession("") })
	ctx.Given(`^a markdown memory with declared metadata is ready to ingest$`, func() error {
		workspace := filepath.Join(w.home, "workspace")
		if err := w.writeConfig(workspace); err != nil {
			return err
		}
		path := filepath.Join(w.home, ".claude", "projects", encodeAgentPath(filepath.Join(workspace, "metadata")), "memory", "declared.md")
		return writeIngestFixture(path, "---\nname: declared-name\ntype: feedback\ndescription: declared-description\n---\nDurable markdown body.\n")
	})
	ctx.Given(`^a malformed Claude session file is ready to ingest$`, w.seedMalformedClaude)
	ctx.Given(`^instruction files and one ordinary session are present$`, w.seedInstructionFiles)
	ctx.When(`^I run ingest$`, func() error { return w.runIngest(false) })
	ctx.Then(`^one session, one exchange, one thinking block and one tool call exist$`, func() error {
		want := map[string]int{"sessions": 1, "exchanges": 1, "thinking_blocks": 1, "tool_uses": 1}
		for table, expected := range want {
			got, err := w.queryInt("SELECT COUNT(*) FROM " + table)
			if err != nil {
				return err
			}
			if got != expected {
				return fmt.Errorf("%s=%d, want %d", table, got, expected)
			}
		}
		return nil
	})
	ctx.Then(`^one memory from "([^"]*)" exists$`, func(family string) error {
		pattern := family
		if family == "claude" {
			pattern = "claude%"
		}
		got, err := w.queryInt("SELECT COUNT(*) FROM memories WHERE source_agent LIKE ? AND content = 'durable family memory'", pattern)
		if err != nil {
			return err
		}
		if got != 1 {
			return fmt.Errorf("%s durable memories=%d, want 1", family, got)
		}
		return nil
	})
	ctx.Then(`^one Codex session and one exchange exist$`, func() error {
		got, err := w.queryInt(`SELECT COUNT(*) FROM sessions s JOIN exchanges e USING (session_id) WHERE s.source_agent = 'codex'`)
		if err != nil {
			return err
		}
		if got != 1 {
			return fmt.Errorf("Codex session/exchange pairs=%d, want 1", got)
		}
		return nil
	})
	ctx.Then(`^its content and declared metadata exist on one memory$`, func() error {
		got, err := w.queryInt(`SELECT COUNT(*) FROM memories WHERE content = 'Durable markdown body.' AND layer = 'feedback' AND json_extract(metadata, '$.memory_name') = 'declared-name' AND json_extract(metadata, '$.memory_description') = 'declared-description'`)
		if err != nil {
			return err
		}
		if got != 1 {
			return fmt.Errorf("markdown memories with declared metadata=%d, want 1", got)
		}
		return nil
	})
	ctx.Then(`^the command succeeds and counts one malformed file$`, func() error {
		errors, err := ingestJSONNumber(w.last.doc, "errors")
		if err != nil {
			return err
		}
		details, _ := w.last.doc["error_details"].([]any)
		if w.last.code != 0 || errors != 1 || len(details) != 1 {
			return fmt.Errorf("code=%d errors=%d details=%d: %s", w.last.code, errors, len(details), w.last.stderr)
		}
		return nil
	})
	ctx.Then(`^no instruction file is recorded as content or ingest state$`, func() error {
		memories, err := w.queryInt("SELECT COUNT(*) FROM memories WHERE content LIKE ?", "%"+instructionSentinel+"%")
		if err != nil {
			return err
		}
		states, err := w.queryInt("SELECT COUNT(*) FROM ingest_file_state WHERE path LIKE '%CLAUDE.md' OR path LIKE '%AGENTS.md'")
		if err != nil {
			return err
		}
		if memories != 0 || states != 0 {
			return fmt.Errorf("instruction memories=%d ingest states=%d, want zero", memories, states)
		}
		return nil
	})
}

func (w *ingestAcceptanceWorld) seedMemoryFile(family string) error {
	switch family {
	case "claude":
		workspace := filepath.Join(w.home, "workspace")
		if err := w.writeConfig(workspace); err != nil {
			return err
		}
		return writeIngestFixture(filepath.Join(w.home, ".claude", "projects", encodeAgentPath(filepath.Join(workspace, "memory-project")), "memory", "durable.md"), "durable family memory\n")
	case "codex":
		return writeIngestFixture(filepath.Join(w.home, ".codex", "memories", "durable.md"), "durable family memory\n")
	default:
		return fmt.Errorf("no memory fixture for family %q", family)
	}
}

func (w *ingestAcceptanceWorld) seedMalformedClaude() error {
	w.sessionID = "99999999-8888-7777-6666-555555555555"
	w.fixturePath = filepath.Join(w.home, ".claude", "projects", "malformed", w.sessionID+".jsonl")
	return writeIngestFixture(w.fixturePath, "this is not json\n{nor is this}\n")
}

func (w *ingestAcceptanceWorld) seedInstructionFiles() error {
	if err := w.seedClaudeSession("instructions", 1, false, ""); err != nil {
		return err
	}
	paths := []string{
		filepath.Join(w.home, ".claude", "CLAUDE.md"),
		filepath.Join(w.home, ".codex", "AGENTS.md"),
		filepath.Join(w.home, "workspace", "instructions", "CLAUDE.md"),
		filepath.Join(w.home, "workspace", "instructions", "AGENTS.md"),
	}
	for _, path := range paths {
		if err := writeIngestFixture(path, "# Instructions\n\n"+instructionSentinel+"\n"); err != nil {
			return err
		}
	}
	return nil
}

func containsFold(values []string, want string) bool {
	for _, value := range values {
		if strings.EqualFold(value, want) {
			return true
		}
	}
	return false
}
