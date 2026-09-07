// @overview Run the shared deterministic vector resident fixture as a binary.
// READING GUIDE: main delegates to testfixture.RunFakeResident.
// MAIN FLOW: invocation -> fake resident protocol -> exit status.
// PUBLIC API: none. INTERNALS: main.
// @exports
// @deps os, testfixture
package main

import (
	"os"

	"github.com/thellmwhisperer/la-roca/test/testfixture"
)

// -- 1 CORE · main <- START HERE --
func main() {
	os.Exit(testfixture.RunFakeResident())
}

// -/ 1
