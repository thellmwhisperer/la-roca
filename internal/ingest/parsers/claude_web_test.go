package parsers

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func toJSON(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestClaudeWebExportPairsEveryHonestBranch(t *testing.T) {
	records := parseClaudeWebFixture(t, "conversations.json")
	if len(records.Sessions) != 2 {
		t.Fatalf("sessions = %d, want 2", len(records.Sessions))
	}
	session := records.Sessions[0]
	if session.ID != "10000000-0000-4000-8000-000000000001" || session.Project != "" {
		t.Fatalf("session identity = %+v", session)
	}
	if session.Title != "Synthetic orchard planning" ||
		session.Metadata["name"] != session.Title ||
		session.Metadata["summary"] != "A fictional plan for an indoor orchard sensor." {
		t.Fatalf("session metadata = %+v", session.Metadata)
	}
	if len(session.Exchanges) != 3 {
		t.Fatalf("exchanges = %+v, want the main branch, alternate branch, and follow-up", session.Exchanges)
	}
	wantIDs := []string{
		"20000000-0000-4000-8000-000000000002",
		"20000000-0000-4000-8000-000000000003",
		"20000000-0000-4000-8000-000000000005",
	}
	for i, want := range wantIDs {
		if session.Exchanges[i].SourceID != want {
			t.Errorf("exchange %d source id = %q, want %q", i, session.Exchanges[i].SourceID, want)
		}
	}
	if got := session.Exchanges[1].HumanText; got != "Choose a synthetic sensor label." {
		t.Errorf("alternate branch human text = %q", got)
	}
	if got := session.Exchanges[1].AgentText; got != "The alternate branch calls it Copper Wren." {
		t.Errorf("alternate branch agent text = %q", got)
	}
	web, _ := session.Metadata["claude_web"].(map[string]any)
	metadata, _ := web["exchange_metadata"].(map[string]any)
	if !strings.Contains(toJSON(t, metadata[wantIDs[0]]), "invented-sensors.txt") ||
		!strings.Contains(toJSON(t, metadata[wantIDs[1]]), "fictional-labels.csv") {
		t.Errorf("exchange metadata does not preserve attachment names: %+v", metadata)
	}
}

func TestClaudeWebExportReportsDanglingAndOrphanedMessages(t *testing.T) {
	records := parseClaudeWebFixture(t, "conversations.json")
	if len(records.Discards) != 2 {
		t.Fatalf("discards = %+v, want dangling and orphaned messages", records.Discards)
	}
	reasons := records.Discards[0].Reason + "\n" + records.Discards[1].Reason
	for _, want := range []string{"has no assistant reply", "parent message 29999999-9999-4999-8999-999999999999 was not found"} {
		if !strings.Contains(reasons, want) {
			t.Errorf("discard reasons = %q, missing %q", reasons, want)
		}
	}
}

func TestClaudeWebExportParserIsDeterministic(t *testing.T) {
	raw := readClaudeWebFixture(t, "conversations.json")
	first, err := ParseClaudeWebConversations(bytes.NewReader(raw), FileMeta{Path: "/declared/export/conversations.json"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := ParseClaudeWebConversations(bytes.NewReader(raw), FileMeta{Path: "/declared/export/conversations.json"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatal("same export produced different normalized records")
	}
}

func TestClaudeWebMemoriesLandInTheUserLayer(t *testing.T) {
	records := parseClaudeWebFixture(t, "memories.json")
	if len(records.Memories) != 1 {
		t.Fatalf("memories = %+v", records.Memories)
	}
	memory := records.Memories[0]
	if memory.Layer != "user" || memory.Origin != "cron" || memory.SourceAgent != "claude-web" {
		t.Fatalf("memory attribution = %+v", memory)
	}
	if memory.Content != "The synthetic operator prefers explicit export paths." ||
		memory.CreatedAt != "2025-04-02T07:00:00.000Z" {
		t.Fatalf("memory = %+v", memory)
	}
}

func parseClaudeWebFixture(t *testing.T, name string) Records {
	t.Helper()
	raw := readClaudeWebFixture(t, name)
	meta := FileMeta{Path: filepath.Join("/declared/export", name), FileName: name}
	var (
		records Records
		err     error
	)
	if name == "conversations.json" {
		records, err = ParseClaudeWebConversations(bytes.NewReader(raw), meta)
	} else {
		records, err = ParseClaudeWebMemories(bytes.NewReader(raw), meta)
	}
	if err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	return records
}

func readClaudeWebFixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "testdata", "anthropic-export", name))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return raw
}
