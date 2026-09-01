package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	snapshotLeaseName        = "lease"
	snapshotReaperCursorName = ".roca-snapshot-reaper-cursor"
	snapshotReapMinAge       = time.Hour
	snapshotReapTimeBudget   = 250 * time.Millisecond
	snapshotReapWorkBudget   = 32
)

var (
	errSnapshotLeaseMissing   = errors.New("snapshot lease is missing")
	errSnapshotLeaseMalformed = errors.New("snapshot lease is malformed")
	errSnapshotStartUnknown   = errors.New("process start time is unknown")
	errSnapshotOwnerUncertain = errors.New("process liveness is uncertain")
)

type snapshotOwnerLiveness int

const (
	ownerLive snapshotOwnerLiveness = iota
	ownerDead
	ownerUncertain
)

type snapshotLease struct {
	PID           int
	StartUnixNano int64
}

type reaperBudget struct {
	deadline time.Time
	work     int
}

type reaperFreed struct {
	Path  string
	Bytes int64
}

type reaperPass struct {
	root    string
	now     time.Time
	budget  *reaperBudget
	minAge  time.Duration
	inspect func(string, snapshotLease) snapshotOwnerLiveness
	onFreed func(reaperFreed)
}

type reaperReport struct {
	Inspected int
	Freed     int
	Bytes     int64
	Exhausted bool
}

type reaperCursor struct {
	mtime int64
	name  string
}

type snapshotCandidate struct {
	name  string
	path  string
	mtime int64
}

func newReaperBudget(deadline time.Time, work int) *reaperBudget {
	return &reaperBudget{deadline: deadline, work: work}
}

func (budget *reaperBudget) ok(ctx context.Context) bool {
	if ctx.Err() != nil {
		return false
	}
	if budget == nil {
		return true
	}
	if budget.work <= 0 {
		return false
	}
	if !budget.deadline.IsZero() && !time.Now().Before(budget.deadline) {
		return false
	}
	return true
}

func (budget *reaperBudget) consume() {
	if budget != nil {
		budget.work--
	}
}

func reapReadOnlySnapshots(ctx context.Context, root string, now time.Time) error {
	deadline := time.Now().Add(snapshotReapTimeBudget)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	_, err := runSnapshotReaper(ctx, reaperPass{
		root:   root,
		now:    now,
		minAge: snapshotReapMinAge,
		budget: newReaperBudget(deadline, snapshotReapWorkBudget),
	})
	return err
}

func runSnapshotReaper(ctx context.Context, pass reaperPass) (reaperReport, error) {
	var report reaperReport
	if !pass.budget.ok(ctx) {
		report.Exhausted = true
		return report, nil
	}
	entries, err := os.ReadDir(pass.root)
	if err != nil {
		return report, fmt.Errorf("inspect read-only snapshot directory: %w", err)
	}
	if !pass.budget.ok(ctx) {
		report.Exhausted = true
		return report, nil
	}
	candidates := make([]snapshotCandidate, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), snapshotDirectoryPrefix) {
			continue
		}
		path := filepath.Join(pass.root, entry.Name())
		info, err := os.Lstat(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			continue
		}
		if !info.IsDir() {
			continue
		}
		candidates = append(candidates, snapshotCandidate{
			name:  entry.Name(),
			path:  path,
			mtime: info.ModTime().UnixNano(),
		})
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].mtime != candidates[j].mtime {
			return candidates[i].mtime < candidates[j].mtime
		}
		return candidates[i].name < candidates[j].name
	})
	if !pass.budget.ok(ctx) {
		report.Exhausted = true
		return report, nil
	}

	cursor, _ := loadReaperCursor(pass.root)
	selected := candidatesAfter(candidates, cursor)
	if len(selected) == 0 {
		_ = saveReaperCursor(pass.root, reaperCursor{})
		return report, nil
	}

	minAge := pass.minAge
	if minAge == 0 {
		minAge = snapshotReapMinAge
	}
	inspect := pass.inspect
	if inspect == nil {
		inspect = inspectSnapshotOwner
	}

	for _, candidate := range selected {
		if !pass.budget.ok(ctx) {
			report.Exhausted = true
			break
		}
		report.Inspected++
		freed, size, retry, err := reapSnapshotCandidate(ctx, pass, candidate, minAge, inspect)
		if err != nil {
			return report, err
		}
		if retry {
			report.Exhausted = true
			break
		}
		if freed {
			event := reaperFreed{Path: candidate.path, Bytes: size}
			report.Freed++
			report.Bytes += size
			if pass.onFreed != nil {
				pass.onFreed(event)
			}
		}
		cursor = reaperCursor{mtime: candidate.mtime, name: candidate.name}
		pass.budget.consume()
	}
	_ = saveReaperCursor(pass.root, cursor)
	if pass.budget != nil && !pass.budget.ok(ctx) {
		report.Exhausted = true
	}
	return report, nil
}

func reapSnapshotCandidate(ctx context.Context, pass reaperPass, candidate snapshotCandidate, minAge time.Duration, inspect func(string, snapshotLease) snapshotOwnerLiveness) (freed bool, size int64, retry bool, err error) {
	lease, leaseErr := readSnapshotLease(candidate.path)
	if leaseErr != nil {
		return false, 0, false, nil
	}
	age := pass.now.Sub(time.Unix(0, candidate.mtime))
	if age < minAge {
		return false, 0, false, nil
	}
	if inspect(candidate.path, lease) != ownerDead {
		return false, 0, false, nil
	}
	size, _ = snapshotDirSize(ctx, candidate.path)
	if removeErr := os.RemoveAll(candidate.path); removeErr != nil && !os.IsNotExist(removeErr) {
		return false, 0, true, nil
	}
	if _, statErr := os.Stat(candidate.path); statErr == nil {
		return false, 0, true, nil
	} else if !os.IsNotExist(statErr) {
		return false, 0, true, nil
	}
	return true, size, false, nil
}

