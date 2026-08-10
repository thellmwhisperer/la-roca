/*
@overview AXI TOON row rendering, delegated to the shared axi package. ~30 lines, no public symbols.

	READING GUIDE
	-------------
	The renderer lives in internal/distribution/axi. This file keeps the
	package-private names the shell's other listings (runtime status, the skill
	table, the doctor) and its tests call, and forwards them to the one owner, so
	a second TOON renderer never grows here.

	PUBLIC API
	----------
	None; this file serves the CLI package.

	INTERNALS
	---------
	rowOutput, renderHelp, fieldWidth
*/
package cli

import (
	"github.com/thellmwhisperer/la-roca/internal/distribution/axi"
)

// fieldWidth is the per-cell clip budget. It mirrors axi.FieldWidth so the test
// that asserts a field never exceeds it stays in the package, while the value
// itself has one owner.
const fieldWidth = axi.FieldWidth

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
