package cli

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestInitProvesWordSearchOnHistoryItJustRead is the first-run promise: the
// command does not return until a word taken from this machine's own history
// has been asked back of the index and found.
func TestInitProvesWordSearchOnHistoryItJustRead(t *testing.T) {
	_, dbPath := deepSearchHome(t)

	run := initMustSucceed(t, "init", "--db-path", dbPath)
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
	home := hermeticHome(t)

	out := runRoot(t, Build{Version: "test", Commit: "test-sha"},
		"init", "--db-path", filepath.Join(home, ".roca", "roca.db"))
	if !strings.Contains(out, "word search: nothing to search yet") {
		t.Fatalf("init on an empty machine does not name the empty state:\n%s", out)
	}
	if strings.Contains(out, "word search: did not answer") {
		t.Errorf("init called an empty machine a broken index:\n%s", out)
	}
}
