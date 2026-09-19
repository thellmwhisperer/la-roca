package ingestprovenance

import (
	"context"
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

func TestBackfillSkipsSourceSurfaceWhenExactPayloadWouldCollide(t *testing.T) {
	db, err := sql.Open("sqlite", "file:backfill-surface?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, statement := range []string{
		`CREATE TABLE sessions (
			session_id TEXT PRIMARY KEY,
			source_agent TEXT,
			title TEXT,
			started_at TEXT,
			metadata TEXT,
			source_surface TEXT
		)`,
		`CREATE UNIQUE INDEX idx_sessions_exact_payload
			ON sessions(source_agent, title, started_at, metadata, source_surface)`,
		`CREATE TABLE memories (
			id INTEGER PRIMARY KEY,
			origin TEXT,
			metadata TEXT,
			source_agent TEXT,
			source_surface TEXT
		)`,
		`CREATE TABLE exchanges (
			id INTEGER PRIMARY KEY,
			session_id TEXT,
			model TEXT,
			provider TEXT
		)`,
		`INSERT INTO sessions(session_id, source_agent, title, started_at, metadata, source_surface)
			VALUES ('labeled', 'claude', 'same', '2026-08-16T10:00:00Z', '{}', 'Claude Code')`,
		`INSERT INTO sessions(session_id, source_agent, title, started_at, metadata, source_surface)
			VALUES ('unlabeled', 'claude', 'same', '2026-08-16T10:00:00Z', '{}', '')`,
		`INSERT INTO sessions(session_id, source_agent, title, started_at, metadata, source_surface)
			VALUES ('fillable', 'claude', 'other', '2026-08-16T10:00:00Z', '{}', '')`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}

	if err := Backfill(context.Background(), db); err != nil {
		t.Fatalf("Backfill = %v", err)
	}
	if err := Backfill(context.Background(), db); err != nil {
		t.Fatalf("Backfill second pass = %v", err)
	}

	want := map[string]string{
		"labeled":   ClaudeCode,
		"unlabeled": "",
		"fillable":  ClaudeCode,
	}
	for sessionID, surface := range want {
		var got sql.NullString
		if err := db.QueryRow(`SELECT source_surface FROM sessions WHERE session_id = ?`,
			sessionID).Scan(&got); err != nil {
			t.Fatalf("read %s: %v", sessionID, err)
		}
		if got.String != surface {
			t.Fatalf("%s source_surface = %q, want %q", sessionID, got.String, surface)
		}
	}
}
