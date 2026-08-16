package parsers

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCursorDatabaseKeepsConversationStructureAndRecordedProvenance(t *testing.T) {
	content, err := os.ReadFile(filepath.Join("testdata", "conformance",
		"cursor-database", "state.vscdb"))
	if err != nil {
		t.Fatal(err)
	}
	registered, ok := Lookup("cursor_database")
	if !ok {
		t.Fatal("Cursor database parser is not registered")
	}
	records, err := registered.Parse(File{Content: content, Meta: FileMeta{
		Path: "/synthetic/Cursor/User/globalStorage/state.vscdb", SourceAgent: "cursor",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(records.Sessions) != 1 {
		t.Fatalf("sessions = %d, want one populated composer: %+v",
			len(records.Sessions), records.Sessions)
	}
	session := records.Sessions[0]
	if session.ID != "cursor:11111111-2222-3333-4444-555555555555" ||
		session.SourceAgent != "cursor" || session.Project != "synthetic-lighthouse" ||
		session.Title != "Synthetic lighthouse session" {
		t.Fatalf("session identity = %+v", session)
	}
	if session.EndedAt != "2026-08-01T13:01:04Z" {
		t.Fatalf("session end from final bubble = %q", session.EndedAt)
	}
	if len(session.Exchanges) != 2 {
		t.Fatalf("exchanges = %d, want two: %+v", len(session.Exchanges), session.Exchanges)
	}
	exchange := session.Exchanges[0]
	if exchange.Number != 1 ||
		exchange.SourceID != "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee" ||
		exchange.HumanText != "map the synthetic lighthouse" ||
		exchange.AgentText != "The synthetic lighthouse is mapped." {
		t.Fatalf("exchange = %+v", exchange)
	}
	if len(exchange.Thinking) != 1 ||
		exchange.Thinking[0].Text != "First inspect the invented beacon map." {
		t.Fatalf("thinking = %+v", exchange.Thinking)
	}
	if len(exchange.Tools) != 1 || exchange.Tools[0].Name != "read_file" ||
		exchange.Tools[0].HadError {
		t.Fatalf("tools = %+v", exchange.Tools)
	}
	if exchange.Provenance.Model != "fixture-cursor-model" ||
		exchange.Provenance.TokensIn == nil || *exchange.Provenance.TokensIn != 11 ||
		exchange.Provenance.TokensOut == nil || *exchange.Provenance.TokensOut != 9 {
		t.Fatalf("provenance = %+v", exchange.Provenance)
	}
	second := session.Exchanges[1]
	if second.Number != 2 || second.HumanText != "confirm the invented beacon color" ||
		second.AgentText != "The invented beacon is amber." {
		t.Fatalf("second exchange = %+v", second)
	}
	if second.Provenance.Model != "" || second.Provenance.TokensIn != nil ||
		second.Provenance.TokensOut != nil {
		t.Fatalf("unstated second-turn provenance = %+v", second.Provenance)
	}
	if len(records.Discards) != 4 {
		t.Fatalf("discards = %d, want the empty composer, orphan bubble, prompt and generation: %+v",
			len(records.Discards), records.Discards)
	}
	for _, discard := range records.Discards {
		if !discard.ByDesign {
			t.Fatalf("secondary Cursor state was reported as unreadable: %+v", discard)
		}
	}
}

func TestCursorDetectorRejectsSQLiteWithoutCursorState(t *testing.T) {
	registered, ok := Lookup("cursor_database")
	if !ok {
		t.Fatal("Cursor database parser is not registered")
	}
	if registered.Parser.Detect(File{Content: []byte("SQLite format 3\x00not a database")}) {
		t.Fatal("Cursor detector claimed an unrelated SQLite header")
	}
}
