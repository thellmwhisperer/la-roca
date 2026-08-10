package cli

import (
	"bytes"
	"strings"
	"testing"
)

// Colour and motion are decoration, and decoration belongs to an interactive
// terminal only. Every test runs against a buffer, so these contracts are the
// gate that keeps the pinned output byte-identical when piped: no escape code
// ever reaches a non-terminal, and the literal symbols survive without colour.

// A bytes buffer is not a terminal, so the whole decoration gate is closed.
func TestTermAwareIsFalseForABuffer(t *testing.T) {
	if termAware(&bytes.Buffer{}) {
		t.Fatal("a bytes buffer reads as a terminal")
	}
}

// paint is the only spelling the CLI uses for colour, so it is the place a raw
// escape code could leak. Off a terminal it returns the text untouched.
func TestPaintEmitsNoCodesWithoutATerminal(t *testing.T) {
	var buf bytes.Buffer
	for _, text := range []string{"[ok]", "route compiler", "1908"} {
		if got := paint(&buf, ansiGreen, text); got != text {
			t.Errorf("paint(%q) = %q, want it plain off a terminal", text, got)
		}
	}
}

// style is the escape primitive paint delegates to. On, it wraps with the code
// and the reset and leaves the text intact; off, it returns the text untouched.
// Both halves are exercised without faking a terminal.
func TestStyleWrapsOnlyWhenOn(t *testing.T) {
	if got := style("memory", ansiCyan, false); got != "memory" {
		t.Errorf("style off = %q, want plain", got)
	}
	got := style("memory", ansiCyan, true)
	if !strings.HasPrefix(got, ansiCyan) || !strings.HasSuffix(got, ansiReset) {
		t.Errorf("style on does not wrap with the code and reset: %q", got)
	}
	if inner := strings.TrimSuffix(strings.TrimPrefix(got, ansiCyan), ansiReset); inner != "memory" {
		t.Errorf("style on altered the text: %q", inner)
	}
}

// NO_COLOR is the standard opt-out. colorAllowed is the half of the decision a
// buffer cannot reach (it is already off a terminal), so it is pinned on its
// own: allowed by default and under an empty value, off under any non-empty one.
func TestColorAllowedHonoursNoColor(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	if !colorAllowed() {
		t.Fatal("colour is disallowed with NO_COLOR empty")
	}
	t.Setenv("NO_COLOR", "1")
	if colorAllowed() {
		t.Fatal("colour is allowed under NO_COLOR")
	}
}

// The diagnosis symbol stays the literal [ok]/[no] off a terminal: colour must
// never be the only signal, and those literals are what the contracts pin.
func TestMarkIsLiteralOffATerminal(t *testing.T) {
	env := &cliEnv{out: &bytes.Buffer{}}
	if got := env.mark(true); got != "[ok]" {
		t.Errorf("mark(true) = %q, want [ok]", got)
	}
	if got := env.mark(false); got != "[no]" {
		t.Errorf("mark(false) = %q, want [no]", got)
	}
}
