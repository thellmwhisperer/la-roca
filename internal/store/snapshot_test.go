package store

import (
	"bufio"
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
)

func TestReadOnlySnapshotLifecycle(t *testing.T) {
	t.Run("canceled before copy", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		snapshot, err := OpenReadOnlySnapshot(ctx, filepath.Join(t.TempDir(), "missing.db"))
		if snapshot != nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("snapshot = %v, error = %v", snapshot, err)
		}
	})

	t.Run("close removes the snapshot directory", func(t *testing.T) {
		root := isolateSnapshotTemp(t)
		snapshot, err := OpenReadOnlySnapshot(context.Background(), fixtureDatabase(t))
		if err != nil {
			t.Fatal(err)
		}
		if dirs := listSnapshotDirs(t, root); len(dirs) != 1 {
			t.Fatalf("open left snapshot dirs %v, want 1", dirs)
		}
		if err := snapshot.Close(); err != nil {
			t.Fatal(err)
		}
		if dirs := listSnapshotDirs(t, root); len(dirs) != 0 {
			t.Fatalf("close left snapshot dirs %v, want none", dirs)
		}
	})

	t.Run("repeated opens reuse one copy", func(t *testing.T) {
		root := isolateSnapshotTemp(t)
		source := fixtureDatabase(t)
		first, err := OpenReadOnlySnapshot(context.Background(), source)
		if err != nil {
			t.Fatal(err)
		}
		second, err := OpenReadOnlySnapshot(context.Background(), source)
		if err != nil {
			t.Fatal(err)
		}
		if first.directory != second.directory {
			t.Fatalf("second open copied again: %q vs %q", first.directory, second.directory)
		}
		if dirs := listSnapshotDirs(t, root); len(dirs) != 1 {
			t.Fatalf("reuse left snapshot dirs %v, want 1", dirs)
		}
		if err := first.Close(); err != nil {
			t.Fatal(err)
		}
		if dirs := listSnapshotDirs(t, root); len(dirs) != 1 {
			t.Fatalf("first close removed a still-used snapshot: %v", dirs)
		}
		if err := second.Close(); err != nil {
			t.Fatal(err)
		}
		if dirs := listSnapshotDirs(t, root); len(dirs) != 0 {
			t.Fatalf("last close left snapshot dirs %v, want none", dirs)
		}
	})

	t.Run("orphans without a live lease are reaped and live copies are kept", func(t *testing.T) {
		root := isolateSnapshotTemp(t)
		live, err := OpenReadOnlySnapshot(context.Background(), fixtureDatabase(t))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = live.Close() })

		orphan := filepath.Join(root, snapshotDirectoryPrefix+"orphan")
		if err := os.Mkdir(orphan, 0o700); err != nil {
			t.Fatal(err)
		}
		payload := filepath.Join(orphan, "payload")
		if err := os.WriteFile(payload, make([]byte, 2<<20), 0o600); err != nil {
			t.Fatal(err)
		}

		next, err := OpenReadOnlySnapshot(context.Background(), fixtureDatabase(t))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = next.Close() })

		if _, err := os.Stat(orphan); !os.IsNotExist(err) {
			t.Fatalf("orphan snapshot still exists: %v", err)
		}
		if _, err := os.Stat(live.directory); err != nil {
			t.Fatalf("live snapshot was reaped: %v", err)
		}
	})
}

func TestKilledProcessOrphanIsReapedAndLiveSnapshotIsKept(t *testing.T) {
	root := isolateSnapshotTemp(t)
	killedSource := fixtureDatabase(t)
	liveSource := fixtureDatabase(t)

	killed := startSnapshotHelper(t, root, killedSource, "hold-forever")
	live := startSnapshotHelper(t, root, liveSource, "hold")
	if err := killed.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := killed.wait(); err == nil {
		t.Fatal("killed helper exited without a signal")
	}
	if _, err := os.Stat(killed.directory); err != nil {
		t.Fatalf("SIGKILL should leave an orphan snapshot: %v", err)
	}
	if _, err := os.Stat(live.directory); err != nil {
		t.Fatalf("live helper snapshot missing before reap: %v", err)
	}

	next, err := OpenReadOnlySnapshot(context.Background(), fixtureDatabase(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = next.Close() })

	if _, err := os.Stat(killed.directory); !os.IsNotExist(err) {
		t.Fatalf("killed helper orphan still exists after next open: %v", err)
	}
	if _, err := os.Stat(live.directory); err != nil {
		t.Fatalf("live helper snapshot was reaped: %v", err)
	}
}

