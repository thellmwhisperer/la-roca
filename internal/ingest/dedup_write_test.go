package ingest

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/ingest/parsers"
	"github.com/thellmwhisperer/la-roca/internal/store/exactdedup"
)

func TestPatchMetadataDoesNotBreakTheExactPayloadIndex(t *testing.T) {
	db := rocaDatabase(t)
	if err := exactdedup.EnsureGuards(context.Background(), db.SQL()); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	write := func(id string) error {
		return db.Write(ctx, func(tx *sql.Tx) error {
			_, err := WriteRecords(ctx, tx, registry(t), parsers.Records{Sessions: []parsers.Session{{
				ID: id, SourceAgent: "claude",
				Metadata: map[string]any{"cwd": "/synthetic/demo"},
			}}})
			return err
		})
	}
	if err := write("session-one"); err != nil {
		t.Fatalf("first session: %v", err)
	}
	if err := write("session-two"); err != nil {
		t.Fatalf("colliding metadata patch aborted the write: %v", err)
	}
	var count int
	if err := db.SQL().QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("sessions = %d, want both rows to land", count)
	}
	var firstCwd, secondCwd sql.NullString
	if err := db.SQL().QueryRow(`
		SELECT
		  (SELECT json_extract(metadata, '$.cwd') FROM sessions WHERE session_id = 'session-one'),
		  (SELECT json_extract(metadata, '$.cwd') FROM sessions WHERE session_id = 'session-two')`,
	).Scan(&firstCwd, &secondCwd); err != nil {
		t.Fatal(err)
	}
	if !firstCwd.Valid || firstCwd.String != "/synthetic/demo" {
		t.Fatalf("first metadata cwd = %q, want the committed patch", firstCwd.String)
	}
	if secondCwd.Valid {
		t.Fatalf("second metadata cwd = %q, want its pre-patch metadata unchanged", secondCwd.String)
	}
}

func TestAMetadataPayloadCollisionDoesNotAbortLaterSources(t *testing.T) {
	result, sessions := runCollidingSources(t, func(world *world) string {
		return collidingClaudeTranscript(world.demoCwd())
	})
	if result.WriteFailed != 0 {
		t.Fatalf("write_failed = %d, want the metadata collision resolved: %+v",
			result.WriteFailed, result.ErrorDetails)
	}
	if grok := result.Sources["grok"]; grok == nil || grok.Sessions+grok.SessionsUpdated == 0 {
		t.Fatalf("later grok source did not run: %+v", result.Sources)
	}
	if sessions < 3 {
		t.Fatalf("sessions = %d, want both Claude rows and the later grok session", sessions)
	}
}

func TestOneWriteFailureDoesNotAbortLaterSources(t *testing.T) {
	body := `{"type":"user","timestamp":"2026-08-01T10:00:00Z","message":{"content":"question"}}
{"type":"assistant","timestamp":"2026-08-01T10:00:01Z","message":{"content":[{"type":"text","text":"answer"}]}}
`
	result, _ := runCollidingSources(t, func(*world) string { return body })
	if result.WriteFailed == 0 || result.Errors == 0 {
		t.Fatalf("the colliding insert was not isolated: errors=%d write_failed=%d details=%+v",
			result.Errors, result.WriteFailed, result.ErrorDetails)
	}
	if stats := result.SourceStats["claude"]; stats == nil || stats.FilesWriteFailed != 1 {
		t.Fatalf("claude write failure stats = %+v, want one", stats)
	}
	if !coverageHas(result, "write failed") {
		t.Fatalf("coverage did not name the write failure: %+v", result.Coverage)
	}
	if grok := result.Sources["grok"]; grok == nil || grok.Sessions+grok.SessionsUpdated == 0 {
		t.Fatalf("one write failure aborted later sources: %+v", result.Sources)
	}
}

func runCollidingSources(t *testing.T, transcript func(*world) string) (Result, int) {
	t.Helper()
	world, roots := collisionWorld(t)
	project := filepath.Join(roots.ClaudeProjects, world.projectDir())
	body := transcript(world)
	world.write(t, filepath.Join(project, "aaaaaaaa-bbbb-cccc-dddd-111111111111.jsonl"), body)
	world.write(t, filepath.Join(project, "bbbbbbbb-cccc-dddd-eeee-222222222222.jsonl"), body)
	world.seedGrok(t, roots)

	db := rocaDatabase(t)
	if err := exactdedup.EnsureGuards(context.Background(), db.SQL()); err != nil {
		t.Fatal(err)
	}
	result, err := Run(context.Background(), db, registry(t), Options{Roots: roots})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	var sessions int
	if err := db.SQL().QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	return result, sessions
}

func collisionWorld(t *testing.T) (*world, Roots) {
	t.Helper()
	home := t.TempDir()
	workspace := filepath.Join(home, "w")
	w := &world{
		home:      home,
		workspace: workspace,
		export:    filepath.Join(home, "unused-export"),
		env:       Environment{GOOS: "darwin", Home: home},
		settings:  Settings{WorkspaceRoots: []string{workspace}},
	}
	return w, w.roots()
}

func collidingClaudeTranscript(cwd string) string {
	return fmt.Sprintf("{\"type\":\"user\",\"timestamp\":\"2026-08-01T10:00:00Z\",\"cwd\":%q,\"message\":{\"content\":\"question\"}}\n"+
		"{\"type\":\"assistant\",\"timestamp\":\"2026-08-01T10:00:01Z\",\"message\":{\"content\":[{\"type\":\"text\",\"text\":\"answer\"}]}}\n", cwd)
}

func coverageHas(result Result, reason string) bool {
	for _, category := range result.Coverage.Files.Skips {
		if strings.Contains(category.Reason, reason) {
			return true
		}
	}
	return false
}
