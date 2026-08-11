package cli

import (
	"strings"
	"testing"
)

func TestInterpretationMarkdownPassesThroughOutsideATerminal(t *testing.T) {
	markdown := "### Raw heading\n\n- first item\n- second item"
	if got := formatInterpretation(markdown, false, 52, false); got != markdown {
		t.Fatalf("piped markdown changed:\n--- got ---\n%s\n--- want ---\n%s", got, markdown)
	}
}

func TestInterpretationMarkdownRendersAtTheRealTerminalWidth(t *testing.T) {
	markdown := "### Calm answer\n\n" +
		"This explanation keeps extraordinarilylongwords intact while ordinary words wrap cleanly across the terminal."
	got := formatInterpretation(markdown, true, 44, false)
	if got == markdown {
		t.Fatalf("TTY bypassed Glamour:\n%s", got)
	}
	if strings.Contains(got, "extraordinarilylong\nwords") ||
		strings.Contains(got, "extraordinarilylong\n  words") {
		t.Fatalf("renderer split a word in half:\n%s", got)
	}
	for _, line := range strings.Split(got, "\n") {
		if len([]rune(line)) > 44 {
			t.Errorf("rendered line is %d columns at width 44: %q", len([]rune(line)), line)
		}
	}
}

func TestTerminalWidthUsesTheDetectedWidthWithASaneFloor(t *testing.T) {
	if got := saneTerminalWidth(137); got != 137 {
		t.Fatalf("detected width = %d, want 137", got)
	}
	if got := saneTerminalWidth(20); got != 40 {
		t.Fatalf("narrow width = %d, want floor 40", got)
	}
}
