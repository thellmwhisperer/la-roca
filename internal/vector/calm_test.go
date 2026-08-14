package vector

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCalmPrefersJourneyDatabaseWhenPresent(t *testing.T) {
	directory := t.TempDir()
	journeyPath := filepath.Join(directory, "journeys.db")
	db, err := sql.Open("sqlite", journeyPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE journeys(command TEXT, status TEXT); INSERT INTO journeys VALUES ('roca ingest','running')`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	gate := CalmGate{DataDir: directory, JourneyPaths: []string{journeyPath}}
	calm, err := gate.calm(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if calm {
		t.Fatal("running ingest journey was reported calm")
	}
}

func TestCalmFallsBackToLatestCoreIngestLog(t *testing.T) {
	directory := t.TempDir()
	logs := filepath.Join(directory, "logs")
	if err := os.MkdirAll(logs, 0o700); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(logs, "ingest-2026-08-14.jsonl")
	if err := os.WriteFile(path, []byte(`{"timestamp":"2026-08-14T11:59:59Z","ok":true}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gate := CalmGate{DataDir: directory, QuietPeriod: 2 * time.Second, Now: func() time.Time { return now }}
	calm, err := gate.calm(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if calm {
		t.Fatal("fresh ingest log was reported calm")
	}
	gate.Now = func() time.Time { return now.Add(3 * time.Second) }
	calm, err = gate.calm(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !calm {
		t.Fatal("settled ingest log was not reported calm")
	}
}
