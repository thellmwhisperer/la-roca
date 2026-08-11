// Command roca is La Roca: local semantic memory for agent fleets.
//
// The binary has no entry point beyond this one: parse, delegate to the CLI and
// translate the failure into an exit code.
package main

import (
	"fmt"
	"os"

	"github.com/thellmwhisperer/la-roca/internal/distribution/cli"
)

// Filled in by the linker at build time. See the Makefile.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	code, err := cli.Execute(cli.Build{Version: version, Commit: commit, Date: date})
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
	}
	os.Exit(code)
}
