package ingest

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/ingest/parsers"
)

func TestDeclaredClaudeWebExportIsIncrementalAndIdempotent(t *testing.T) {
	db := rocaDatabase(t)
	roots := Roots{ClaudeWebExports: []string{
		filepath.Join("testdata", "anthropic-export"),
	}}
	first, err := Run(context.Background(), db, registry(t), Options{Roots: roots})
	if err != nil {
		t.Fatal(err)
	}
	if first.Errors != 0 || first.Delta.Sessions != 2 ||
		first.Delta.Exchanges != 4 || first.Delta.Memories != 1 {
		t.Fatalf("first ingest = %+v", first)
	}
	if first.RecordsDiscarded != 0 {
		t.Fatalf("discards = %+v", first.DiscardDetails)
	}

	second, err := Run(context.Background(), db, registry(t), Options{Roots: roots})
	if err != nil {
		t.Fatal(err)
	}
	if second.FilesSkipped != 2 || second.Delta != (Tables{}) {
		t.Fatalf("second ingest = %+v", second)
	}
	if got := countRows(t, db.SQL(), "ingest_file_state"); got != 2 {
		t.Fatalf("file-state rows = %d, want 2", got)
	}
	var project string
	if err := db.SQL().QueryRow(
		"SELECT COALESCE(project, '') FROM sessions WHERE session_id = ?",
		"10000000-0000-4000-8000-000000000001").Scan(&project); err != nil {
		t.Fatal(err)
	}
	if project != "" {
		t.Fatalf("Claude web project = %q, want unset", project)
	}
	var metadata string
	if err := db.SQL().QueryRow(
		"SELECT metadata FROM sessions WHERE session_id = ?",
		"10000000-0000-4000-8000-000000000001").Scan(&metadata); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"invented-sensors.txt", "fictional-labels.csv"} {
		if !strings.Contains(metadata, name) {
			t.Errorf("session exchange metadata does not contain %q: %s", name, metadata)
		}
	}
}

