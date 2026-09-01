package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thellmwhisperer/la-roca/internal/distribution/logfile"
	"github.com/thellmwhisperer/la-roca/internal/distribution/rocaops"
	"github.com/thellmwhisperer/la-roca/internal/store"
)

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

func TestSnapshotCleanupFailureIsLoggedAsCommandFailure(t *testing.T) {
	fixtureInstallation(t)
	cleanupErr := errors.New("synthetic snapshot cleanup failure")
	env := &cliEnv{
		build: contractBuild(), out: io.Discard, errOut: io.Discard,
		snapshotCleanup: func() error { return cleanupErr },
	}
	code, err := executeWithOptions(env, []string{"version"}, nil, false)
	if code != ExitError || !errors.Is(err, cleanupErr) {
		t.Fatalf("cleanup verdict = %d, %v", code, err)
	}
	var record struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(readAuditStream(t, filepath.Join(os.Getenv("HOME"), ".roca"),
		logfile.Executions), &record); err != nil {
		t.Fatal(err)
	}
	if record.OK || !strings.Contains(record.Error, cleanupErr.Error()) {
		t.Fatalf("cleanup execution record = %+v", record)
	}
}

func TestCustodySnapshotUsesCLITelemetry(t *testing.T) {
	if os.Getenv("ROCA_CUSTODY_ORPHAN_HELPER") == "1" {
		if _, err := store.OpenReadOnlySnapshot(context.Background(), os.Getenv("ROCA_CUSTODY_ORPHAN_SOURCE")); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}

	tempRoot := t.TempDir()
	t.Setenv("TMPDIR", tempRoot)
	t.Setenv("TMP", tempRoot)
	t.Setenv("TEMP", tempRoot)
	dataDir := t.TempDir()
	opsPath := filepath.Join(dataDir, "ops.db")
	database, err := store.Open(opsPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestCustodySnapshotUsesCLITelemetry$")
	cmd.Env = append(os.Environ(),
		"ROCA_CUSTODY_ORPHAN_HELPER=1",
		"ROCA_CUSTODY_ORPHAN_SOURCE="+opsPath,
		"TMPDIR="+tempRoot,
		"TMP="+tempRoot,
		"TEMP="+tempRoot,
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("create custody orphan: %v\n%s", err, output)
	}
	fenced, err := rocaops.MemoryCustodyWriterFenced(snapshotTelemetryContext(t.Context(), dataDir), opsPath)
	if err != nil {
		t.Fatal(err)
	}
	if fenced {
		t.Fatal("empty custody database reported a writer fence")
	}
	if err := store.CloseReadOnlySnapshots(); err != nil {
		t.Fatal(err)
	}
	if directories, err := snapshotDirectories(tempRoot); err != nil || len(directories) != 0 {
		t.Fatalf("custody check left snapshot directories %v, error = %v", directories, err)
	}
	raw, err := os.ReadFile(filepath.Join(dataDir, logfile.DirName,
		logfile.Snapshots+"-"+time.Now().UTC().Format(time.DateOnly)+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	events := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var record struct {
			Event string `json:"event"`
		}
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatal(err)
		}
		events[record.Event] = true
	}
	if !events["create"] || !events["reap"] {
		t.Fatalf("custody snapshot telemetry events = %v, want create and reap", events)
	}
}

func snapshotDirectories(root string) ([]string, error) {
	return store.SnapshotDirectories(root)
}
