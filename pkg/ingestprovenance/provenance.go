// Package ingestprovenance exposes La Roca's canonical source-to-harness labels
// and historical provenance backfill.
package ingestprovenance

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

const (
	ClaudeCode    = "Claude Code"
	ClaudeDesktop = "Claude Desktop"
	ClaudeWeb     = "Claude Web"
	ChatGPT       = "ChatGPT"
	CodexCLI      = "Codex CLI"
	Cowork        = "Cowork"
	Cursor        = "Cursor"
	GrokBuild     = "Grok Build"
	GLM           = "GLM"
	Hermes        = "Hermes"
	OpenCode      = "OpenCode"
	ZCode         = "ZCode"
	Pi            = "Pi"
	QwenCode      = "Qwen Code"
	LegacyStore   = "Legacy store"
)

// HarnessForSource returns the canonical harness known from an ingestion
// surface. It never inspects artifact content.
func HarnessForSource(source string) string {
	source = strings.ToLower(strings.TrimSpace(source))
	switch {
	case source == "claude-cowork", source == "cowork":
		return Cowork
	case source == "claude-desktop":
		return ClaudeDesktop
	case source == "claude-web":
		return ClaudeWeb
	case source == "chatgpt-web":
		return ChatGPT
	case source == "claude", source == "claude-code":
		return ClaudeCode
	case source == "codex" || strings.HasPrefix(source, "codex-"):
		return CodexCLI
	case source == "cursor":
		return Cursor
	case source == "opencode" || strings.HasPrefix(source, "opencode-"):
		return OpenCode
	case source == "zcode" || strings.HasPrefix(source, "zcode-"):
		return ZCode
	case source == "pi" || strings.HasPrefix(source, "pi-"):
		return Pi
	case source == "hermes" || strings.HasPrefix(source, "hermes-"):
		return Hermes
	case source == "grok" || strings.HasPrefix(source, "grok-"):
		return GrokBuild
	case source == "glm" || strings.HasPrefix(source, "glm-"):
		return GLM
	case source == "qwen-code" || strings.HasPrefix(source, "qwen-code-"):
		return QwenCode
	case source == "legacy-store" || strings.HasPrefix(source, "legacy-store-"):
		return LegacyStore
	default:
		return ""
	}
}

type execer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

// Backfill labels historical ingest rows only where their recorded source
// agent determines the harness. It also removes the old "-build" harness
// suffix from known legacy Grok model labels without assigning a provider.
func Backfill(ctx context.Context, db execer) error {
	mappings := []struct {
		harness   string
		predicate string
	}{
		{Cowork, "source_agent IN ('cowork', 'claude-cowork')"},
		{ClaudeDesktop, "source_agent = 'claude-desktop'"},
		{ClaudeWeb, "source_agent = 'claude-web'"},
		{ChatGPT, "source_agent = 'chatgpt-web'"},
		{ClaudeCode, "source_agent IN ('claude', 'claude-code')"},
		{CodexCLI, "(source_agent = 'codex' OR source_agent LIKE 'codex-%')"},
		{Cursor, "source_agent = 'cursor'"},
		{OpenCode, "(source_agent = 'opencode' OR source_agent LIKE 'opencode-%')"},
		{ZCode, "(source_agent = 'zcode' OR source_agent LIKE 'zcode-%')"},
		{Pi, "(source_agent = 'pi' OR source_agent LIKE 'pi-%')"},
		{Hermes, "(source_agent = 'hermes' OR source_agent LIKE 'hermes-%')"},
		{GrokBuild, "(source_agent = 'grok' OR source_agent LIKE 'grok-%')"},
	}
	for _, mapping := range mappings {
		for _, table := range []string{"sessions", "memories"} {
			restriction := ""
			if table == "memories" {
				restriction = " AND origin = 'cron'" +
					" AND json_extract(metadata, '$._cron_source') IS NOT NULL"
			}
			statement := fmt.Sprintf(`UPDATE %s SET source_surface = ?
				WHERE COALESCE(source_surface, '') = '' AND %s%s`,
				table, mapping.predicate, restriction)
			if _, err := db.ExecContext(ctx, statement, mapping.harness); err != nil {
				return fmt.Errorf("backfill %s source surface: %w", table, err)
			}
		}
	}
	_, err := db.ExecContext(ctx, `
		UPDATE exchanges SET
			model = CASE model
				WHEN 'grok-4.6-build' THEN 'grok-4.6'
				WHEN 'grok-4.5-build' THEN 'grok-4.5'
				ELSE model END,
			provider = CASE provider WHEN 'xai' THEN NULL ELSE provider END
		WHERE model IN ('grok-4.6-build', 'grok-4.5-build')
		  AND EXISTS (
			SELECT 1 FROM sessions
			WHERE sessions.session_id = exchanges.session_id
			  AND sessions.source_surface = 'Grok Build'
		  )`)
	if err != nil {
		return fmt.Errorf("sanitize historical Grok model labels: %w", err)
	}
	return nil
}
