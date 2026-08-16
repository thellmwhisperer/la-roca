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
		if discard.Category != "human message has no text" {
			t.Errorf("discard %d has unstable category %q", i, discard.Category)
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

func TestClaudeWebMemoriesReadCurrentAccountSurfacesAndOlderShapes(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want []struct{ content, project, pathSuffix string }
	}{
		{
			name: "legacy string",
			raw:  `["Synthetic legacy string memory."]`,
			want: []struct{ content, project, pathSuffix string }{
				{content: "Synthetic legacy string memory.", pathSuffix: "#memory=entry-1"},
			},
		},
		{
			name: "legacy object",
			raw:  `[{"uuid":"synthetic-memory-1","memory":"Synthetic preference.","created_at":"2025-04-02T07:00:00.000Z"}]`,
			want: []struct{ content, project, pathSuffix string }{
				{content: "Synthetic preference.", pathSuffix: "memory-uuid:synthetic-memory-1"},
			},
		},
		{
			name: "current account",
			raw: `[
				{
					"account_uuid":"aaaaaaaa-0000-4000-8000-000000000099",
					"conversations_memory":"Synthetic account conversation memory.",
					"project_memories":{
						"aaaaaaaa-0000-4000-8000-000000000001":"Synthetic orchard project memory.",
						"aaaaaaaa-0000-4000-8000-000000000002":"Synthetic atlas project memory."
					},
					"memory_files":[
						{"content":"Synthetic memory file.","path":"memories/synthetic-atlas.md","updated_at":"2026-08-01T16:31:00Z"}
					]
				}
			]`,
			want: []struct{ content, project, pathSuffix string }{
				{content: "Synthetic account conversation memory.", pathSuffix: "memory-account:aaaaaaaa-0000-4000-8000-000000000099"},
				{content: "Synthetic orchard project memory.", project: "aaaaaaaa-0000-4000-8000-000000000001", pathSuffix: "memory-project:aaaaaaaa-0000-4000-8000-000000000001"},
				{content: "Synthetic atlas project memory.", project: "aaaaaaaa-0000-4000-8000-000000000002", pathSuffix: "memory-project:aaaaaaaa-0000-4000-8000-000000000002"},
				{content: "Synthetic memory file.", pathSuffix: "memory-file:memories/synthetic-atlas.md"},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			records, err := ParseClaudeWebMemories(bytes.NewReader([]byte(test.raw)), FileMeta{
				Path: "/declared/export/memories.json", FileName: "memories.json",
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(records.Memories) != len(test.want) {
				t.Fatalf("memories = %+v, want %d", records.Memories, len(test.want))
			}
			for i, want := range test.want {
				got := records.Memories[i]
				if got.Content != want.content || got.Project != want.project ||
					got.Layer != "user" || !strings.Contains(got.FilePath, want.pathSuffix) {
					t.Errorf("memory %d = content %q project %q path %q, want content %q project %q path containing %q",
						i, got.Content, got.Project, got.FilePath, want.content, want.project, want.pathSuffix)
				}
			}
			if len(records.Discards) != 0 {
				t.Fatalf("discards = %+v", records.Discards)
			}
		})
	}
}

func TestClaudeWebMemoriesReportUnreadableAccountSurfaces(t *testing.T) {
	tests := []struct {
		name           string
		raw            string
		wantMemories   int
		wantCategories []string
	}{
		{
			name: "scalar project_memories keeps memory_files",
			raw: `[{"account_uuid":"aaaaaaaa-0000-4000-8000-000000000099",
				"project_memories":"not-an-object",
				"memory_files":[{"content":"Synthetic memory file.","path":"memories/synthetic-atlas.md"}]}]`,
			wantMemories:   1,
			wantCategories: []string{"claude-web project_memories is unreadable"},
		},
		{
			name: "malformed memory_files entry reports the surface",
			raw: `[{"account_uuid":"aaaaaaaa-0000-4000-8000-000000000099",
				"project_memories":{"aaaaaaaa-0000-4000-8000-000000000001":"Synthetic orchard project memory."},
				"memory_files":["not-an-object"]}]`,
			wantMemories:   1,
			wantCategories: []string{"claude-web memory_files is unreadable"},
		},
		{
			name: "both unreadable surfaces report each without a no-text fallback",
			raw: `[{"account_uuid":"aaaaaaaa-0000-4000-8000-000000000099",
				"project_memories":42,
				"memory_files":{"content":"not-an-array"}}]`,
			wantMemories: 0,
			wantCategories: []string{
				"claude-web project_memories is unreadable",
				"claude-web memory_files is unreadable",
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			records, err := ParseClaudeWebMemories(bytes.NewReader([]byte(test.raw)), FileMeta{
				Path: "/declared/export/memories.json", FileName: "memories.json",
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(records.Memories) != test.wantMemories {
				t.Fatalf("memories = %+v, want %d", records.Memories, test.wantMemories)
			}
			if len(records.Discards) != len(test.wantCategories) {
				t.Fatalf("discards = %+v, want %d", records.Discards, len(test.wantCategories))
			}
			for i, category := range test.wantCategories {
				if got := records.Discards[i]; got.Category != category || got.ByDesign ||
					!strings.Contains(got.Reason, "is unreadable") {
					t.Errorf("discard %d = %+v, want category %q and an unreadable reason", i, got, category)
				}
			}
		})
	}
}

func TestClaudeWebProjectEntitiesAndDocsBecomeStoreRows(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "testdata", "anthropic-export-projects",
		"projects", "aaaaaaaa-0000-4000-8000-000000000001.json"))
	if err != nil {
		t.Fatal(err)
	}
	records, err := ParseClaudeWebProject(raw, FileMeta{
		Path:     "/declared/export/projects/aaaaaaaa-0000-4000-8000-000000000001.json",
		FileName: "aaaaaaaa-0000-4000-8000-000000000001.json", SourceAgent: "claude-web",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(records.Sessions) != 0 {
		t.Fatalf("project file produced sessions: %+v", records.Sessions)
	}
	if len(records.Memories) != 3 {
		t.Fatalf("memories = %+v, want entity plus two docs", records.Memories)
	}
	entity := records.Memories[0]
	if entity.Layer != "project" || entity.Project != "aaaaaaaa-0000-4000-8000-000000000001" ||
		entity.Content != "A fictional indoor orchard sensor programme." ||
		entity.Metadata["name"] != "Synthetic orchard" ||
		entity.Metadata["prompt_template"] != "Answer only about the invented orchard." {
		t.Fatalf("project entity = %+v", entity)
	}
	if records.Memories[1].Content != "The invented sensor is called Amber Finch." ||
		records.Memories[1].Project != entity.Project || records.Memories[1].Layer != "project" {
		t.Fatalf("first doc = %+v", records.Memories[1])
	}
	if records.Memories[2].Metadata["filename"] != "synthetic-wiring.md" {
		t.Fatalf("second doc metadata = %+v", records.Memories[2].Metadata)
	}
}

func TestClaudeWebNamelessProjectUsesNameAsContentAndKeepsUnresolvedHonesty(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "testdata", "anthropic-export-projects",
		"projects", "aaaaaaaa-0000-4000-8000-000000000002.json"))
	if err != nil {
		t.Fatal(err)
	}
	records, err := ParseClaudeWebProject(raw, FileMeta{SourceAgent: "claude-web"})
	if err != nil {
		t.Fatal(err)
	}
	if len(records.Memories) != 1 || records.Memories[0].Content != "Synthetic atlas" {
		t.Fatalf("nameless description fallback = %+v", records.Memories)
	}
}

func TestClaudeWebDesignChatKeepsProjectUUIDAndOrdinaryConversationsStayUnprojected(t *testing.T) {
	chat, err := os.ReadFile(filepath.Join("..", "testdata", "anthropic-export-projects",
		"design_chats", "cccccccc-0000-4000-8000-000000000001.json"))
	if err != nil {
		t.Fatal(err)
	}
	records, err := ParseClaudeWebDesignChat(chat, FileMeta{SourceAgent: "claude-web"})
	if err != nil {
		t.Fatal(err)
	}
	if len(records.Sessions) != 1 {
		t.Fatalf("sessions = %d", len(records.Sessions))
	}
	session := records.Sessions[0]
	if session.Project != "aaaaaaaa-0000-4000-8000-000000000001" {
		t.Fatalf("design chat project = %q, want the uuid not the label", session.Project)
	}
	if session.Title != "Synthetic orchard canvas" || len(session.Exchanges) != 1 ||
		session.Exchanges[0].HumanText != "Sketch the invented sensor." ||
		session.Exchanges[0].AgentText != "A cobalt ring around an amber lens." {
		t.Fatalf("design chat session = %+v", session)
	}
	if session.Metadata["project_name"] != "Synthetic orchard" {
		t.Fatalf("design chat did not keep the source project name: %+v", session.Metadata)
	}

	ordinary := parseClaudeWebFixture(t, "conversations.json")
	for _, session := range ordinary.Sessions {
		if session.Project != "" {
			t.Fatalf("ordinary conversation %s was assigned project %q", session.ID, session.Project)
		}
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
