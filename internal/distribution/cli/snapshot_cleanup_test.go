/**
 * @overview Verifies completed CLI snapshot cleanup. ~85 lines, no public symbols.
 *
 *   READING GUIDE
 *   -------------
 *   1. Start at TestCompletedReadOnlyCommandRemovesSnapshots  <- executable contract
 *
 *   MAIN FLOW
 *   ---------
 *   fixtureInstallation -> CLI subprocess -> read-only exec -> assert empty snapshot namespace
 *
 *   PUBLIC API
 *   ----------
 *   None.
 *
 *   INTERNALS
 *   ---------
 *   TestCompletedReadOnlyCommandRemovesSnapshots
 *
 * @exports
 * @deps os/exec; testing
 */
package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// -- 1/1 CORE · TestCompletedReadOnlyCommandRemovesSnapshots -- <- START HERE

func TestCompletedReadOnlyCommandRemovesSnapshots(t *testing.T) {
	if os.Getenv("ROCA_CLI_SNAPSHOT_HELPER") == "1" {
		os.Args = []string{
			"roca", "--read-only", "--db-path", os.Getenv("ROCA_CLI_SNAPSHOT_DB"),
			"exec", "SELECT COUNT(*) AS count FROM memories",
		}
		code, err := Execute(contractBuild())
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
		}
		os.Exit(code)
	}

	t.Setenv("ROCA_READ_ONLY", "")
	fixture := fixtureInstallation(t)
	tempRoot := t.TempDir()
	t.Setenv("TMPDIR", tempRoot)
	t.Setenv("TMP", tempRoot)
	t.Setenv("TEMP", tempRoot)
	cmd := exec.Command(os.Args[0], "-test.run=^TestCompletedReadOnlyCommandRemovesSnapshots$")
	cmd.Env = append(os.Environ(),
		"ROCA_CLI_SNAPSHOT_HELPER=1",
		"ROCA_CLI_SNAPSHOT_DB="+filepath.Join(fixture.home, ".roca", "roca.db"),
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("read-only CLI subprocess: %v\n%s", err, output)
	}
	entries, err := os.ReadDir(tempRoot)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), "roca-read-only-snapshot-") {
			t.Fatalf("completed command left snapshot directory %q", entry.Name())
		}
	}
}

// -/ 1/1
