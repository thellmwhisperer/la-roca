/*
@overview Contracts the one human duration spelling used across terminal output. ~50 lines, no public symbols.

	READING GUIDE
	-------------
	1. Start at TestDuration
	2. Each row is an operator-provided output class

	MAIN FLOW
	---------
	milliseconds -> Duration -> compact human text

	PUBLIC API
	----------
	None; this file tests Duration.

	INTERNALS
	---------
	TestDuration

@exports
@deps testing
*/
package human

import "testing"

// -- 1/1 CORE · TestDuration -- <- START HERE

func TestDuration(t *testing.T) {
	for _, test := range []struct {
		milliseconds int64
		want         string
	}{
		{0, "0 ms"},
		{340, "340 ms"},
		{1200, "1.2 s"},
		{25635, "26 s"},
	} {
		if got := Duration(test.milliseconds); got != test.want {
			t.Errorf("Duration(%d) = %q, want %q", test.milliseconds, got, test.want)
		}
	}
}

// -/ 1/1