func TestGrownClaudeWebExportAddsOnlyNewMessageIdentities(t *testing.T) {
	export := copyClaudeWebFixture(t)
	db := rocaDatabase(t)
	roots := Roots{ClaudeWebExports: []string{export}}
	if _, err := Run(context.Background(), db, registry(t), Options{Roots: roots}); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(export, "conversations.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var conversations []map[string]any
	if err := json.Unmarshal(raw, &conversations); err != nil {
		t.Fatal(err)
	}
	messages := conversations[1]["chat_messages"].([]any)
	messages = append(messages,
		map[string]any{
			"uuid": "30000000-0000-4000-8000-000000000003",
			"text": "Add a fictional lens check.", "sender": "human",
			"created_at":          "2026-08-01T09:01:00.000Z",
			"updated_at":          "2026-08-01T09:01:00.000Z",
			"parent_message_uuid": "30000000-0000-4000-8000-000000000002",
			"attachments":         []any{}, "files": []any{}, "content": []any{},
		},
		map[string]any{
			"uuid": "30000000-0000-4000-8000-000000000004",
			"text": "The synthetic lens is clear.", "sender": "assistant",
			"created_at":          "2026-08-01T09:01:01.000Z",
			"updated_at":          "2026-08-01T09:01:01.000Z",
			"parent_message_uuid": "30000000-0000-4000-8000-000000000003",
			"attachments":         []any{}, "files": []any{}, "content": []any{},
		})
	conversations[1]["chat_messages"] = messages
	grown, err := json.Marshal(conversations)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, grown, 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := Run(context.Background(), db, registry(t), Options{Roots: roots})
	if err != nil {
		t.Fatal(err)
	}
	if result.Delta != (Tables{Exchanges: 1}) {
		t.Fatalf("grown export delta = %+v", result.Delta)
	}
	counts := result.Sources["claude-web"]
	if counts.Exchanges != 1 || counts.ExchangesUnchanged != 4 {
		t.Fatalf("grown export counts = %+v", counts)
	}
}

func TestClaudeWebParserRevisionBackfillsLegacyCascadeWithoutDuplicates(t *testing.T) {
	export := t.TempDir()
	path := filepath.Join(export, "conversations.json")
	raw, err := os.ReadFile(filepath.Join("testdata", "anthropic-export", "discarded-ancestors.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	records, err := parsers.ParseClaudeWebConversations(strings.NewReader(string(raw)), parsers.FileMeta{
		Path: path, FileName: "conversations.json",
	})
	if err != nil {
		t.Fatal(err)
	}
	records.Sessions[0].Exchanges = nil
	records.Sessions[1].Exchanges = records.Sessions[1].Exchanges[:1]

	db := rocaDatabase(t)
	target := Target{Path: path, Kind: parsers.KindClaudeWebConversations, SourceAgent: "claude-web"}
	legacyFingerprint, err := Fingerprint(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Write(context.Background(), func(tx *sql.Tx) error {
		if _, err := WriteRecords(context.Background(), tx, registry(t), records); err != nil {
			return err
		}
		return RecordState(context.Background(), tx, target, legacyFingerprint, "", nil)
	}); err != nil {
		t.Fatal(err)
	}

	first, err := Run(context.Background(), db, registry(t), Options{
		Roots: Roots{ClaudeWebExports: []string{export}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.FilesRead != 1 || first.Delta != (Tables{Exchanges: 2}) {
		t.Fatalf("recovery ingest = %+v", first)
	}
	counts := first.Sources["claude-web"]
	if counts.Exchanges != 2 || counts.ExchangesUnchanged != 1 ||
		countRows(t, db.SQL(), "sessions") != 2 || countRows(t, db.SQL(), "exchanges") != 3 {
		t.Fatalf("recovery counts = %+v, sessions/exchanges = %d/%d", counts,
			countRows(t, db.SQL(), "sessions"), countRows(t, db.SQL(), "exchanges"))
	}
	if first.RecordsDiscarded != 2 {
		t.Fatalf("recovery discards = %+v", first.DiscardDetails)
	}
	for _, detail := range first.DiscardDetails {
		if !strings.Contains(detail.Reason, "has no text") || strings.Contains(detail.Reason, "parent chain") {
			t.Errorf("imprecise recovery discard = %+v", detail)
		}
	}

	second, err := Run(context.Background(), db, registry(t), Options{
		Roots: Roots{ClaudeWebExports: []string{export}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.FilesSkipped != 1 || second.Delta != (Tables{}) ||
		countRows(t, db.SQL(), "sessions") != 2 || countRows(t, db.SQL(), "exchanges") != 3 {
		t.Fatalf("idempotent recovery ingest = %+v", second)
	}
}

func TestMultipleClaudeWebExportsKeepNewestSnapshotAndOneMemory(t *testing.T) {
	directories := []struct {
		path, name, summary, updated, memory, memoryUpdated string
	}{
		{filepath.Join(t.TempDir(), "oldest"), "Oldest synthetic snapshot", "Oldest synthetic summary.", "2026-08-01T09:01:00Z", "Newest synthetic memory.", "2026-08-01T08:01:00Z"},
		{filepath.Join(t.TempDir(), "newest"), "Newest synthetic snapshot", "Newest synthetic summary.", "2026-08-01T09:10:00Z", "Newest synthetic memory.", "2026-08-01T08:10:00Z"},
		{filepath.Join(t.TempDir(), "middle"), "Middle synthetic snapshot", "Middle synthetic summary.", "2026-08-01T09:05:00Z", "Middle synthetic memory.", "2026-08-01T08:05:00Z"},
	}
	for _, export := range directories {
		if err := os.MkdirAll(export.path, 0o700); err != nil {
			t.Fatal(err)
		}
		conversation, err := json.Marshal([]map[string]any{{
			"uuid": "synthetic-overlap", "name": export.name, "summary": export.summary,
			"created_at": "2026-08-01T09:00:00Z", "updated_at": export.updated,
			"chat_messages": []map[string]any{
				{"uuid": "synthetic-human", "text": "Name the synthetic marker.", "sender": "human", "created_at": "2026-08-01T09:00:01Z"},
				{"uuid": "synthetic-assistant", "text": "Glass Finch.", "sender": "assistant", "created_at": "2026-08-01T09:00:02Z", "parent_message_uuid": "synthetic-human"},
			},
		}})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(export.path, "conversations.json"), conversation, 0o600); err != nil {
			t.Fatal(err)
		}
		memory, err := json.Marshal([]map[string]any{{
			"uuid": "synthetic-overlap-memory", "memory": export.memory,
			"updated_at": export.memoryUpdated,
		}})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(export.path, "memories.json"), memory, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	db := rocaDatabase(t)
	roots := Roots{ClaudeWebExports: []string{
		directories[0].path, directories[1].path, directories[2].path,
	}}
	if _, err := Run(context.Background(), db, registry(t), Options{Roots: roots}); err != nil {
		t.Fatal(err)
	}
	if got := countRows(t, db.SQL(), "memories"); got != 1 {
		t.Fatalf("memories = %d, want 1", got)
	}
	var memory, memoryUpdated string
	if err := db.SQL().QueryRow(`SELECT content, json_extract(metadata, '$.updated_at') FROM memories`).
		Scan(&memory, &memoryUpdated); err != nil {
		t.Fatal(err)
	}
	if memory != directories[1].memory || memoryUpdated != directories[1].memoryUpdated {
		t.Fatalf("newest memory = content %q updated %q", memory, memoryUpdated)
	}
	var ended, name, summary, updated string
	var duration int
	err := db.SQL().QueryRow(`SELECT ended_at, duration_minutes,
		json_extract(metadata, '$.name'), json_extract(metadata, '$.summary'),
		json_extract(metadata, '$.updated_at') FROM sessions WHERE session_id = 'synthetic-overlap'`).
		Scan(&ended, &duration, &name, &summary, &updated)
	if err != nil {
		t.Fatal(err)
	}
	if ended != directories[1].updated || duration != 10 || name != directories[1].name ||
		summary != directories[1].summary || updated != directories[1].updated {
		t.Fatalf("newest snapshot = ended %q duration %d name %q summary %q updated %q",
			ended, duration, name, summary, updated)
	}
}

func copyClaudeWebFixture(t *testing.T) string {
	t.Helper()
	target := t.TempDir()
	copyClaudeWebFixtureInto(t, target)
	return target
}

func copyClaudeWebFixtureInto(t *testing.T, target string) {
	t.Helper()
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"conversations.json", "memories.json"} {
		raw, err := os.ReadFile(filepath.Join("testdata", "anthropic-export", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(target, name), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDeclaredClaudeWebExportIngestsProjectsDocsMemoriesAndDesignChats(t *testing.T) {
	db := rocaDatabase(t)
	root := filepath.Join("testdata", "anthropic-export-projects")
	result, err := Run(context.Background(), db, registry(t), Options{
		Roots: Roots{ClaudeWebExports: []string{root}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Delta.Sessions != 3 || result.Delta.Exchanges != 2 {
		t.Fatalf("project export sessions/exchanges = %+v, want 3 sessions and 2 exchanges", result.Delta)
	}
	if result.Delta.Memories != 9 {
		t.Fatalf("project export memories = %d, want 9", result.Delta.Memories)
	}

	var ordinary string
	if err := db.SQL().QueryRow(
		"SELECT COALESCE(project, '') FROM sessions WHERE session_id = ?",
		"10000000-0000-4000-8000-000000000099").Scan(&ordinary); err != nil {
		t.Fatal(err)
	}
	if ordinary != "" {
		t.Fatalf("ordinary conversation project = %q, want unset", ordinary)
	}

	var designProject string
	if err := db.SQL().QueryRow(
		"SELECT COALESCE(project, '') FROM sessions WHERE session_id = ?",
		"cccccccc-0000-4000-8000-000000000001").Scan(&designProject); err != nil {
		t.Fatal(err)
	}
	if designProject != "aaaaaaaa-0000-4000-8000-000000000001" {
		t.Fatalf("design chat project = %q", designProject)
	}

	counts := map[string]int{}
	rows, err := db.SQL().Query(`SELECT layer, COUNT(*) FROM memories
		WHERE source_agent = 'claude-web' GROUP BY layer`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var layer string
		var n int
		if err := rows.Scan(&layer, &n); err != nil {
			t.Fatal(err)
		}
		counts[layer] = n
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if counts["user"] != 5 || counts["project"] != 4 {
		t.Fatalf("memory layers = %v, want user=5 project=4", counts)
	}

	var projected int
	if err := db.SQL().QueryRow(`SELECT COUNT(*) FROM memories
		WHERE source_agent = 'claude-web' AND project = 'aaaaaaaa-0000-4000-8000-000000000001'`).
		Scan(&projected); err != nil {
		t.Fatal(err)
	}
	if projected != 4 {
		t.Fatalf("orchard-keyed memories = %d, want entity+2 docs+1 project memory", projected)
	}

	plan := Scan(Roots{ClaudeWebExports: []string{root}})
	if plan.Scanned["claude_web_export_files"] != 6 {
		t.Fatalf("scanned export files = %d, want 6", plan.Scanned["claude_web_export_files"])
	}
}

func TestClaudeWebExportIsNeverDiscoveredWithoutADeclaration(t *testing.T) {
	home := t.TempDir()
	copyClaudeWebFixtureInto(t, filepath.Join(home, "Downloads", "claude-export"))
	roots := ResolveRoots(Environment{GOOS: "darwin", Home: home}, Settings{})
	plan := Scan(roots)
	if plan.Scanned["claude_web_export_files"] != 0 {
		t.Fatalf("undeclared export scan = %+v", plan.Scanned)
	}
	for _, target := range plan.Targets {
		if target.SourceAgent == "claude-web" {
			t.Fatalf("undeclared export target = %+v", target)
		}
	}
}
