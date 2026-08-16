//go:build acceptance

package acceptance

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/cucumber/godog"
)

func registerIngestAttributionSteps(ctx *godog.ScenarioContext, w *ingestAcceptanceWorld) {
	ctx.Given(`^a Claude session path declares project "([^"]*)"$`, func(project string) error {
		return w.seedClaudeSession(project, 1, false, "")
	})
	ctx.Given(`^a Claude session path has no recognizable project$`, func() error {
		return w.seedClaudeSession("", 1, false, "")
	})
	ctx.Given(`^one session from every supported agent family is ready to ingest$`, w.seedEverySupportedSession)
	ctx.Given(`^a "([^"]*)" session declares model "([^"]*)"$`, w.seedModelSession)
	ctx.Then(`^that session belongs to project "([^"]*)"$`, func(project string) error {
		got, err := w.queryInt("SELECT COUNT(*) FROM sessions WHERE session_id = ? AND project = ?", w.sessionID, project)
		if err != nil {
			return err
		}
		if got != 1 {
			return fmt.Errorf("session %s does not belong to project %q", w.sessionID, project)
		}
		return nil
	})
	ctx.Then(`^that session has no project$`, func() error {
		got, err := w.queryInt("SELECT COUNT(*) FROM sessions WHERE session_id = ? AND project IS NULL", w.sessionID)
		if err != nil {
			return err
		}
		if got != 1 {
			return fmt.Errorf("session %s is not global", w.sessionID)
		}
		return nil
	})
	ctx.Then(`^the ingest report explains the global attribution$`, func() error {
		warnings, _ := w.last.doc["warnings"].([]any)
		for _, warning := range warnings {
			if strings.Contains(fmt.Sprint(warning), "stored with no project") {
				return nil
			}
		}
		return fmt.Errorf("report does not explain global attribution: %v", warnings)
	})
	ctx.Then(`^every session source is one of the supported agent families$`, func() error {
		families, err := w.queryStrings("SELECT DISTINCT source_agent FROM sessions ORDER BY source_agent")
		if err != nil {
			return err
		}
		for _, family := range families {
			if !slices.Contains(supportedIngestFamilies, family) {
				return fmt.Errorf("session source %q is outside the supported roster", family)
			}
		}
		return nil
	})
	ctx.Then(`^every supported agent family owns a session$`, func() error {
		families, err := w.queryStrings("SELECT DISTINCT source_agent FROM sessions ORDER BY source_agent")
		if err != nil {
			return err
		}
		for _, family := range supportedIngestFamilies {
			if !slices.Contains(families, family) {
				return fmt.Errorf("supported family %q owns no session: %v", family, families)
			}
		}
		return nil
	})
	ctx.Then(`^every supported agent family uses its canonical harness$`, func() error {
		db, err := w.openDB()
		if err != nil {
			return err
		}
		defer db.Close()
		rows, err := db.Query("SELECT DISTINCT source_agent, source_surface FROM sessions")
		if err != nil {
			return err
		}
		defer rows.Close()
		want := map[string]string{
			"claude": "Claude Code", "claude-desktop": "Claude Desktop",
			"cowork": "Cowork", "codex": "Codex CLI", "opencode": "OpenCode",
			"pi": "Pi", "hermes": "Hermes", "grok": "Grok Build",
		}
		for rows.Next() {
			var family, harness string
			if err := rows.Scan(&family, &harness); err != nil {
				return err
			}
			if harness != want[family] {
				return fmt.Errorf("family %q harness=%q, want %q", family, harness, want[family])
			}
			delete(want, family)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if len(want) != 0 {
			return fmt.Errorf("canonical harnesses not exercised: %v", want)
		}
		return nil
	})
	ctx.Then(`^that session records model "([^"]*)"$`, func(model string) error {
		db, err := w.openDB()
		if err != nil {
			return err
		}
		defer db.Close()
		var raw string
		if err := db.QueryRow("SELECT metadata FROM sessions WHERE session_id = ?", w.sessionID).Scan(&raw); err != nil {
			return err
		}
		var metadata map[string]any
		if err := json.Unmarshal([]byte(raw), &metadata); err != nil {
			return err
		}
		if fmt.Sprint(metadata["model"]) != model {
			return fmt.Errorf("session model=%v, want %q (metadata=%s)", metadata["model"], model, raw)
		}
		return nil
	})
}

func (w *ingestAcceptanceWorld) seedEverySupportedSession() error {
	builders := []func() error{
		func() error { return w.seedClaudeSession("claude-project", 1, false, "") },
		func() error { return w.seedDesktopSession("") },
		w.seedCoworkSession,
		func() error { return w.seedCodexSession("") },
		w.seedOpenCodeSession,
		w.seedPiSession,
		func() error { return w.seedHermesSession("") },
		w.seedGrokSession,
	}
	for _, build := range builders {
		if err := build(); err != nil {
			return err
		}
	}
	return nil
}

func (w *ingestAcceptanceWorld) seedModelSession(family, model string) error {
	switch family {
	case "claude":
		return w.seedClaudeSession("model-project", 1, false, model)
	case "claude-desktop":
		return w.seedDesktopSession(model)
	case "codex":
		return w.seedCodexSession(model)
	case "hermes":
		return w.seedHermesSession(model)
	case "grok":
		return w.seedGrokSessionWithModel(model)
	default:
		return fmt.Errorf("no model-declaring fixture for %q", family)
	}
}
