package store

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSnapshotReaperKeepAndDeleteDecisions(t *testing.T) {
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	minAge := time.Hour
	dead := snapshotLease{PID: 4242, StartUnixNano: 99}
	cases := []struct {
		name     string
		lease    string
		age      time.Duration
		liveness snapshotOwnerLiveness
		wantGone bool
	}{
		{name: "missing-lease-old", lease: "", age: 2 * time.Hour, wantGone: false},
		{name: "missing-lease-young", lease: "", age: 30 * time.Minute, wantGone: false},
		{name: "malformed-lease-old", lease: "not-a-lease\n", age: 2 * time.Hour, wantGone: false},
		{name: "live-owner-old", lease: encodeSnapshotLease(dead), age: 2 * time.Hour, liveness: ownerLive, wantGone: false},
		{name: "uncertain-owner-old", lease: encodeSnapshotLease(dead), age: 2 * time.Hour, liveness: ownerUncertain, wantGone: false},
		{name: "dead-owner-young", lease: encodeSnapshotLease(dead), age: 30 * time.Minute, liveness: ownerDead, wantGone: false},
		{name: "dead-owner-old", lease: encodeSnapshotLease(dead), age: 2 * time.Hour, liveness: ownerDead, wantGone: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			path := plantSnapshotDir(t, root, snapshotDirectoryPrefix+tc.name, now.Add(-tc.age), tc.lease, 32)
			report, err := runSnapshotReaper(context.Background(), reaperPass{
				root:   root,
				now:    now,
				minAge: minAge,
				budget: newReaperBudget(time.Now().Add(time.Minute), 8),
				inspect: func(string, snapshotLease) snapshotOwnerLiveness {
					return tc.liveness
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			_, statErr := os.Stat(path)
			gone := os.IsNotExist(statErr)
			if gone != tc.wantGone {
				t.Fatalf("gone=%v want %v report=%+v stat=%v", gone, tc.wantGone, report, statErr)
			}
			if tc.wantGone && report.Freed != 1 {
				t.Fatalf("freed=%d want 1", report.Freed)
			}
			if !tc.wantGone && report.Freed != 0 {
				t.Fatalf("freed=%d want 0 for a keep", report.Freed)
			}
		})
	}
}

func TestSnapshotReaperOldestProvenOrphanFirstAndResumes(t *testing.T) {
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	root := t.TempDir()
	lease := encodeSnapshotLease(snapshotLease{PID: 7, StartUnixNano: 1})
	k1 := plantSnapshotDir(t, root, snapshotDirectoryPrefix+"k1", now.Add(-5*time.Hour), "", 8)
	k2 := plantSnapshotDir(t, root, snapshotDirectoryPrefix+"k2", now.Add(-4*time.Hour), "", 8)
	older := plantSnapshotDir(t, root, snapshotDirectoryPrefix+"older", now.Add(-3*time.Hour), lease, 64)
	newer := plantSnapshotDir(t, root, snapshotDirectoryPrefix+"newer", now.Add(-2*time.Hour), lease, 64)
	var considered []string
	inspect := func(path string, _ snapshotLease) snapshotOwnerLiveness {
		considered = append(considered, filepath.Base(path))
		return ownerDead
	}
	pass := func(work int) reaperReport {
		report, err := runSnapshotReaper(context.Background(), reaperPass{
			root:    root,
			now:     now,
			minAge:  time.Hour,
			budget:  newReaperBudget(time.Now().Add(time.Minute), work),
			inspect: inspect,
		})
		if err != nil {
			t.Fatal(err)
		}
		return report
	}

	first := pass(1)
	if first.Exhausted != true || first.Freed != 0 {
		t.Fatalf("first pass report=%+v want exhausted keep", first)
	}
	mustExist(t, k1, k2, older, newer)
	if got := append([]string(nil), considered...); len(got) != 0 {
		t.Fatalf("first pass inspected leases %v, want none (oldest is lease-less)", got)
	}

	considered = nil
	second := pass(1)
	if second.Freed != 0 {
		t.Fatalf("second pass freed=%d want 0", second.Freed)
	}
	mustExist(t, k1, k2, older, newer)

	considered = nil
	third := pass(1)
	if third.Freed != 1 {
		t.Fatalf("third pass freed=%d want 1", third.Freed)
	}
	mustMissing(t, older)
	mustExist(t, k1, k2, newer)
	if len(considered) != 1 || considered[0] != filepath.Base(older) {
		t.Fatalf("third pass considered %v, want only the older orphan", considered)
	}

	considered = nil
	fourth := pass(1)
	if fourth.Freed != 1 {
		t.Fatalf("fourth pass freed=%d want 1", fourth.Freed)
	}
	mustMissing(t, newer)
	mustExist(t, k1, k2)
	if len(considered) != 1 || considered[0] != filepath.Base(newer) {
		t.Fatalf("fourth pass considered %v, want only the newer orphan", considered)
	}
}

func TestSnapshotReaperBudgetCoversEveryPhaseAndStops(t *testing.T) {
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	root := t.TempDir()
	lease := encodeSnapshotLease(snapshotLease{PID: 9, StartUnixNano: 2})
	for i, age := range []time.Duration{5 * time.Hour, 4 * time.Hour, 3 * time.Hour} {
		plantSnapshotDir(t, root, snapshotDirectoryPrefix+string(rune('a'+i)), now.Add(-age), lease, 16)
	}
	report, err := runSnapshotReaper(context.Background(), reaperPass{
		root:   root,
		now:    now,
		minAge: time.Hour,
		budget: newReaperBudget(time.Now().Add(-time.Second), 100),
		inspect: func(string, snapshotLease) snapshotOwnerLiveness {
			t.Fatal("inspect ran after the time budget was already gone")
			return ownerDead
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !report.Exhausted || report.Freed != 0 {
		t.Fatalf("expired time budget report=%+v", report)
	}

	var inspected int
	report, err = runSnapshotReaper(context.Background(), reaperPass{
		root:   root,
		now:    now,
		minAge: time.Hour,
		budget: newReaperBudget(time.Now().Add(time.Minute), 1),
		inspect: func(string, snapshotLease) snapshotOwnerLiveness {
			inspected++
			return ownerDead
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !report.Exhausted || report.Freed != 1 || inspected != 1 {
		t.Fatalf("work budget report=%+v inspected=%d", report, inspected)
	}
	remaining := snapshotDirs(t, root)
	if len(remaining) != 2 {
		t.Fatalf("remaining=%v want 2 newer orphans", remaining)
	}
}

func TestSnapshotReaperAccountsEachFreedDirectory(t *testing.T) {
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	root := t.TempDir()
	lease := encodeSnapshotLease(snapshotLease{PID: 3, StartUnixNano: 3})
	first := plantSnapshotDir(t, root, snapshotDirectoryPrefix+"one", now.Add(-3*time.Hour), lease, 40)
	second := plantSnapshotDir(t, root, snapshotDirectoryPrefix+"two", now.Add(-2*time.Hour), lease, 80)
	want := map[string]int64{
		filepath.Base(first):  dirSize(t, first),
		filepath.Base(second): dirSize(t, second),
	}
	var events []reaperFreed
	report, err := runSnapshotReaper(context.Background(), reaperPass{
		root:   root,
		now:    now,
		minAge: time.Hour,
		budget: newReaperBudget(time.Now().Add(time.Minute), 8),
		inspect: func(string, snapshotLease) snapshotOwnerLiveness {
			return ownerDead
		},
		onFreed: func(event reaperFreed) {
			events = append(events, event)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Freed != 2 || len(events) != 2 {
		t.Fatalf("report=%+v events=%d want 2 incremental frees", report, len(events))
	}
	if events[0].Bytes+events[1].Bytes != report.Bytes {
		t.Fatalf("per-element bytes %d+%d != total %d", events[0].Bytes, events[1].Bytes, report.Bytes)
	}
	seen := map[string]int64{}
	for _, event := range events {
		seen[filepath.Base(event.Path)] = event.Bytes
	}
	for name, size := range want {
		if seen[name] != size {
			t.Fatalf("freed %s bytes=%d want %d", name, seen[name], size)
		}
	}
}

func TestSnapshotReaperDoesNotTakeAGlobalLock(t *testing.T) {
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	root := t.TempDir()
	lease := encodeSnapshotLease(snapshotLease{PID: 5, StartUnixNano: 5})
	plantSnapshotDir(t, root, snapshotDirectoryPrefix+"left", now.Add(-3*time.Hour), lease, 8)
	plantSnapshotDir(t, root, snapshotDirectoryPrefix+"right", now.Add(-2*time.Hour), lease, 8)
	var inFlight atomic.Int32
	release := make(chan struct{})
	var releaseOnce sync.Once
	inspect := func(string, snapshotLease) snapshotOwnerLiveness {
		if inFlight.Add(1) == 2 {
			releaseOnce.Do(func() { close(release) })
		}
		select {
		case <-release:
		case <-time.After(2 * time.Second):
			t.Error("second inspect never started; a global reaper lock would stall the other pass")
		}
		inFlight.Add(-1)
		return ownerDead
	}
	var wg sync.WaitGroup
	errCh := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := runSnapshotReaper(context.Background(), reaperPass{
				root:    root,
				now:     now,
				minAge:  time.Hour,
				budget:  newReaperBudget(time.Now().Add(time.Minute), 8),
				inspect: inspect,
			})
			errCh <- err
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestSnapshotReaperKeepsLeaseUntilAfterTheDecision(t *testing.T) {
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	root := t.TempDir()
	lease := encodeSnapshotLease(snapshotLease{PID: 11, StartUnixNano: 11})
	path := plantSnapshotDir(t, root, snapshotDirectoryPrefix+"owned", now.Add(-2*time.Hour), lease, 8)
	leasePath := filepath.Join(path, snapshotLeaseName)
	var sawLease atomic.Bool
	_, err := runSnapshotReaper(context.Background(), reaperPass{
		root:   root,
		now:    now,
		minAge: time.Hour,
		budget: newReaperBudget(time.Now().Add(time.Minute), 4),
		inspect: func(candidate string, _ snapshotLease) snapshotOwnerLiveness {
			if _, err := os.Stat(leasePath); err != nil {
				t.Errorf("lease missing at decision for %s: %v", candidate, err)
			}
			if filepath.Base(candidate) != filepath.Base(path) {
				t.Errorf("decision path %q was renamed before keep-or-delete", candidate)
			}
			sawLease.Store(true)
			return ownerDead
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !sawLease.Load() {
		t.Fatal("inspect never ran")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("proven orphan still present: %v", err)
	}
}

func TestSnapshotReaperUsesProcessDeathAsPositiveProof(t *testing.T) {
	now := time.Now()
	root := t.TempDir()
	deadPID, err := exitedProcessPID()
	if err != nil {
		t.Fatal(err)
	}
	old := now.Add(-2 * time.Hour)
	livePath := plantSnapshotDir(t, root, snapshotDirectoryPrefix+"live", old, encodeSnapshotLease(currentSnapshotLease()), 8)
	deadPath := plantSnapshotDir(t, root, snapshotDirectoryPrefix+"dead", old, encodeSnapshotLease(snapshotLease{PID: deadPID}), 8)
	if _, err := runSnapshotReaper(context.Background(), reaperPass{
		root:   root,
		now:    now,
		minAge: time.Hour,
		budget: newReaperBudget(time.Now().Add(time.Minute), 8),
	}); err != nil {
		t.Fatal(err)
	}
	mustExist(t, livePath)
	mustMissing(t, deadPath)
}

func TestSaveReaperCursorDoesNotReusePredictableTempFile(t *testing.T) {
	root := t.TempDir()
	predictableTemp := filepath.Join(root, snapshotReaperCursorName+".tmp")
	canary := []byte("do not overwrite")
	if err := os.WriteFile(predictableTemp, canary, 0o600); err != nil {
		t.Fatal(err)
	}
	cursor := reaperCursor{mtime: 123, name: snapshotDirectoryPrefix + "candidate"}
	if err := saveReaperCursor(root, cursor); err != nil {
		t.Fatal(err)
	}
	gotCanary, err := os.ReadFile(predictableTemp)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotCanary) != string(canary) {
		t.Fatalf("predictable temp file changed to %q", gotCanary)
	}
	gotCursor, err := loadReaperCursor(root)
	if err != nil {
		t.Fatal(err)
	}
	if gotCursor != cursor {
		t.Fatalf("cursor=%+v want %+v", gotCursor, cursor)
	}
}

func TestOpenReadOnlySnapshotWritesALeaseAndLeavesUncertainDirs(t *testing.T) {
	root := t.TempDir()
	tmp := filepath.Join(root, "tmp")
	if err := os.Mkdir(tmp, 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := plantSnapshotDir(t, tmp, snapshotDirectoryPrefix+"legacy", time.Now().Add(-48*time.Hour), "", 16)
	t.Setenv("TMPDIR", tmp)
	dbPath := filepath.Join(root, "roca.db")
	db, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	snapshot, err := OpenReadOnlySnapshot(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	dirs := snapshotDirs(t, tmp)
	if len(dirs) < 1 {
		t.Fatal("expected a live snapshot directory")
	}
	foundLease := false
	for _, dir := range dirs {
		if dir == legacy {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, snapshotLeaseName)); err != nil {
			t.Fatalf("published snapshot %s has no lease: %v", dir, err)
		}
		foundLease = true
	}
	if !foundLease {
		t.Fatal("open did not publish a leased snapshot")
	}
	mustExist(t, legacy)
	if err := snapshot.Close(); err != nil {
		t.Fatal(err)
	}
	mustExist(t, legacy)
}

func plantSnapshotDir(t *testing.T, root, name string, mtime time.Time, lease string, payload int) string {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if lease != "" {
		if err := os.WriteFile(filepath.Join(path, snapshotLeaseName), []byte(lease), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if payload > 0 {
		if err := os.WriteFile(filepath.Join(path, "payload"), make([]byte, payload), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	return path
}

func snapshotDirs(t *testing.T, root string) []string {
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

func dirSize(t *testing.T, root string) int64 {
	t.Helper()
	var total int64
	err := filepath.Walk(root, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			total += info.Size()
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return total
}

func mustExist(t *testing.T, paths ...string) {
	t.Helper()
	for _, path := range paths {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("want %s to exist: %v", path, err)
		}
	}
}

func mustMissing(t *testing.T, paths ...string) {
	t.Helper()
	for _, path := range paths {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("want %s gone: %v", path, err)
		}
	}
}

func exitedProcessPID() (int, error) {
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	if err := cmd.Run(); err != nil {
		return 0, err
	}
	return cmd.Process.Pid, nil
}
