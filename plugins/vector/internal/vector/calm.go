package vector

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

const (
	defaultCalmPoll    = time.Second
	defaultCalmQuiet   = 2 * time.Second
	defaultCalmTimeout = 5 * time.Minute
	coreLogDirectory   = "logs"
	coreLockFilename   = ".roca.lock"
)

type CalmGate struct {
	DataDir      string
	JourneyPaths []string
	QuietPeriod  time.Duration
	PollInterval time.Duration
	Timeout      time.Duration
	Now          func() time.Time
}

func (g CalmGate) Wait(ctx context.Context) error {
	poll := g.PollInterval
	if poll <= 0 {
		poll = defaultCalmPoll
	}
	timeout := g.Timeout
	if timeout <= 0 {
		timeout = defaultCalmTimeout
	}
	deadline := time.Now().Add(timeout)
	for {
		calm, blocker, err := g.calm(ctx)
		if err != nil {
			return err
		}
		if calm {
			return nil
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("waited %s for %s to settle and it is still busy; rerun once it is idle", timeout, blocker)
		}
		timer := time.NewTimer(poll)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (g CalmGate) calm(ctx context.Context) (bool, string, error) {
	lockPath := filepath.Join(g.DataDir, coreLogDirectory, coreLockFilename)
	if _, err := os.Stat(lockPath); err == nil {
		release, busy, err := tryLockExisting(lockPath)
		if err != nil {
			return false, "", fmt.Errorf("wait for core lock: %w", err)
		}
		if busy {
			return false, "the active core write lock at " + lockPath, nil
		}
		if err := release(); err != nil {
			return false, "", fmt.Errorf("release core lock: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return false, "", fmt.Errorf("inspect core lock: %w", err)
	}

	for _, path := range g.JourneyPaths {
		if path == "" {
			continue
		}
		if _, err := os.Stat(path); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return false, "", fmt.Errorf("inspect roca-cron journey database: %w", err)
		}
		calm, recognized, err := journeyCalm(ctx, path)
		if err != nil {
			return false, "", err
		}
		if !recognized {
			continue
		}
		return calm, "the active roca-cron ingest journeys in " + path, nil
	}
	calm, err := g.ingestLogCalm()
	return calm, "core ingest activity in " + filepath.Join(g.DataDir, coreLogDirectory), err
}

func DefaultJourneyPaths(dataDir, home, override string) []string {
	paths := []string{override, filepath.Join(dataDir, "journeys.db"),
		filepath.Join(dataDir, "cron", "journeys.db"), filepath.Join(dataDir, "roca-cron", "journeys.db")}
	if home != "" {
		paths = append(paths, filepath.Join(home, ".roca-cron", "journeys.db"),
			filepath.Join(home, ".local", "share", "roca-cron", "journeys.db"),
			filepath.Join(home, "Library", "Application Support", "roca-cron", "journeys.db"))
	}
	return slices.Compact(paths)
}

func journeyCalm(ctx context.Context, path string) (bool, bool, error) {
	db, err := openSQLite(path, true)
	if err != nil {
		return false, false, fmt.Errorf("open roca-cron journey database: %w", err)
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type='table' AND lower(name) LIKE '%journey%'`)
	if err != nil {
		return false, false, fmt.Errorf("read roca-cron journey database: %w", err)
	}
	var tables []string
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			rows.Close()
			return false, false, err
		}
		tables = append(tables, table)
	}
	rows.Close()
	recognized := false
	for _, table := range tables {
		status, operation, err := journeyColumns(ctx, db, table)
		if err != nil {
			return false, false, err
		}
		if status == "" {
			continue
		}
		recognized = true
		predicate := fmt.Sprintf(`lower(%s) IN ('running','started','in_progress','active')`, quoteIdentifier(status))
		if operation != "" {
			predicate += fmt.Sprintf(` AND lower(%s) LIKE '%%ingest%%'`, quoteIdentifier(operation))
		}
		var count int
		query := fmt.Sprintf(`SELECT COUNT(*) FROM %s WHERE %s`, quoteIdentifier(table), predicate)
		if err := db.QueryRowContext(ctx, query).Scan(&count); err != nil {
			return false, false, fmt.Errorf("read active roca-cron journeys: %w", err)
		}
		if count > 0 {
			return false, true, nil
		}
	}
	return true, recognized, nil
}

func journeyColumns(ctx context.Context, db *sql.DB, table string) (string, string, error) {
	rows, err := db.QueryContext(ctx, `PRAGMA table_info(`+quoteIdentifier(table)+`)`)
	if err != nil {
		return "", "", err
	}
	defer rows.Close()
	var status, operation string
	for rows.Next() {
		var cid, notNull, pk int
		var name, kind string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &kind, &notNull, &defaultValue, &pk); err != nil {
			return "", "", err
		}
		switch strings.ToLower(name) {
		case "status", "state":
			if status == "" {
				status = name
			}
		case "command", "operation", "action", "task", "name", "kind":
			if operation == "" {
				operation = name
			}
		}
	}
	return status, operation, rows.Err()
}

func quoteIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

func (g CalmGate) ingestLogCalm() (bool, error) {
	matches, err := filepath.Glob(filepath.Join(g.DataDir, coreLogDirectory, "ingest-*.jsonl"))
	if err != nil || len(matches) == 0 {
		return true, err
	}
	slices.Sort(matches)
	file, err := os.Open(matches[len(matches)-1])
	if err != nil {
		return false, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return false, fmt.Errorf("inspect core ingest log: %w", err)
	}
	var last string
	scanner := bufio.NewScanner(file)
	buffer := make([]byte, 64*1024)
	scanner.Buffer(buffer, 8<<20)
	for scanner.Scan() {
		if strings.TrimSpace(scanner.Text()) != "" {
			last = scanner.Text()
		}
	}
	if err := scanner.Err(); err != nil {
		return false, fmt.Errorf("read core ingest log: %w", err)
	}
	if last == "" {
		return true, nil
	}
	var record struct {
		Timestamp  time.Time `json:"timestamp"`
		DurationMS int64     `json:"duration_ms"`
	}
	if err := json.Unmarshal([]byte(last), &record); err != nil {
		return false, fmt.Errorf("read latest core ingest record: %w", err)
	}
	quiet := g.QuietPeriod
	if quiet <= 0 {
		quiet = defaultCalmQuiet
	}
	now := time.Now
	if g.Now != nil {
		now = g.Now
	}
	return !now().UTC().Before(ingestFinished(record.Timestamp, record.DurationMS, info.ModTime()).Add(quiet)), nil
}

func ingestFinished(started time.Time, durationMS int64, appended time.Time) time.Time {
	finished := appended.UTC()
	if started.IsZero() {
		return finished
	}
	if ended := started.UTC().Add(time.Duration(durationMS) * time.Millisecond); ended.After(finished) {
		finished = ended
	}
	return finished
}
