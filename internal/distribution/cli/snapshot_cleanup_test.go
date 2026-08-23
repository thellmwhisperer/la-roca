/**
 * @overview Verifies completed CLI snapshot cleanup. ~125 lines, no public symbols.
 *
 *   READING GUIDE
 *   -------------
 *   1. Start at TestCompletedReadOnlyCommandRemovesSnapshots  <- executable contract
 *   2. TestCompletedCommandDrainsExistingSnapshots            <- process boundary
 *
 *   MAIN FLOW
 *   ---------
 *   fixtureInstallation -> CLI command -> process-boundary drain -> empty snapshot namespace
 *
 *   PUBLIC API
 *   ----------
 *   None.
 *
 *   INTERNALS
 *   ---------
 *   TestCompletedReadOnlyCommandRemovesSnapshots, TestCompletedCommandDrainsExistingSnapshots
 *   snapshotDirectories
 *
 * @exports
 * @deps context; os/exec; internal/store; testing
 */
package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/store"
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
	directories, err := snapshotDirectories(tempRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(directories) != 0 {
		t.Fatalf("completed command left snapshot directories %v", directories)
	}
}

func TestCompletedCommandDrainsExistingSnapshots(t *testing.T) {
	tempRoot := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("ROCA_READ_ONLY", "1")
	t.Setenv("TMPDIR", tempRoot)
	t.Setenv("TMP", tempRoot)
	t.Setenv("TEMP", tempRoot)
	databasePath := filepath.Join(t.TempDir(), "source.db")
	database, err := store.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.OpenReadOnlySnapshot(context.Background(), databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if directories, err := snapshotDirectories(tempRoot); err != nil || len(directories) != 1 {
		t.Fatalf("open snapshot directories = %v, error = %v, want one", directories, err)
	}
	code, err := executeCommand(contractBuild(), io.Discard, io.Discard, []string{"--help"}, false)
	if err != nil || code != ExitOK {
		t.Fatalf("completed command code = %d, error = %v", code, err)
	}
	if directories, err := snapshotDirectories(tempRoot); err != nil || len(directories) != 0 {
		t.Fatalf("completed command left snapshot directories %v, error = %v", directories, err)
	}
	_ = snapshot.Close()
}

func snapshotDirectories(root string) ([]string, error) {
	var directories []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() && strings.HasPrefix(entry.Name(), "roca-read-only-snapshot-") {
			directories = append(directories, path)
			return filepath.SkipDir
		}
		return nil
	})
	return directories, err
}

// -/ 1/1
