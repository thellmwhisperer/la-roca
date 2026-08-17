// Command bundle-vector appends a platform-matched vector executable to a
// freshly built Roca release binary.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/thellmwhisperer/la-roca/internal/distribution/rocavector"
)

func main() {
	binary := flag.String("binary", "", "core roca binary")
	payload := flag.String("payload", "", "roca-vector executable")
	flag.Parse()
	if *binary == "" || *payload == "" {
		fmt.Fprintln(os.Stderr, "error: binary and payload are required")
		os.Exit(2)
	}
	if err := rocavector.AppendPayload(*binary, *payload); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
