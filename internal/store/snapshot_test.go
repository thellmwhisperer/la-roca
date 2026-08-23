/**
 * @overview Verifies snapshot lifetime, reuse, reaping, and telemetry. ~700 lines, no public symbols.
 *
 *   READING GUIDE
 *   -------------
 *   1. Start at TestReadOnlySnapshotLifecycle        <- baseline behavior
 *   2. TestSnapshotFlightsUseFinalSourceFingerprint <- concurrency contract
 *   3. TestKilledProcessOrphanIsReapedAndLiveSnapshotIsKept
 *   4. TestSnapshotHelperProcess                    <- subprocess fixture
 *
 *   MAIN FLOW
 *   ---------
 *   fixtureDatabase -> OpenReadOnlySnapshot -> assert filesystem/telemetry -> Close
 *
 *   PUBLIC API
 *   ----------
 *   None.
 *
 *   INTERNALS
 *   ---------
 *   lifecycle tests, flight tests, reap tests, snapshotHelper, filesystem helpers
 *
 * @exports
 * @deps testing; os/exec; internal/securefile
 */
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
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thellmwhisperer/la-roca/internal/securefile"
)

// -- 1/5 CORE · Snapshot lifecycle -- <- START HERE

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
		if first == second {
			t.Fatal("cache reuse returned the same acquisition handle")
		}
		if dirs := listSnapshotDirs(t, root); len(dirs) != 1 {
			t.Fatalf("reuse left snapshot dirs %v, want 1", dirs)
		}
		if err := first.Close(); err != nil {
			t.Fatal(err)
		}
		if err := first.Close(); err != nil {
			t.Fatal(err)
		}
		if dirs := listSnapshotDirs(t, root); len(dirs) != 1 {
			t.Fatalf("repeated close removed a still-used snapshot: %v", dirs)
		}
		var schemaVersion int
		if err := second.SQL().QueryRow("PRAGMA schema_version").Scan(&schemaVersion); err != nil {
			t.Fatalf("second acquisition was closed by an alias: %v", err)
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

// -/ 1/5

// -- 2/5 HELPER · Cache identity and context-aware flights --

func TestSnapshotFlightsUseFinalSourceFingerprint(t *testing.T) {
	root := isolateSnapshotTemp(t)
	source := fixtureDatabase(t)
	originalCopy := copySnapshotSourceFn
	t.Cleanup(func() { copySnapshotSourceFn = originalCopy })

	entered := make(chan struct{})
	releaseCopy := make(chan struct{})
	concurrentCopy := make(chan struct{})
	var concurrentOnce sync.Once
	var copies atomic.Int32
	copySnapshotSourceFn = func(ctx context.Context, source, destination string) error {
		call := copies.Add(1)
		if call == 1 {
			close(entered)
			<-releaseCopy
		} else {
			select {
			case <-releaseCopy:
			default:
				concurrentOnce.Do(func() { close(concurrentCopy) })
			}
		}
		return originalCopy(ctx, source, destination)
	}

	firstResult := make(chan snapshotResult, 1)
	go func() {
		snapshot, err := OpenReadOnlySnapshot(context.Background(), source)
		firstResult <- snapshotResult{snapshot: snapshot, err: err}
	}()
	<-entered
	info, err := os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}
	changed := info.ModTime().Add(2 * time.Second)
	if err := os.Chtimes(source, changed, changed); err != nil {
		t.Fatal(err)
	}

	secondResult := make(chan snapshotResult, 1)
	go func() {
		snapshot, err := OpenReadOnlySnapshot(context.Background(), source)
		secondResult <- snapshotResult{snapshot: snapshot, err: err}
	}()
	select {
	case <-concurrentCopy:
		t.Fatal("source change started a second concurrent snapshot copy")
	case <-time.After(50 * time.Millisecond):
	}
	close(releaseCopy)

	first := <-firstResult
	second := <-secondResult
	if first.err != nil || second.err != nil {
		t.Fatalf("opens failed: first=%v second=%v", first.err, second.err)
	}
	if first.snapshot.directory != second.snapshot.directory {
		t.Fatalf("flights published different snapshots: %q vs %q",
			first.snapshot.directory, second.snapshot.directory)
	}
	third, err := OpenReadOnlySnapshot(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	if third.directory != first.snapshot.directory {
		t.Fatalf("final fingerprint copied again: %q vs %q", third.directory, first.snapshot.directory)
	}
	if got := copies.Load(); got != 2 {
		t.Fatalf("copy attempts = %d, want 2 for one source-change retry", got)
	}
	if dirs := listSnapshotDirs(t, root); len(dirs) != 1 {
		t.Fatalf("flight reuse left snapshot dirs %v, want 1", dirs)
	}
	for _, snapshot := range []*ReadOnlySnapshot{first.snapshot, second.snapshot, third} {
		if err := snapshot.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSnapshotFlightWaitersOwnTheirContexts(t *testing.T) {
	t.Run("creator cancellation is retried", func(t *testing.T) {
		isolateSnapshotTemp(t)
		source := fixtureDatabase(t)
		originalCopy := copySnapshotSourceFn
		t.Cleanup(func() { copySnapshotSourceFn = originalCopy })

		entered := make(chan struct{})
		var copies atomic.Int32
		copySnapshotSourceFn = func(ctx context.Context, source, destination string) error {
			if copies.Add(1) == 1 {
				close(entered)
				<-ctx.Done()
				return ctx.Err()
			}
			return originalCopy(ctx, source, destination)
		}

		creatorCtx, cancelCreator := context.WithCancel(context.Background())
		creatorResult := make(chan snapshotResult, 1)
		go func() {
			snapshot, err := OpenReadOnlySnapshot(creatorCtx, source)
			creatorResult <- snapshotResult{snapshot: snapshot, err: err}
		}()
		<-entered
		waiterResult := make(chan snapshotResult, 1)
		go func() {
			snapshot, err := OpenReadOnlySnapshot(context.Background(), source)
			waiterResult <- snapshotResult{snapshot: snapshot, err: err}
		}()
		select {
		case result := <-waiterResult:
			if result.snapshot != nil {
				_ = result.snapshot.Close()
			}
			t.Fatalf("waiter returned before creator cancellation: %v", result.err)
		case <-time.After(50 * time.Millisecond):
		}
		cancelCreator()
		creator := <-creatorResult
		if !errors.Is(creator.err, context.Canceled) {
			t.Fatalf("creator error = %v, want context canceled", creator.err)
		}
		waiter := <-waiterResult
		if waiter.err != nil {
			t.Fatalf("healthy waiter inherited creator error: %v", waiter.err)
		}
		if err := waiter.snapshot.Close(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("waiter cancellation returns during copy", func(t *testing.T) {
		isolateSnapshotTemp(t)
		source := fixtureDatabase(t)
		originalCopy := copySnapshotSourceFn
		t.Cleanup(func() { copySnapshotSourceFn = originalCopy })

		entered := make(chan struct{})
		releaseCopy := make(chan struct{})
		copySnapshotSourceFn = func(ctx context.Context, source, destination string) error {
			close(entered)
			<-releaseCopy
			return originalCopy(ctx, source, destination)
		}
		creatorResult := make(chan snapshotResult, 1)
		go func() {
			snapshot, err := OpenReadOnlySnapshot(context.Background(), source)
			creatorResult <- snapshotResult{snapshot: snapshot, err: err}
		}()
		<-entered

		waiterCtx, cancelWaiter := context.WithCancel(context.Background())
		waiterResult := make(chan snapshotResult, 1)
		go func() {
			snapshot, err := OpenReadOnlySnapshot(waiterCtx, source)
			waiterResult <- snapshotResult{snapshot: snapshot, err: err}
		}()
		select {
		case result := <-waiterResult:
			t.Fatalf("waiter returned before cancellation: %v", result.err)
		case <-time.After(50 * time.Millisecond):
		}
		cancelWaiter()
		select {
		case result := <-waiterResult:
			if !errors.Is(result.err, context.Canceled) {
				t.Fatalf("waiter error = %v, want context canceled", result.err)
			}
		case <-time.After(time.Second):
			t.Fatal("canceled waiter remained blocked on the creator")
		}
		close(releaseCopy)
		creator := <-creatorResult
		if creator.err != nil {
			t.Fatal(creator.err)
		}
		if err := creator.snapshot.Close(); err != nil {
			t.Fatal(err)
		}
	})
}

// -/ 2/5

// -- 3/5 HELPER · Cross-process reaping and telemetry --

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

func TestSnapshotPublicationIsLeasedBeforeItIsVisible(t *testing.T) {
	root := isolateSnapshotTemp(t)
	source := fixtureDatabase(t)
	originalCopy := copySnapshotSourceFn
	t.Cleanup(func() { copySnapshotSourceFn = originalCopy })
	entered := make(chan struct{})
	releaseCopy := make(chan struct{})
	copySnapshotSourceFn = func(ctx context.Context, source, destination string) error {
		close(entered)
		<-releaseCopy
		return originalCopy(ctx, source, destination)
	}
	result := make(chan snapshotResult, 1)
	go func() {
		snapshot, err := OpenReadOnlySnapshot(context.Background(), source)
		result <- snapshotResult{snapshot: snapshot, err: err}
	}()
	<-entered
	dirs := listSnapshotDirs(t, root)
	if len(dirs) != 1 {
		t.Fatalf("visible snapshot dirs = %v, want 1", dirs)
	}
	if _, err := securefile.TryLock(filepath.Join(dirs[0], snapshotLeaseName)); !errors.Is(err, securefile.ErrBusy) {
		t.Fatalf("visible snapshot lease = %v, want busy", err)
	}
	if matches, err := filepath.Glob(filepath.Join(root, snapshotStagingPrefix+"*")); err != nil {
		t.Fatal(err)
	} else if len(matches) != 0 {
		t.Fatalf("staging directories remained visible after publication: %v", matches)
	}
	if err := scavengeReadOnlySnapshots(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dirs[0]); err != nil {
		t.Fatalf("concurrent reap removed a published snapshot: %v", err)
	}
	close(releaseCopy)
	opened := <-result
	if opened.err != nil {
		t.Fatal(opened.err)
	}
	if err := opened.snapshot.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentSnapshotReapersCountOneRemoval(t *testing.T) {
	root := isolateSnapshotTemp(t)
	dataDir := t.TempDir()
	SetSnapshotLogDir(dataDir)
	t.Cleanup(func() { SetSnapshotLogDir("") })
	orphan := filepath.Join(root, snapshotDirectoryPrefix+"orphan")
	if err := os.Mkdir(orphan, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(orphan, snapshotLeaseName), []byte("1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(orphan, "payload"), make([]byte, 2<<20), 0o600); err != nil {
		t.Fatal(err)
	}

	releaseNamespace, err := securefile.Lock(filepath.Join(root, snapshotNamespaceLeaseName))
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	for range 2 {
		go func() { results <- scavengeReadOnlySnapshots(context.Background(), root) }()
	}
	select {
	case err := <-results:
		t.Fatalf("reaper bypassed namespace lease: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	if err := releaseNamespace(); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}

	var reaped float64
	for _, record := range readSnapshotLog(t, dataDir) {
		if record["event"] == "reap" {
			reaped += record["count"].(float64)
		}
	}
	if reaped != 1 {
		t.Fatalf("concurrent reapers reported %.0f removals, want 1", reaped)
	}
}

func TestVanishedSnapshotIsNotCountedAsReaped(t *testing.T) {
	root := isolateSnapshotTemp(t)
	dataDir := t.TempDir()
	SetSnapshotLogDir(dataDir)
	t.Cleanup(func() { SetSnapshotLogDir("") })
	orphan := filepath.Join(root, snapshotDirectoryPrefix+"vanished")
	if err := os.Mkdir(orphan, 0o700); err != nil {
		t.Fatal(err)
	}
	originalClaim := claimSnapshotDirectoryFn
	t.Cleanup(func() { claimSnapshotDirectoryFn = originalClaim })
	claimSnapshotDirectoryFn = func(root, directory string) (string, error) {
		if err := os.RemoveAll(directory); err != nil {
			return "", err
		}
		return originalClaim(root, directory)
	}
	if err := scavengeReadOnlySnapshots(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	matches, err := filepath.Glob(filepath.Join(dataDir, "logs", "snapshots-*.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("vanished snapshot produced reap telemetry: %v", matches)
	}
}

func TestSnapshotReaperReleasesLeaseBeforeDeletion(t *testing.T) {
	root := isolateSnapshotTemp(t)
	orphan := filepath.Join(root, snapshotDirectoryPrefix+"windows-compatible")
	if err := os.Mkdir(orphan, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(orphan, snapshotLeaseName), []byte("1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	originalRemove := removeSnapshotDirectoryFn
	t.Cleanup(func() { removeSnapshotDirectoryFn = originalRemove })
	var deletionErr error
	var deletionCalled bool
	removeSnapshotDirectoryFn = func(directory string) error {
		deletionCalled = true
		release, err := securefile.TryLock(filepath.Join(directory, snapshotLeaseName))
		if err != nil {
			deletionErr = fmt.Errorf("lease remained held at deletion: %w", err)
			return deletionErr
		}
		if err := release(); err != nil {
			return err
		}
		return originalRemove(directory)
	}
	if err := scavengeReadOnlySnapshots(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	if !deletionCalled || deletionErr != nil {
		t.Fatalf("deletion called = %t, error = %v", deletionCalled, deletionErr)
	}
	if matches, err := filepath.Glob(filepath.Join(root, snapshotReapPrefix+"*")); err != nil {
		t.Fatal(err)
	} else if len(matches) != 0 {
		t.Fatalf("claimed orphan remained after reap: %v", matches)
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

// -/ 3/5

// -- 4/5 HELPER · Subprocess snapshot owner --

func TestSnapshotHelperProcess(t *testing.T) {
	mode := os.Getenv("ROCA_SNAPSHOT_HELPER")
	if mode == "" {
		t.Skip("helper process")
	}
	if mode == "signal-registration" {
		snapshotBeforeLeaseRegistrationFn = func(directory string) {
			fmt.Printf("STAGING %s\n", directory)
			os.Stdout.Sync()
			for !snapshotShuttingDown.Load() {
				runtime.Gosched()
			}
		}
	}
	snapshot, err := OpenReadOnlySnapshot(context.Background(), os.Getenv("ROCA_SNAPSHOT_SOURCE"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "open: %v\n", err)
		os.Exit(1)
	}
	if mode == "hold-query" {
		started := make(chan struct{})
		go func() {
			close(started)
			var count int64
			_ = snapshot.SQL().QueryRowContext(context.Background(), `
				WITH RECURSIVE count_to_a_billion(value) AS (
					VALUES(0) UNION ALL SELECT value + 1 FROM count_to_a_billion WHERE value < 1000000000
				)
				SELECT COUNT(*) FROM count_to_a_billion`).Scan(&count)
		}()
		<-started
		time.Sleep(50 * time.Millisecond)
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
	directory := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(line, "READY "), "STAGING "))
	if directory == "" || (!strings.Contains(directory, snapshotDirectoryPrefix) &&
		!strings.Contains(directory, snapshotStagingPrefix)) {
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

// -/ 4/5

// -- 5/5 HELPER · Test fixtures and filesystem assertions --

type snapshotResult struct {
	snapshot *ReadOnlySnapshot
	err      error
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

// -/ 5/5
