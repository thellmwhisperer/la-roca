package cli

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestInitNarratesItsPhasesAndPointsToThePromptLast(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ROCA_DB_PATH", "")
	t.Setenv("ROCA_CONFIG", "")
	t.Setenv("ROCA_MODELS_ORDER", "none")

	out := runRoot(t, Build{Version: "test", Commit: "test-sha"},
		"init", "--db-path", filepath.Join(home, ".roca", "roca.db"))
	for _, want := range []string{
		"setup:",
		"agents: checking known sources",
		"agents detected:",
		"agents not found:",
		"database: inspecting",
		"database outcome: created",
		"rows: memories=",
		"ingest:",
		"delta:",
		"index: full-text index ready",
		"model:",
		"total:",
		"next steps:",
		"data directory:",
		"configuration:",
		"agent prompt:",
		"Paste its contents into the agent instructions you choose.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("init narration does not carry %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "## La Roca — local semantic memory") ||
		strings.Contains(out, "La Roca never edits agent instruction files") {
		t.Errorf("init dumped prompt.md instead of pointing to it:\n%s", out)
	}
}