func TestSnapshotTelemetryLogsCreateAndReap(t *testing.T) {
	root := isolateSnapshotTemp(t)
	dataDir := t.TempDir()
	SetSnapshotLogDir(dataDir)
	t.Cleanup(func() { SetSnapshotLogDir("") })

	orphan := filepath.Join(root, snapshotDirectoryPrefix+"orphan")
	if err := os.Mkdir(orphan, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(orphan, "payload"), make([]byte, 3<<20), 0o600); err != nil {
		t.Fatal(err)
	}

	source := fixtureDatabase(t)
	snapshot, err := OpenReadOnlySnapshot(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	if err := snapshot.Close(); err != nil {
		t.Fatal(err)
	}

	records := readSnapshotLog(t, dataDir)
	var sawCreate, sawReap bool
	for _, record := range records {
		switch record["event"] {
		case "create":
			sawCreate = true
			if record["source"] != source {
				t.Errorf("create source = %v, want %q", record["source"], source)
			}
			if record["reason"] != "copy" {
				t.Errorf("create reason = %v, want copy", record["reason"])
			}
			size, _ := record["size_bytes"].(float64)
			if size <= 0 {
				t.Errorf("create size_bytes = %v, want > 0", record["size_bytes"])
			}
		case "reap":
			sawReap = true
			count, _ := record["count"].(float64)
			reclaimed, _ := record["reclaimed_mb"].(float64)
			if count < 1 {
				t.Errorf("reap count = %v, want at least 1", record["count"])
			}
			if reclaimed < 1 {
				t.Errorf("reap reclaimed_mb = %v, want at least 1", record["reclaimed_mb"])
			}
		}
	}
	if !sawCreate || !sawReap {
		t.Fatalf("log records = %v, want create and reap", records)
	}
}

func TestExitCleanupRemovesOpenSnapshots(t *testing.T) {
	root := isolateSnapshotTemp(t)
	snapshot, err := OpenReadOnlySnapshot(context.Background(), fixtureDatabase(t))
	if err != nil {
		t.Fatal(err)
	}
	if dirs := listSnapshotDirs(t, root); len(dirs) != 1 {
		t.Fatalf("open left snapshot dirs %v, want 1", dirs)
	}
	cleanupHeldSnapshots()
	if dirs := listSnapshotDirs(t, root); len(dirs) != 0 {
		t.Fatalf("exit cleanup left snapshot dirs %v, want none", dirs)
	}
	_ = snapshot.Close()
}

func TestSnapshotHelperProcess(t *testing.T) {
	mode := os.Getenv("ROCA_SNAPSHOT_HELPER")
	if mode == "" {
		t.Skip("helper process")
	}
	snapshot, err := OpenReadOnlySnapshot(context.Background(), os.Getenv("ROCA_SNAPSHOT_SOURCE"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "open: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("READY %s\n", snapshot.directory)
	os.Stdout.Sync()
	_, _ = io.Copy(io.Discard, os.Stdin)
	if mode != "hold-forever" {
		_ = snapshot.Close()
	}
}

type snapshotHelper struct {
	cmd       *exec.Cmd
	directory string
	stdin     io.WriteCloser
	waited    bool
}

func startSnapshotHelper(t *testing.T, tmp, source, mode string) *snapshotHelper {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestSnapshotHelperProcess$")
	cmd.Env = append(os.Environ(),
		"ROCA_SNAPSHOT_HELPER="+mode,
		"ROCA_SNAPSHOT_SOURCE="+source,
		"TMPDIR="+tmp,
		"TMP="+tmp,
		"TEMP="+tmp,
	)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	helper := &snapshotHelper{cmd: cmd, stdin: stdin}
	t.Cleanup(func() {
		_ = helper.stdin.Close()
		if helper.cmd.Process != nil {
			_ = helper.cmd.Process.Kill()
			_ = helper.wait()
		}
	})
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		_ = helper.wait()
		t.Fatalf("helper stdout: %v", err)
	}
	directory := strings.TrimSpace(strings.TrimPrefix(line, "READY "))
	if directory == "" || !strings.Contains(directory, snapshotDirectoryPrefix) {
		t.Fatalf("helper announced %q", line)
	}
	helper.directory = directory
	return helper
}

func (helper *snapshotHelper) wait() error {
	if helper.waited {
		return nil
	}
	helper.waited = true
	return helper.cmd.Wait()
}

func isolateSnapshotTemp(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("TMPDIR", root)
	t.Setenv("TMP", root)
	t.Setenv("TEMP", root)
	return root
}

func fixtureDatabase(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "source.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL().Exec(`CREATE TABLE fixture (id INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func listSnapshotDirs(t *testing.T, root string) []string {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	var dirs []string
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), snapshotDirectoryPrefix) {
			dirs = append(dirs, filepath.Join(root, entry.Name()))
		}
	}
	return dirs
}

func readSnapshotLog(t *testing.T, dataDir string) []map[string]any {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dataDir, "logs", "snapshots-*.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) == 0 {
		t.Fatal("no snapshot telemetry log")
	}
	raw, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	var records []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if line == "" {
			continue
		}
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("invalid snapshot log line %q: %v", line, err)
		}
		records = append(records, record)
	}
	return records
}
