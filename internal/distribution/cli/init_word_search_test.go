package cli

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// TestInitProvesWordSearchOnHistoryItJustRead is the first-run promise: the
// command does not return until a word taken from this machine's own history
// has been asked back of the index and found.
func TestInitProvesWordSearchOnHistoryItJustRead(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ROCA_DB_PATH", "")
	t.Setenv("ROCA_CONFIG", "")
	t.Setenv("ROCA_MODELS_ORDER", "none")
	claudeRoot := filepath.Join(home, "sources", "claude")
	writeConfig(t, home, fmt.Sprintf("[defaults]\nclaude_projects_root = %q\nworkspace_roots = [%q]\n",
		claudeRoot, filepath.Join(home, "workspace")))
	writeFile(t, filepath.Join(claudeRoot, "-synthetic-demo", "66666666-6666-6666-6666-666666666666.jsonl"),
		`{"type":"user","timestamp":"2026-08-01T10:00:00Z","cwd":"/synthetic/demo","message":{"content":"how did we settle the harbour lighting question"}}
{"type":"assistant","timestamp":"2026-08-01T10:00:01Z","message":{"content":[{"type":"text","text":"we kept the harbour lighting on a separate circuit"}]}}
`)

	run := executeHermeticCLI([]string{"init", "--db-path", filepath.Join(home, ".roca", "roca.db")})
	if run.err != nil || run.code != ExitOK {
		t.Fatalf("init = code %d err %v:\n%s%s", run.code, run.err, run.output, run.warnings)
	}
	if !strings.Contains(run.output, "word search: ready ·") {
		t.Fatalf("init did not prove word search on a machine that has history:\n%s", run.output)
	}
	if !strings.Contains(run.output, "and found it in") {
		t.Errorf("the proof does not say where the word was found:\n%s", run.output)
	}
}

// TestInitSaysNothingToSearchYetRatherThanBroken keeps the empty machine out of
// the failure wording: no history is a fact about the machine, not a fault in
// the index.
func TestInitSaysNothingToSearchYetRatherThanBroken(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ROCA_DB_PATH", "")
	t.Setenv("ROCA_CONFIG", "")
	t.Setenv("ROCA_MODELS_ORDER", "none")

	out := runRoot(t, Build{Version: "test", Commit: "test-sha"},
		"init", "--db-path", filepath.Join(home, ".roca", "roca.db"))
	if !strings.Contains(out, "word search: nothing to search yet") {
		t.Fatalf("init on an empty machine does not name the empty state:\n%s", out)
	}
	if strings.Contains(out, "word search: did not answer") {
		t.Errorf("init called an empty machine a broken index:\n%s", out)
	}
}
