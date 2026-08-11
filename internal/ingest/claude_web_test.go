package ingest

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	if first.RecordsDiscarded != 2 {
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
