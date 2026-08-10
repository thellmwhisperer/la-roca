package cli

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestInitNarratesItsPhasesAndPrintsThePromptLast(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ROCA_DB_PATH", "")
	t.Setenv("ROCA_CONFIG", "")
	t.Setenv("ROCA_MODELS_ORDER", "none")

	out := runRoot(t, Build{Version: "test", Commit: "test-sha"},
		"init", "--db-path", filepath.Join(home, ".roca", "roca.db"))
	for _, want := range []string{
		"agents: checking known sources",
		"agents detected:",
		"agents not found:",
		"database: inspecting",
		"database: created",
		"rows: memories=",
		"index: building",
		"index: ready in",
		"ingest: starting",
		"ingest: complete",
		"model:",
		"data directory:",
		"configuration:",
		"agent prompt:",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("init narration does not carry %q:\n%s", want, out)
		}
	}
	last := "La Roca never edits agent instruction files; a human chooses where to paste this block."
	if !strings.HasSuffix(strings.TrimSpace(out), last) {
		t.Errorf("the presentation prompt is not the last init artifact:\n%s", out)
	}
}
