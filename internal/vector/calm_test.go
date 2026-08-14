package vector

import (
	"context"
	"database/sql"
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
				writeIngestLog(t, directory, now.Add(-time.Second))
			}
			gate := CalmGate{DataDir: directory, JourneyPaths: []string{journeyPath},
				QuietPeriod: 2 * time.Second, Now: func() time.Time { return now }}
			calm, _, err := gate.calm(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if calm != test.want {
				t.Fatalf("calm = %t, want %t", calm, test.want)
			}
		})
	}
}

func TestWaitGivesUpWithABoundedTimeoutThatNamesTheBlocker(t *testing.T) {
	directory := t.TempDir()
	writeIngestLog(t, directory, time.Now().UTC())
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

func writeIngestLog(t *testing.T, directory string, stamp time.Time) {
	t.Helper()
	logs := filepath.Join(directory, "logs")
	if err := os.MkdirAll(logs, 0o700); err != nil {
		t.Fatal(err)
	}
	record := `{"timestamp":"` + stamp.Format(time.RFC3339Nano) + `","ok":true}` + "\n"
	if err := os.WriteFile(filepath.Join(logs, "ingest-2026-08-14.jsonl"), []byte(record), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestCalmFallsBackToLatestCoreIngestLog(t *testing.T) {
	directory := t.TempDir()
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	writeIngestLog(t, directory, now.Add(-time.Second))
	gate := CalmGate{DataDir: directory, QuietPeriod: 2 * time.Second, Now: func() time.Time { return now }}
	calm, _, err := gate.calm(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if calm {
		t.Fatal("fresh ingest log was reported calm")
	}
	gate.Now = func() time.Time { return now.Add(3 * time.Second) }
	calm, _, err = gate.calm(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !calm {
		t.Fatal("settled ingest log was not reported calm")
	}
}
