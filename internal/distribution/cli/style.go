package cli

import (
	"io"
	"os"

	"golang.org/x/term"
)

// Colour and motion are decoration, and decoration belongs to an interactive
// terminal only. A piped command, a test buffer and a program reading --json
// all receive the plain bytes the output contracts pin, because every style
// decision funnels through one gate: is this writer a terminal, and has the
// operator not turned colour off with the standard NO_COLOR opt-out.

// termAware reports whether a writer is an interactive terminal. It is the gate
// the spinner and the palette both check, so a bytes buffer (every test) and a
// pipe (every script) are never decorated.
func termAware(w io.Writer) bool {
	type descriptor interface{ Fd() uintptr }
	file, ok := w.(descriptor)
	return ok && term.IsTerminal(int(file.Fd()))
}

// colorAllowed is the operator's standing permission: colour is on unless they
// set NO_COLOR. The de-facto standard disables on any non-empty value and
// leaves an empty one alone.
func colorAllowed() bool { return os.Getenv("NO_COLOR") == "" }

// colorOn is the full decision: a terminal that has not opted out.
func colorOn(w io.Writer) bool { return termAware(w) && colorAllowed() }

// style wraps text in an ANSI sequence when on, and returns it untouched when
// off. It is the escape primitive; paint is the writer-aware front the rest of
// the CLI calls.
func style(text, code string, on bool) string {
	if !on {
		return text
	}
	return code + text + ansiReset
}

// paint styles text for a writer: coloured on a terminal that allows it, plain
// everywhere else. It is the only spelling the CLI uses, so no other site can
// emit a raw escape code.
func paint(w io.Writer, code, text string) string {
	return style(text, code, colorOn(w))
}

// The palette is small and semantic, never the only signal: a green check, a
// red cross and a cyan accent. Standard 4-bit codes render on every terminal
// and survive a copy-paste into a log.
const (
	ansiReset = "\x1b[0m"
	ansiGreen = "\x1b[32m"
	ansiRed   = "\x1b[31m"
	ansiCyan  = "\x1b[36m"
)

// mark is the one line a diagnosis paints for every provider, coloured on a
// terminal and literal everywhere else. The symbol stays [ok]/[no] off a
// terminal because colour must never be the only signal: the words carry it.
func (env *cliEnv) mark(ready bool) string {
	if ready {
		return paint(env.out, ansiGreen, "[ok]")
	}
	return paint(env.out, ansiRed, "[no]")
}
