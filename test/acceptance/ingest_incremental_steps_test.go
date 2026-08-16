//go:build acceptance

package acceptance

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

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
	ctx.Given(`^an extracted Anthropic export is ready to ingest$`, func() error {
		export := filepath.Join(w.home, "declared-anthropic-export")
		w.exportPath = export
		return copyAnthropicExportFixture(export)
	})
	ctx.Given(`^an extracted Anthropic export with project surfaces is ready to ingest$`, func() error {
		export := filepath.Join(w.home, "declared-anthropic-export-projects")
		w.exportPath = export
		return copyExportTree(export, "anthropic-export-projects")
	})
	ctx.Given(`^an extracted OpenAI export is ready to ingest$`, func() error {
		export := filepath.Join(w.home, "declared-openai-export-v1")
		w.exportPath = export
		if err := copyOpenAIExportFixture(export, "openai-export-v1"); err != nil {
			return err
		}
		return nil
	})
	ctx.Given(`^an extracted OpenAI export with project chats is ready to ingest$`, func() error {
		export := filepath.Join(w.home, "declared-openai-export-projects")
		w.exportPath = export
		return copyOpenAIExportFixture(export, "openai-export-projects")
	})
	ctx.Given(`^an extracted sharded OpenAI export is ready to ingest$`, func() error {
		export := filepath.Join(w.home, "declared-openai-export-sharded")
		w.exportPath = export
		if err := copyOpenAIExportFixture(export, "openai-export-sharded"); err != nil {
			return err
		}
		return nil
	})
	ctx.Given(`^an extracted export directory has no conversation layout$`, func() error {
		export := filepath.Join(w.home, "unrecognized-account-export")
		w.exportPath = export
		return os.MkdirAll(export, 0o700)
	})
	ctx.Given(`^standing export paths remain in config$`, func() error {
		anthropic := filepath.Join(w.home, "leftover-anthropic-export")
		openai := filepath.Join(w.home, "leftover-openai-export")
		if err := copyOpenAIExportFixture(openai, "openai-export-v1"); err != nil {
			return err
		}
		if err := copyAnthropicExportFixture(anthropic); err != nil {
			return err
		}
		return writeFixture(filepath.Join(w.home, ".roca", "config.toml"), fmt.Sprintf(
			"[defaults]\nanthropic_export_paths = [%q]\nopenai_export_paths = [%q]\n", anthropic, openai))
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
	ctx.When(`^I run ingest with the export path$`, func() error {
		return w.runExportIngest(false)
	})
	ctx.When(`^I run ingest with the export path and it is refused$`, func() error {
		result, err := w.runCommand("ingest", w.exportPath, "--db-path", w.dbPath)
		if err != nil {
			return err
		}
		if result.code == 0 {
			return fmt.Errorf("ingest accepted %q: %s", w.exportPath, result.stdout)
		}
		return nil
	})
	ctx.When(`^I run ingest with the export path twice in a row$`, func() error {
		if err := w.runExportIngest(false); err != nil {
			return err
		}
		return w.runExportIngest(false)
	})
	ctx.When(`^a second exchange is appended and I run ingest again$`, func() error {
		if err := w.appendClaudeExchange(); err != nil {
			return err
		}
		return w.runIngest(false)
	})
	ctx.When(`^I select the newer OpenAI export and run ingest$`, func() error {
		export := filepath.Join(w.home, "declared-openai-export-v2")
		w.exportPath = export
		if err := copyOpenAIExportFixture(export, "openai-export-v2"); err != nil {
			return err
		}
		return w.runExportIngest(false)
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
	ctx.Then(`^the explicit Anthropic export is ingested$`, func() error {
		for query, want := range map[string]int{
			`SELECT COUNT(*) FROM sessions WHERE source_agent = 'claude-web'`: 2,
			`SELECT COUNT(*) FROM exchanges e JOIN sessions s ON s.session_id = e.session_id
			  WHERE s.source_agent = 'claude-web'`: 4,
			`SELECT COUNT(*) FROM memories WHERE source_agent = 'claude-web'
			  AND layer = 'user' AND origin = 'cron'`: 1,
		} {
			got, err := w.queryInt(query)
			if err != nil {
				return err
			}
			if got != want {
				return fmt.Errorf("query %q returned %d, want %d", query, got, want)
			}
		}
		return nil
	})
	ctx.Then(`^doctor reports the export's older date as bedrock$`, func() error {
		result, err := w.runCommand("doctor", "--db-path", w.dbPath, "--json")
		if err != nil {
			return err
		}
		bedrock, ok := result.doc["bedrock"].(map[string]any)
		if !ok || bedrock["timestamp"] != "2025-04-02T07:00:00.000Z" {
			return fmt.Errorf("doctor bedrock = %v", result.doc["bedrock"])
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
	ctx.Then(`^only the new ChatGPT conversations and messages are added$`, func() error {
		if err := w.expectDelta(map[string]int{"sessions": 1, "exchanges": 2}); err != nil {
			return err
		}
		for query, want := range map[string]int{
			`SELECT COUNT(*) FROM sessions WHERE source_agent = 'chatgpt-web'`: 2,
			`SELECT COUNT(*) FROM exchanges e JOIN sessions s ON s.session_id = e.session_id
			  WHERE s.source_agent = 'chatgpt-web'`: 4,
		} {
			got, err := w.queryInt(query)
			if err != nil {
				return err
			}
			if got != want {
				return fmt.Errorf("query %q returned %d, want %d", query, got, want)
			}
		}
		return nil
	})
	ctx.Then(`^Claude project entities documents and memories land$`, func() error {
		for query, want := range map[string]int{
			`SELECT COUNT(*) FROM memories WHERE source_agent = 'claude-web' AND layer = 'project'`:                                4,
			`SELECT COUNT(*) FROM memories WHERE source_agent = 'claude-web' AND layer = 'user' AND origin = 'cron'`:               5,
			`SELECT COUNT(*) FROM memories WHERE source_agent = 'claude-web' AND project = 'aaaaaaaa-0000-4000-8000-000000000001'`: 4,
		} {
			if err := expectQueryCount(w, query, want); err != nil {
				return err
			}
		}
		return nil
	})
	ctx.Then(`^ordinary Claude conversations stay unprojected$`, func() error {
		return expectQueryCount(w, `SELECT COUNT(*) FROM sessions
			WHERE session_id = '10000000-0000-4000-8000-000000000099'
			  AND source_agent = 'claude-web' AND project IS NULL`, 1)
	})
	ctx.Then(`^the design chat keeps its project uuid$`, func() error {
		return expectQueryCount(w, `SELECT COUNT(*) FROM sessions
			WHERE session_id = 'cccccccc-0000-4000-8000-000000000001'
			  AND project = 'aaaaaaaa-0000-4000-8000-000000000001'`, 1)
	})
	ctx.Then(`^ChatGPT snorlax chats share a virtual project keyed by gizmo id$`, func() error {
		return expectQueryCount(w, `SELECT COUNT(*) FROM sessions
			WHERE source_agent = 'chatgpt-web'
			  AND project = 'g-p-syntheticorchard000000000000'`, 2)
	})
	ctx.Then(`^Custom GPT chats stay unprojected$`, func() error {
		return expectQueryCount(w, `SELECT COUNT(*) FROM sessions
			WHERE session_id IN (
			  '50000000-0000-4000-8000-000000000003',
			  '50000000-0000-4000-8000-000000000004'
			) AND project IS NULL`, 2)
	})
	ctx.Then(`^the ChatGPT exchanges retain OpenAI provenance$`, func() error {
		return expectQueryCount(w, `SELECT COUNT(*) FROM exchanges e
			JOIN sessions s ON s.session_id = e.session_id
			WHERE s.source_agent = 'chatgpt-web' AND e.provider = 'openai'
			  AND e.tokens_in IS NULL AND e.tokens_out IS NULL`, 4)
	})
	ctx.Then(`^every ChatGPT shard is ingested with OpenAI provenance$`, func() error {
		if err := w.expectDelta(map[string]int{"sessions": 2, "exchanges": 3}); err != nil {
			return err
		}
		return expectQueryCount(w, `SELECT COUNT(*) FROM exchanges e
			JOIN sessions s ON s.session_id = e.session_id
			WHERE s.source_agent = 'chatgpt-web' AND e.provider = 'openai'
			  AND e.model IS NOT NULL`, 3)
	})
	ctx.Then(`^ingest names the directory and both export layouts$`, func() error {
		refusal := w.last.stdout + w.last.stderr
		for _, want := range []string{w.exportPath, "memories.json", "conversations-*.json"} {
			if !strings.Contains(refusal, want) {
				return fmt.Errorf("refusal does not name %q: %s", want, refusal)
			}
		}
		return nil
	})
	ctx.Then(`^no standing export is scanned$`, func() error {
		for _, source := range []string{"claude_web_export_files", "chatgpt_web_export_files"} {
			got, err := ingestJSONNumber(w.last.doc, "scanned", source)
			if err != nil {
				return err
			}
			if got != 0 {
				return fmt.Errorf("scanned.%s=%d, want 0", source, got)
			}
		}
		return expectQueryCount(w, `SELECT COUNT(*) FROM sessions
			WHERE source_agent IN ('claude-web', 'chatgpt-web')`, 0)
	})
}

func copyAnthropicExportFixture(target string) error {
	return copyExportFixture(target, "anthropic-export",
		[]string{"conversations.json", "memories.json"})
}

func copyOpenAIExportFixture(target, fixtureName string) error {
	fixture := filepath.Join("..", "..", "internal", "ingest", "testdata", fixtureName)
	entries, err := os.ReadDir(fixture)
	if err != nil {
		return err
	}
	var names []string
	for _, entry := range entries {
		if !entry.Type().IsRegular() {
			continue
		}
		names = append(names, entry.Name())
	}
	return copyExportFixture(target, fixtureName, names)
}

func copyExportTree(target, fixtureName string) error {
	fixture := filepath.Join("..", "..", "internal", "ingest", "testdata", fixtureName)
	return filepath.WalkDir(fixture, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(fixture, path)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return os.MkdirAll(filepath.Join(target, rel), 0o700)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return writeFixture(filepath.Join(target, rel), string(raw))
	})
}

func copyExportFixture(target, fixtureName string, names []string) error {
	fixture := filepath.Join("..", "..", "internal", "ingest", "testdata", fixtureName)
	for _, name := range names {
		raw, err := os.ReadFile(filepath.Join(fixture, name))
		if err != nil {
			return err
		}
		if err := writeFixture(filepath.Join(target, name), string(raw)); err != nil {
			return err
		}
	}
	return nil
}

func expectQueryCount(w *ingestAcceptanceWorld, query string, want int) error {
	got, err := w.queryInt(query)
	if err != nil {
		return err
	}
	if got != want {
		return fmt.Errorf("query %q returned %d, want %d", query, got, want)
	}
	return nil
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
