package cli

import (
	"io"
	"strings"

	"github.com/charmbracelet/glamour"
	"github.com/thellmwhisperer/la-roca/internal/distribution/axi"
	"golang.org/x/term"
)

// fieldWidth is the per-cell clip budget. It mirrors axi.FieldWidth so the test
// that asserts a field never exceeds it stays in the package, while the value
// itself has one owner.
const fieldWidth = axi.FieldWidth

const (
	minimumTerminalWidth  = 40
	fallbackTerminalWidth = 80
)

func terminalWidth(w io.Writer) int {
	type descriptor interface{ Fd() uintptr }
	file, ok := w.(descriptor)
	if !ok {
		return fallbackTerminalWidth
	}
	width, _, err := term.GetSize(int(file.Fd()))
	if err != nil {
		return fallbackTerminalWidth
	}
	return saneTerminalWidth(width)
}

func saneTerminalWidth(width int) int {
	if width < minimumTerminalWidth {
		return minimumTerminalWidth
	}
	return width
}

// formatInterpretation keeps pipes byte-for-byte plain and lets Glamour own
// interactive Markdown. The renderer receives the detected terminal width, so
// it wraps at word boundaries instead of applying a fixed paper width.
func formatInterpretation(markdown string, tty bool, width int, colour bool) string {
	if !tty || markdown == "" {
		return markdown
	}
	style := "ascii"
	if colour {
		style = "dark"
	}
	renderer, err := glamour.NewTermRenderer(
		glamour.WithStandardStyle(style),
		glamour.WithWordWrap(saneTerminalWidth(width)),
	)
	if err != nil {
		return markdown
	}
	rendered, err := renderer.Render(markdown)
	if err != nil {
		return markdown
	}
	return strings.Trim(rendered, "\n")
}

// rowOutput forwards to the shared renderer. The shell's listings build rows of
// their own and hand them here; the query and exec paths go through the axi
// composers, which call the same function.
func rowOutput(columns []string, rows []map[string]any, terms ...string) string {
	return axi.RowOutput(columns, rows, terms...)
}

// renderHelp forwards to the shared renderer.
func renderHelp(lines ...string) string {
	return axi.RenderHelp(lines...)
}