func candidatesAfter(candidates []snapshotCandidate, cursor reaperCursor) []snapshotCandidate {
	if cursor.name == "" && cursor.mtime == 0 {
		return candidates
	}
	var selected []snapshotCandidate
	for _, candidate := range candidates {
		if cursor.before(candidate.mtime, candidate.name) {
			selected = append(selected, candidate)
		}
	}
	return selected
}

func (cursor reaperCursor) before(mtime int64, name string) bool {
	if cursor.mtime != mtime {
		return cursor.mtime < mtime
	}
	return cursor.name < name
}

func encodeSnapshotLease(lease snapshotLease) string {
	var builder strings.Builder
	builder.WriteString("v1\n")
	fmt.Fprintf(&builder, "pid=%d\n", lease.PID)
	if lease.StartUnixNano != 0 {
		fmt.Fprintf(&builder, "start_unix_nano=%d\n", lease.StartUnixNano)
	}
	return builder.String()
}

func writeSnapshotLease(directory string, lease snapshotLease) error {
	return os.WriteFile(filepath.Join(directory, snapshotLeaseName), []byte(encodeSnapshotLease(lease)), 0o600)
}

func readSnapshotLease(directory string) (snapshotLease, error) {
	body, err := os.ReadFile(filepath.Join(directory, snapshotLeaseName))
	if err != nil {
		if os.IsNotExist(err) {
			return snapshotLease{}, errSnapshotLeaseMissing
		}
		return snapshotLease{}, err
	}
	return parseSnapshotLease(string(body))
}

func parseSnapshotLease(body string) (snapshotLease, error) {
	lines := strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
	if len(lines) == 0 || lines[0] != "v1" {
		return snapshotLease{}, errSnapshotLeaseMalformed
	}
	var lease snapshotLease
	var sawPID bool
	for _, line := range lines[1:] {
		if line == "" {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return snapshotLease{}, errSnapshotLeaseMalformed
		}
		switch key {
		case "pid":
			pid, err := strconv.Atoi(value)
			if err != nil || pid <= 0 {
				return snapshotLease{}, errSnapshotLeaseMalformed
			}
			lease.PID = pid
			sawPID = true
		case "start_unix_nano":
			nano, err := strconv.ParseInt(value, 10, 64)
			if err != nil {
				return snapshotLease{}, errSnapshotLeaseMalformed
			}
			lease.StartUnixNano = nano
		}
	}
	if !sawPID {
		return snapshotLease{}, errSnapshotLeaseMalformed
	}
	return lease, nil
}

func currentSnapshotLease() snapshotLease {
	lease := snapshotLease{PID: os.Getpid()}
	if start, err := processStartUnixNano(lease.PID); err == nil {
		lease.StartUnixNano = start
	}
	return lease
}

func inspectSnapshotOwner(_ string, lease snapshotLease) snapshotOwnerLiveness {
	exists, err := pidExists(lease.PID)
	if err != nil {
		return ownerUncertain
	}
	if !exists {
		return ownerDead
	}
	if lease.StartUnixNano == 0 {
		return ownerLive
	}
	start, err := processStartUnixNano(lease.PID)
	if err != nil {
		return ownerUncertain
	}
	if start == lease.StartUnixNano {
		return ownerLive
	}
	return ownerDead
}

func snapshotDirSize(ctx context.Context, root string) (int64, error) {
	var total int64
	err := filepath.WalkDir(root, func(_ string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		return nil
	})
	return total, err
}

func loadReaperCursor(root string) (reaperCursor, error) {
	body, err := os.ReadFile(filepath.Join(root, snapshotReaperCursorName))
	if err != nil {
		if os.IsNotExist(err) {
			return reaperCursor{}, nil
		}
		return reaperCursor{}, err
	}
	lines := strings.Split(strings.ReplaceAll(string(body), "\r\n", "\n"), "\n")
	if len(lines) < 2 || lines[0] != "v1" {
		return reaperCursor{}, nil
	}
	mtimeText, name, ok := strings.Cut(lines[1], " ")
	if !ok || name == "" {
		return reaperCursor{}, nil
	}
	mtime, err := strconv.ParseInt(mtimeText, 10, 64)
	if err != nil {
		return reaperCursor{}, nil
	}
	return reaperCursor{mtime: mtime, name: name}, nil
}

func saveReaperCursor(root string, cursor reaperCursor) error {
	path := filepath.Join(root, snapshotReaperCursorName)
	if cursor.name == "" && cursor.mtime == 0 {
		err := os.Remove(path)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	payload := fmt.Sprintf("v1\n%d %s\n", cursor.mtime, cursor.name)
	tmpFile, err := os.CreateTemp(root, snapshotReaperCursorName+".tmp-*")
	if err != nil {
		return err
	}
	tmp := tmpFile.Name()
	defer os.Remove(tmp)
	if _, err := tmpFile.WriteString(payload); err != nil {
		_ = tmpFile.Close()
		return err
	}
	if err := tmpFile.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
