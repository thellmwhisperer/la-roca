package parsers

import (
	"os"
	"path/filepath"
	"testing"
)

const cursorStoreSessionID = "cursor:11111111-aaaa-4bbb-8ccc-222222222222"

func TestCursorStoreKeepsConversationStructureAndSidecarProject(t *testing.T) {
	content, err := os.ReadFile(filepath.Join("testdata", "conformance",
		"cursor-store", "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	sidecar, err := os.ReadFile(filepath.Join("testdata", "conformance",
		"cursor-store", "meta.json"))
	if err != nil {
		t.Fatal(err)
	}
	registered, ok := Lookup("cursor_store")
	if !ok {
		t.Fatal("Cursor store parser is not registered")
	}
	records, err := registered.Parse(File{Content: content, Meta: FileMeta{
		Path: "/synthetic/home/.cursor/chats/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/" +
			"11111111-aaaa-4bbb-8ccc-222222222222/store.db",
		FileName:    "store.db",
		SourceAgent: "cursor",
		Sidecar:     sidecar,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(records.Sessions) != 1 {
		t.Fatalf("sessions = %d, want one store conversation: %+v",
			len(records.Sessions), records.Sessions)
	}
	session := records.Sessions[0]
	if session.ID != cursorStoreSessionID ||
		session.SourceAgent != "cursor" || session.Project != "harbor" ||
		session.Title != "Synthetic harbor session" {
		t.Fatalf("session identity = %+v", session)
	}
	if session.StartedAt != "2026-08-01T13:03:20Z" {
		t.Fatalf("session start from store createdAt = %q", session.StartedAt)
	}
	if len(session.Exchanges) != 2 {
		t.Fatalf("exchanges = %d, want two: %+v", len(session.Exchanges), session.Exchanges)
	}
	wantTurns := []struct {
		human, agent string
	}{
		{"map the synthetic harbor", "The synthetic harbor is mapped."},
		{"confirm the invented beacon color", "The invented beacon is amber."},
	}
	for i, want := range wantTurns {
		got := session.Exchanges[i]
		if got.Number != i+1 || got.HumanText != want.human || got.AgentText != want.agent {
			t.Fatalf("exchange %d = %+v, want %q / %q", i+1, got, want.human, want.agent)
		}
		if got.Provenance.Model != "fixture-cursor-store-model" {
			t.Fatalf("exchange %d model = %q", i+1, got.Provenance.Model)
		}
	}
	first := session.Exchanges[0]
	if len(first.Thinking) != 1 || first.Thinking[0].Text != "First inspect the invented pier." {
		t.Fatalf("thinking = %+v", first.Thinking)
	}
	if len(first.Tools) != 1 || first.Tools[0].Name != "Read" || first.Tools[0].HadError {
		t.Fatalf("tools = %+v", first.Tools)
	}
	if len(records.Discards) == 0 {
		t.Fatal("expected by-design discards for the system prompt and context wrapper")
	}
	for _, discard := range records.Discards {
		if !discard.ByDesign {
			t.Fatalf("Cursor store skip was reported as unreadable: %+v", discard)
		}
	}
}

func TestCursorStoreDetectorRejectsTheLegacyIDEDatabase(t *testing.T) {
	content, err := os.ReadFile(filepath.Join("testdata", "conformance",
		"cursor-database", "state.vscdb"))
	if err != nil {
		t.Fatal(err)
	}
	registered, ok := Lookup("cursor_store")
	if !ok {
		t.Fatal("Cursor store parser is not registered")
	}
	if registered.Parser.Detect(File{Content: content, Meta: FileMeta{
		FileName: "state.vscdb", SourceAgent: "cursor",
	}}) {
		t.Fatal("store detector claimed the legacy IDE state database")
	}
	if registered.Parser.Detect(File{Content: []byte("SQLite format 3\x00not a database")}) {
		t.Fatal("store detector claimed an unrelated SQLite header")
	}
}

func TestCursorStoreSecondParseIsByteIdentical(t *testing.T) {
	content, err := os.ReadFile(filepath.Join("testdata", "conformance",
		"cursor-store", "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	registered, ok := Lookup("cursor_store")
	if !ok {
		t.Fatal("Cursor store parser is not registered")
	}
	file := File{Content: content, Meta: FileMeta{
		FileName: "store.db", SourceAgent: "cursor",
		Path: "/synthetic/home/.cursor/chats/hash/11111111-aaaa-4bbb-8ccc-222222222222/store.db",
	}}
	first, err := registered.Parse(file)
	if err != nil {
		t.Fatal(err)
	}
	second, err := registered.Parse(file)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Sessions) != 1 || len(second.Sessions) != 1 {
		t.Fatalf("parse counts first=%d second=%d", len(first.Sessions), len(second.Sessions))
	}
	if first.Sessions[0].ID != second.Sessions[0].ID ||
		len(first.Sessions[0].Exchanges) != len(second.Sessions[0].Exchanges) {
		t.Fatalf("second parse changed the session: first=%+v second=%+v",
			first.Sessions[0], second.Sessions[0])
	}
	for i := range first.Sessions[0].Exchanges {
		if first.Sessions[0].Exchanges[i].Fingerprint !=
			second.Sessions[0].Exchanges[i].Fingerprint {
			t.Fatalf("second parse changed exchange %d fingerprint", i+1)
		}
	}
}
