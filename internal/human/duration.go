/*
@overview Formats measured milliseconds for people while machine output keeps its numeric fields. ~50 lines, 1 public symbol.

	READING GUIDE
	-------------
	1. Start at Duration
	2. The three branches are the complete unit policy

	MAIN FLOW
	---------
	milliseconds -> choose precision -> value plus spaced unit

	PUBLIC API
	----------
	Duration  Formats milliseconds as ms, tenths of seconds, or whole seconds.

	INTERNALS
	---------
	None.

@exports Duration
@deps fmt
*/
package human

import "fmt"

// -- 1/1 CORE · Duration -- <- START HERE

// Duration formats milliseconds with only the precision a person can use.
func Duration(milliseconds int64) string {
	switch {
	case milliseconds < 1000:
		return fmt.Sprintf("%d ms", milliseconds)
	case milliseconds < 10000:
		return fmt.Sprintf("%.1f s", float64(milliseconds)/1000)
	default:
		return fmt.Sprintf("%.0f s", float64(milliseconds)/1000)
	}
}

// -/ 1/1
