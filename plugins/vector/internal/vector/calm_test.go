package vector

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCalmPrefersJourneyDatabaseAndFallsBackWhenItsSchemaIsUnknown(t *testing.T) {
	cases := []struct {
		name    string
		journey string
		busyLog bool
		want    bool
	}{
		{name: "a running ingest journey is not calm", want: false,
			journey: `CREATE TABLE journeys(command TEXT, status TEXT); INSERT INTO journeys VALUES ('roca ingest','running')`},
		{name: "an unknown journey schema defers to a busy ingest log", busyLog: true, want: false,
			journey: `CREATE TABLE journeys(command TEXT, phase TEXT); INSERT INTO journeys VALUES ('roca ingest','running')`},
		{name: "an unknown journey schema defers to a quiet ingest log", want: true,
			journey: `CREATE TABLE journeys(command TEXT, phase TEXT); INSERT INTO journeys VALUES ('roca ingest','running')`},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			journeyPath := filepath.Join(directory, "journeys.db")
			db, err := sql.Open("sqlite", journeyPath)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(test.journey); err != nil {
				t.Fatal(err)
			}
			db.Close()
			now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
			if test.busyLog {
				writeIngestLog(t, directory, now.Add(-time.Second), 0)
			}
			gate := CalmGate{DataDir: directory, JourneyPaths: []string{journeyPath},
				QuietPeriod: 2 * time.Second, Now: func() time.Time { return now }}
			if calm := calmNow(t, gate); calm != test.want {
				t.Fatalf("calm = %t, want %t", calm, test.want)
			}
		})
	}
}

func TestWaitGivesUpWithABoundedTimeoutThatNamesTheBlocker(t *testing.T) {
	directory := t.TempDir()
	writeIngestLog(t, directory, time.Now().UTC(), 0)
	gate := CalmGate{DataDir: directory, QuietPeriod: time.Hour,
		PollInterval: time.Millisecond, Timeout: 20 * time.Millisecond}
	err := gate.Wait(context.Background())
	if err == nil {
		t.Fatal("wait returned before the corpus settled")
	}
	if !strings.Contains(err.Error(), "core ingest activity") {
		t.Fatalf("timeout error does not name what it waited on: %v", err)
	}
}

func TestCalmChecksTheCoreLockWithoutBlockingItsOwnTimeout(t *testing.T) {
	directory := t.TempDir()
	logs := filepath.Join(directory, coreLogDirectory)
	if err := os.MkdirAll(logs, 0o700); err != nil {
		t.Fatal(err)
	}
	release, err := lockFile(filepath.Join(logs, coreLockFilename))
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	started := time.Now()
	calm, blocker, err := (CalmGate{DataDir: directory}).calm(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if calm || !strings.Contains(blocker, "core write lock") {
		t.Fatalf("calm=%t blocker=%q", calm, blocker)
	}
	if time.Since(started) > time.Second {
		t.Fatal("probing an active core lock blocked instead of returning a busy verdict")
	}
}

// calmNow reads the gate's verdict at the instant its clock reports. Every
// calm case asks the same question, and a failure to read the signals at all is
// never one of the answers under test.
func calmNow(t *testing.T, gate CalmGate) bool {
	t.Helper()
	calm, _, err := gate.calm(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return calm
}

func writeIngestLog(t *testing.T, directory string, started time.Time, duration time.Duration) {
	t.Helper()
	logs := filepath.Join(directory, "logs")
	if err := os.MkdirAll(logs, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(logs, "ingest-2026-08-14.jsonl")
	record := fmt.Sprintf("{\"timestamp\":%q,\"ok\":true,\"duration_ms\":%d}\n",
		started.Format(time.RFC3339Nano), duration.Milliseconds())
	if err := os.WriteFile(path, []byte(record), 0o600); err != nil {
		t.Fatal(err)
	}
	finished := started.Add(duration)
	if err := os.Chtimes(path, finished, finished); err != nil {
		t.Fatal(err)
	}
}

func TestCalmFallsBackToLatestCoreIngestLog(t *testing.T) {
	cases := []struct {
		name     string
		started  time.Duration
		duration time.Duration
	}{
		{name: "a run that just finished", started: -time.Second},
		{name: "a long run whose record carries an old start stamp", started: -90 * time.Second, duration: 90 * time.Second},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
			writeIngestLog(t, directory, now.Add(test.started), test.duration)
			gate := CalmGate{DataDir: directory, QuietPeriod: 2 * time.Second, Now: func() time.Time { return now }}
			if calmNow(t, gate) {
				t.Fatal("an ingest that finished inside the quiet period was reported calm")
			}
			gate.Now = func() time.Time { return now.Add(3 * time.Second) }
			if !calmNow(t, gate) {
				t.Fatal("settled ingest log was not reported calm")
			}
		})
	}
}
