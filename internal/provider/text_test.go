package provider

import "testing"

// CleanProse drops reasoning while preserving fenced blocks and punctuation.
func TestCleanProse(t *testing.T) {
	prose := "The details:\n```\nfixture\n```\nand 23 subscribers."
	for raw, want := range map[string]string{
		prose: prose,
		"<think>\nrows\n</think>\nThe channel has 23 subscribers.": "The channel has 23 subscribers.",
		"The memory ends here;": "The memory ends here;",
	} {
		if got := CleanProse(raw); got != want {
			t.Errorf("CleanProse(%q) = %q, want %q", raw, got, want)
		}
	}
}
