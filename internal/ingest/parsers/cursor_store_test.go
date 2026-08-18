package parsers

import (
	"bytes"
	"database/sql"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
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
		if got.Provenance.Model != "" {
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

func TestCursorStoreReportsMalformedMetadataWithoutLosingConversation(t *testing.T) {
	fixture, err := os.ReadFile(filepath.Join("testdata", "conformance",
		"cursor-store", "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name, value, category string
	}{
		{"hex", "not-hex", "invalid Cursor store metadata hex"},
		{"json", hex.EncodeToString([]byte("{")), "invalid Cursor store metadata JSON"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "store.db")
			if err := os.WriteFile(path, fixture, 0o600); err != nil {
				t.Fatal(err)
			}
			db, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`UPDATE meta SET value = ? WHERE key = '0'`, test.value); err != nil {
				db.Close()
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			content, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			records, err := cursorStoreParser{}.Parse(File{Content: content, Meta: FileMeta{
				FileName: "store.db",
				Path:     "/synthetic/home/.cursor/chats/hash/11111111-aaaa-4bbb-8ccc-222222222222/store.db",
			}})
			if err != nil {
				t.Fatal(err)
			}
			if len(records.Sessions) != 1 || len(records.Sessions[0].Exchanges) != 2 {
				t.Fatalf("malformed metadata lost conversation: %+v", records.Sessions)
			}
			assertCursorStoreDiscard(t, records.Discards, test.category)
		})
	}
}

func TestCursorStoreReportsMalformedSidecarWithoutLosingConversation(t *testing.T) {
	content, err := os.ReadFile(filepath.Join("testdata", "conformance",
		"cursor-store", "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	records, err := cursorStoreParser{}.Parse(File{Content: content, Meta: FileMeta{
		FileName: "store.db", Sidecar: []byte("{"),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(records.Sessions) != 1 || len(records.Sessions[0].Exchanges) != 2 {
		t.Fatalf("malformed sidecar lost conversation: %+v", records.Sessions)
	}
	assertCursorStoreDiscard(t, records.Discards, "invalid Cursor sidecar JSON")
}

func TestCursorStoreReportsUnreadableMerkleChildren(t *testing.T) {
	validHash := bytes.Repeat([]byte{0x11}, 32)
	missingHash := bytes.Repeat([]byte{0x22}, 32)
	malformedHash := bytes.Repeat([]byte{0x33}, 32)
	listData := make([]byte, 0, 102)
	for _, hash := range [][]byte{validHash, missingHash, malformedHash} {
		listData = append(listData, 0x0a, 0x20)
		listData = append(listData, hash...)
	}
	validID := hex.EncodeToString(validHash)
	malformedID := hex.EncodeToString(malformedHash)
	items, discards := cursorStoreOrderedMessages(map[string][]byte{
		"list":      listData,
		validID:     []byte(`{"role":"user","content":[{"type":"text","text":"hello"}]}`),
		malformedID: []byte("not JSON"),
	}, "")
	if len(items) != 1 {
		t.Fatalf("items = %d, want one valid message", len(items))
	}
	if len(discards) != 2 {
		t.Fatalf("discards = %d, want missing and malformed children: %+v", len(discards), discards)
	}
	assertCursorStoreDiscard(t, discards, "Cursor Merkle child blob is missing")
	assertCursorStoreDiscard(t, discards, "Cursor Merkle child blob is not a valid message")
}

func TestCursorStoreProtoRejectsLengthBeyondRemainingBytes(t *testing.T) {
	data := []byte{0x0a, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x01}
	if _, ok := cursorStoreProtoFields(data); ok {
		t.Fatal("protobuf parser accepted a length beyond the remaining bytes")
	}
}

func assertCursorStoreDiscard(t *testing.T, discards []Discard, category string) {
	t.Helper()
	for _, discard := range discards {
		if discard.Category == category {
			if discard.ByDesign {
				t.Fatalf("unreadable record marked by design: %+v", discard)
			}
			return
		}
	}
	var categories []string
	for _, discard := range discards {
		categories = append(categories, discard.Category)
	}
	t.Fatalf("missing discard category %q in %s", category, strings.Join(categories, ", "))
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
