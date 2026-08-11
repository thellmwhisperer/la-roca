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

func TestClaudeWebExportDoesNotDiscardUnpairedReadableMessages(t *testing.T) {
	records := parseClaudeWebFixture(t, "conversations.json")
	if len(records.Discards) != 0 {
		t.Fatalf("discards = %+v, want readable thread roots and leaves retained", records.Discards)
	}
}

func TestClaudeWebDiscardedMessagesDoNotPoisonDescendants(t *testing.T) {
	records := parseClaudeWebFixture(t, "discarded-ancestors.json")
	if len(records.Sessions) != 2 {
		t.Fatalf("sessions = %d, want 2", len(records.Sessions))
	}
	var sourceIDs []string
	for _, session := range records.Sessions {
		for _, exchange := range session.Exchanges {
			sourceIDs = append(sourceIDs, exchange.SourceID)
		}
	}
	wantIDs := []string{
		"root-recovered-assistant",
		"mid-existing-assistant",
		"mid-recovered-assistant",
	}
	if !reflect.DeepEqual(sourceIDs, wantIDs) {
		t.Fatalf("exchange source ids = %v, want %v", sourceIDs, wantIDs)
	}
	if len(records.Discards) != 2 {
		t.Fatalf("discards = %+v, want only the two unreadable messages", records.Discards)
	}
	for i, discard := range records.Discards {
		if discard.Record != []int{1, 7}[i] || !strings.Contains(discard.Reason, "has no text") {
			t.Errorf("discard %d = %+v, want its own unreadable-message reason", i, discard)
		}
		if strings.Contains(discard.Reason, "parent chain") {
			t.Errorf("discard %d retained the cascading reason: %+v", i, discard)
		}
	}
}

func TestClaudeWebUnreadableMessagesKeepTheirOwnReasons(t *testing.T) {
	payload := claudeWebConversation{UUID: "synthetic-reasons", ChatMessages: []claudeWebMessage{
		{Text: "Readable but unidentified.", Sender: "human"},
		{UUID: "unsupported", Text: "Synthetic unsupported record.", Sender: "system"},
		{UUID: "empty", Sender: "human"},
		{UUID: "duplicate", Text: "Original identity.", Sender: "human"},
		{UUID: "duplicate", Text: "Duplicate identity.", Sender: "assistant"},
	}}
	records := parseClaudeWebConversation(payload, 0)
	want := []struct {
		record int
		reason string
	}{
		{1, "message has no uuid"},
		{2, `message unsupported has unsupported sender "system"`},
		{3, "human message empty has no text"},
		{5, "message uuid duplicate is duplicated"},
	}
	if len(records.Discards) != len(want) {
		t.Fatalf("discards = %+v, want %d precise reasons", records.Discards, len(want))
	}
	for i, expected := range want {
		if got := records.Discards[i]; got.Record != expected.record || got.Reason != expected.reason {
			t.Errorf("discard %d = %+v, want record %d reason %q", i, got, expected.record, expected.reason)
		}
	}
}

func TestClaudeWebCyclesDiscardEveryMember(t *testing.T) {
	tests := []struct {
		name        string
		messages    []claudeWebMessage
		wantReasons []string
		wantSources []string
	}{
		{
			name: "two members",
			messages: []claudeWebMessage{
				{UUID: "cycle-human", Text: "Synthetic cycle question.", Sender: "human", ParentMessageUUID: "cycle-assistant"},
				{UUID: "cycle-assistant", Text: "Synthetic cycle answer.", Sender: "assistant", ParentMessageUUID: "cycle-human"},
			},
			wantReasons: []string{
				"message cycle-human has a cyclic parent chain",
				"message cycle-assistant has a cyclic parent chain",
			},
			wantSources: []string{},
		},
		{
			name: "three members with surviving descendant",
			messages: []claudeWebMessage{
				{UUID: "cycle-a", Text: "Synthetic cycle A.", Sender: "human", ParentMessageUUID: "cycle-b"},
				{UUID: "cycle-b", Text: "Synthetic cycle B.", Sender: "assistant", ParentMessageUUID: "cycle-c"},
				{UUID: "cycle-c", Text: "Synthetic cycle C.", Sender: "human", ParentMessageUUID: "cycle-a"},
				{UUID: "descendant-human", Text: "Readable descendant question.", Sender: "human", ParentMessageUUID: "cycle-b"},
				{UUID: "descendant-assistant", Text: "Readable descendant answer.", Sender: "assistant", ParentMessageUUID: "descendant-human"},
			},
			wantReasons: []string{
				"message cycle-a has a cyclic parent chain",
				"message cycle-b has a cyclic parent chain",
				"message cycle-c has a cyclic parent chain",
			},
			wantSources: []string{"descendant-assistant"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			records := parseClaudeWebConversation(claudeWebConversation{
				UUID: "synthetic-cycle", ChatMessages: test.messages,
			}, 0)
			gotReasons := make([]string, 0, len(records.Discards))
			for _, discard := range records.Discards {
				gotReasons = append(gotReasons, discard.Reason)
			}
			if !reflect.DeepEqual(gotReasons, test.wantReasons) {
				t.Errorf("discard reasons = %v, want %v", gotReasons, test.wantReasons)
			}
			gotSources := make([]string, 0, len(records.Sessions[0].Exchanges))
			for _, exchange := range records.Sessions[0].Exchanges {
				gotSources = append(gotSources, exchange.SourceID)
			}
			if !reflect.DeepEqual(gotSources, test.wantSources) {
				t.Errorf("exchange source ids = %v, want %v", gotSources, test.wantSources)
			}
		})
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

func TestClaudeWebMemoryIdentityUsesUUIDOrScopedPosition(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		same bool
	}{
		{"uuid", `{"uuid":"synthetic-memory-1","memory":"Synthetic preference."}`, true},
		{"position", `"Synthetic preference."`, false},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			first, reason := parseClaudeWebMemory(json.RawMessage(test.raw), FileMeta{Path: "/export-one/memories.json"}, 1)
			if reason != "" {
				t.Fatal(reason)
			}
			second, reason := parseClaudeWebMemory(json.RawMessage(test.raw), FileMeta{Path: "/export-two/memories.json"}, 1)
			if reason != "" {
				t.Fatal(reason)
			}
			if got := first.FilePath == second.FilePath; got != test.same {
				t.Fatalf("same identity = %t, want %t (%q, %q)", got, test.same, first.FilePath, second.FilePath)
			}
		})
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
	if name == "memories.json" {
		records, err = ParseClaudeWebMemories(bytes.NewReader(raw), meta)
	} else {
		records, err = ParseClaudeWebConversations(bytes.NewReader(raw), meta)
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
