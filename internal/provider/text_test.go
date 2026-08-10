package provider

import "testing"

func TestCleanStripsTheThinkingBlock(t *testing.T) {
	raw := "<think>\nthe user wants a count\n</think>\nSELECT count(*) FROM memories LIMIT 1"
	if got := Clean(raw); got != "SELECT count(*) FROM memories LIMIT 1" {
		t.Fatalf("got %q", got)
	}
}

func TestCleanStripsMarkdownFences(t *testing.T) {
	for _, raw := range []string{
		"```sql\nSELECT 1 LIMIT 1\n```",
		"```\nSELECT 1 LIMIT 1\n```",
		"Here you go:\n```sql\nSELECT 1 LIMIT 1\n```\nHope it helps.",
	} {
		if got := Clean(raw); got != "SELECT 1 LIMIT 1" {
			t.Errorf("for %q got %q", raw, got)
		}
	}
}

// Small local models loop: they repeat a long line forever until they run out
// of tokens. The lab cuts at the first repetition (`_deloop`) and so does this.
func TestCleanCutsAtTheFirstRepetitionLoop(t *testing.T) {
	long := "SELECT content FROM memories WHERE content LIKE '%format%' AND supersedes IS NULL"
	raw := long + "\n" + long + "\n" + long
	if got := Clean(raw); got != long {
		t.Fatalf("the loop was not cut: %q", got)
	}
}

// A short line that repeats is not a loop: SQL legitimately repeats short
// fragments, and cutting there would mutilate a valid query.
func TestCleanDoesNotCutOnShortRepeatedLines(t *testing.T) {
	raw := "SELECT a\nFROM t\nUNION\nSELECT a\nFROM t\nLIMIT 1"
	if got := Clean(raw); got != raw {
		t.Fatalf("it cut a legitimate query: %q", got)
	}
}

func TestCleanDropsTheTrailingSemicolonAndTheSurroundingBlanks(t *testing.T) {
	if got := Clean("  \n SELECT 1 LIMIT 1 ;  \n "); got != "SELECT 1 LIMIT 1" {
		t.Fatalf("got %q", got)
	}
}

func TestCleanLeavesAnEmptyAnswerEmpty(t *testing.T) {
	if got := Clean("<think>only thinking</think>"); got != "" {
		t.Fatalf("got %q", got)
	}
}

// CleanProse is the interpretation's cleanup: reasoning goes, everything else
// stays. Extracting the first fence out of prose is what turned a full answer
// into the single word "atm" (2026-08-10), so fences and punctuation survive
// whole.
func TestCleanProse(t *testing.T) {
	prose := "The details:\n```\natm\n```\nand 97 subs."
	for raw, want := range map[string]string{
		prose: prose,
		"<think>\nrows\n</think>\nThe channel has 97 subs.": "The channel has 97 subs.",
		"The memory ends here;":                             "The memory ends here;",
	} {
		if got := CleanProse(raw); got != want {
			t.Errorf("CleanProse(%q) = %q, want %q", raw, got, want)
		}
	}
}
