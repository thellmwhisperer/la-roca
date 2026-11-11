package cli

import (
	"io"

	"github.com/thellmwhisperer/la-roca/internal/distribution/axi"
	"golang.org/x/term"
)

// fieldWidth is the per-cell clip budget. It mirrors axi.FieldWidth so the test
// that asserts a field never exceeds it stays in the package, while the value
// itself has one owner.
const fieldWidth = axi.FieldWidth

const fallbackTerminalWidth = 80

func detectedTerminalWidth(w io.Writer) int {
	type descriptor interface{ Fd() uintptr }
	file, ok := w.(descriptor)
	if !ok {
		return fallbackTerminalWidth
	}
	width, _, err := term.GetSize(int(file.Fd()))
	if err != nil {
		return fallbackTerminalWidth
	}
	return width
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
