package parsers

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestChatGPTWebExportWalksBranchesAndFillsProvenance(t *testing.T) {
	records := parseChatGPTWebFixture(t, filepath.Join("openai-export-v1", "conversations.json"))
	if len(records.Sessions) != 1 {
		t.Fatalf("sessions = %d, want 1", len(records.Sessions))
	}
	session := records.Sessions[0]
	if session.ID != "40000000-0000-4000-8000-000000000001" ||
		session.SourceAgent != "chatgpt-web" || session.Title != "Synthetic lighthouse planning" {
		t.Fatalf("session = %+v", session)
	}
	if session.StartedAt != "2026-01-01T00:00:00.125Z" ||
		session.EndedAt != "2026-01-01T00:00:04.875Z" {
		t.Fatalf("session timestamps = %q to %q", session.StartedAt, session.EndedAt)
	}
	if len(session.Exchanges) != 2 {
		t.Fatalf("exchanges = %+v, want both assistant branches", session.Exchanges)
	}
	wantIDs := []string{
		"42000000-0000-4000-8000-000000000001",
		"42000000-0000-4000-8000-000000000002",
	}
	for index, want := range wantIDs {
		if session.Exchanges[index].SourceID != want {
			t.Errorf("exchange %d source id = %q, want %q", index, session.Exchanges[index].SourceID, want)
		}
	}
	first, alternate := session.Exchanges[0], session.Exchanges[1]
	if first.HumanTimestamp != "2026-01-01T00:00:01.125Z" ||
		first.AgentTimestamp != "2026-01-01T00:00:02.625Z" {
		t.Errorf("first timestamps = %q and %q", first.HumanTimestamp, first.AgentTimestamp)
	}
	if first.Provenance.Model != "gpt-synthetic-message" || first.Provenance.Provider != "openai" {
		t.Errorf("message provenance = %+v", first.Provenance)
	}
	if alternate.Provenance.Model != "gpt-synthetic-default" || alternate.Provenance.Provider != "openai" {
		t.Errorf("fallback provenance = %+v", alternate.Provenance)
	}
	if first.Provenance.TokensIn != nil || first.Provenance.TokensOut != nil ||
		first.Provenance.TokensReasoning != nil || first.Provenance.CostUSD != nil {
		t.Errorf("unstated usage was invented: %+v", first.Provenance)
	}
	if len(records.Discards) != 3 {
		t.Fatalf("exclusions = %+v, want root, system, and tool nodes", records.Discards)
	}
	for _, discard := range records.Discards {
		if !discard.ByDesign {
			t.Errorf("quiet exclusion reported as unreadable: %+v", discard)
		}
	}
}

// The additively read per-answer fields get no column of their own, so what they
// are for is pinned here: the legacy snapshot states more about the same two
// answers than a shard of them does, and a measured nothing is stated as zero.
func TestChatGPTWebSignalCountsWhatEachShapeStated(t *testing.T) {
	for fixture, want := range map[string][]int{
		filepath.Join("openai-export-v1", "conversations.json"):          {7, 6},
		filepath.Join("openai-export-sharded", "conversations-000.json"): {1, 0},
		filepath.Join("openai-export-sharded", "conversations-001.json"): {0},
	} {
		exchanges := parseChatGPTWebFixture(t, fixture).Sessions[0].Exchanges
		if len(exchanges) != len(want) {
			t.Fatalf("%s exchanges = %d, want %d", fixture, len(exchanges), len(want))
		}
		for index, exchange := range exchanges {
			if exchange.Signal == nil {
				t.Fatalf("%s exchange %d measured no signal at all", fixture, index)
			}
			if *exchange.Signal != want[index] {
				t.Errorf("%s exchange %d signal = %d, want %d", fixture, index,
					*exchange.Signal, want[index])
			}
		}
	}
}

func TestChatGPTWebDiscardedNodesDoNotPoisonDescendants(t *testing.T) {
	records := parseChatGPTWebFixture(t, filepath.Join("openai-export-discarded", "conversations.json"))
	session := records.Sessions[0]
	wantIDs := []string{"visible-after-empty", "visible-after-hidden", "visible-after-bad-role"}
	gotIDs := make([]string, 0, len(session.Exchanges))
	for _, exchange := range session.Exchanges {
		gotIDs = append(gotIDs, exchange.SourceID)
	}
	if !reflect.DeepEqual(gotIDs, wantIDs) {
		t.Fatalf("recovered exchange ids = %v, want %v", gotIDs, wantIDs)
	}
	if len(records.Discards) != 4 {
		t.Fatalf("discards = %+v, want root, empty, hidden, and bad-role nodes", records.Discards)
	}
	var excluded, unreadable int
	for _, discard := range records.Discards {
		if discard.ByDesign {
			excluded++
		} else {
			unreadable++
			if discard.Reason != `message bad-role has unsupported author role "critic"` {
				t.Errorf("unreadable reason = %q", discard.Reason)
			}
		}
	}
	if excluded != 3 || unreadable != 1 {
		t.Fatalf("excluded/unreadable = %d/%d, want 3/1", excluded, unreadable)
	}
}

func TestChatGPTWebMalformedMessageDoesNotPoisonConversation(t *testing.T) {
	raw := []byte(`[{
		"conversation_id":"synthetic-malformed-message",
		"mapping":{
			"root":{"id":"root","parent":null,"children":["user"],"message":null},
			"user":{"id":"user","parent":"root","children":["broken","sibling"],"message":{"author":{"role":"user"},"content":{"parts":["Synthetic prompt."]}}},
			"broken":{"id":"broken","parent":"user","children":["descendant"],"message":"corrupt"},
			"descendant":{"id":"descendant","parent":"broken","children":[],"message":{"author":{"role":"assistant"},"content":{"parts":["Recovered descendant."]}}},
			"sibling":{"id":"sibling","parent":"user","children":[],"message":{"author":{"role":"assistant"},"content":{"parts":["Readable sibling."]}}}
		}
	}]`)
	records, err := ParseChatGPTWebConversations(bytes.NewReader(raw), FileMeta{})
	if err != nil {
		t.Fatal(err)
	}
	wantIDs := []string{"descendant", "sibling"}
	gotIDs := make([]string, 0, len(records.Sessions[0].Exchanges))
	for _, exchange := range records.Sessions[0].Exchanges {
		gotIDs = append(gotIDs, exchange.SourceID)
	}
	if !reflect.DeepEqual(gotIDs, wantIDs) {
		t.Fatalf("recovered exchange ids = %v, want %v", gotIDs, wantIDs)
	}
	if len(records.Discards) != 2 {
		t.Fatalf("discards = %+v, want root and malformed message", records.Discards)
	}
	malformed := records.Discards[1]
	if malformed.Category != "ChatGPT message is unreadable" || malformed.ByDesign ||
		!strings.Contains(malformed.Reason, "message broken is unreadable") ||
		!strings.Contains(malformed.Reason, "cannot unmarshal string") {
		t.Fatalf("malformed message discard = %+v", malformed)
	}
}

func TestChatGPTWebMalformedEnvelopePreservesValidParent(t *testing.T) {
	raw := []byte(`[{
		"conversation_id":"synthetic-malformed-envelope",
		"mapping":{
			"root":{"id":"root","parent":null,"children":["user"],"message":null},
			"user":{"id":"user","parent":"root","children":["broken"],"message":{"author":{"role":"user"},"content":{"parts":["Synthetic prompt."]}}},
			"broken":{"id":"broken","parent":"user","children":"corrupt","message":{"author":{"role":"assistant"},"content":{"parts":["Unreadable envelope."]}}},
			"descendant":{"id":"descendant","parent":"broken","children":[],"message":{"author":{"role":"assistant"},"content":{"parts":["Recovered descendant."]}}}
		}
	}]`)
	records, err := ParseChatGPTWebConversations(bytes.NewReader(raw), FileMeta{})
	if err != nil {
		t.Fatal(err)
	}
	exchanges := records.Sessions[0].Exchanges
	if len(exchanges) != 1 || exchanges[0].SourceID != "descendant" ||
		exchanges[0].HumanText != "Synthetic prompt." {
		t.Fatalf("recovered exchanges = %+v", exchanges)
	}
	if len(records.Discards) != 2 {
		t.Fatalf("discards = %+v, want root and malformed envelope", records.Discards)
	}
	malformed := records.Discards[1]
	if malformed.Category != "ChatGPT conversation node has unreadable children" ||
		malformed.ByDesign || !strings.Contains(malformed.Reason, "node broken has unreadable children") {
		t.Fatalf("malformed envelope discard = %+v", malformed)
	}
}

func TestChatGPTWebMalformedConversationDoesNotPoisonExport(t *testing.T) {
	raw := []byte(`[
		{"conversation_id":"synthetic-readable-before","mapping":{}},
		{"conversation_id":"synthetic-malformed-conversation","update_time":"corrupt","mapping":{}},
		{"conversation_id":"synthetic-readable-after","mapping":{}}
	]`)
	records, err := ParseChatGPTWebConversations(bytes.NewReader(raw), FileMeta{})
	if err != nil {
		t.Fatal(err)
	}
	wantIDs := []string{"synthetic-readable-before", "synthetic-readable-after"}
	gotIDs := make([]string, 0, len(records.Sessions))
	for _, session := range records.Sessions {
		gotIDs = append(gotIDs, session.ID)
	}
	if !reflect.DeepEqual(gotIDs, wantIDs) {
		t.Fatalf("surviving conversation ids = %v, want %v", gotIDs, wantIDs)
	}
	if len(records.Discards) != 1 {
		t.Fatalf("discards = %+v, want malformed conversation only", records.Discards)
	}
	discard := records.Discards[0]
	if discard.Record != 1 || discard.Category != "ChatGPT conversation is unreadable" ||
		discard.ByDesign || !strings.Contains(discard.Reason, "conversation 2 is unreadable") ||
		!strings.Contains(discard.Reason, "cannot unmarshal string") {
		t.Fatalf("malformed conversation discard = %+v", discard)
	}
}

func TestChatGPTWebSnorlaxBecomesVirtualProjectAndCustomGPTDoesNot(t *testing.T) {
	tests := []struct {
		name, gizmoType, gizmoID, templateID, memoryScope, wantProject string
	}{
		{
			name: "snorlax project", gizmoType: "snorlax",
			gizmoID:     "g-p-syntheticorchard000000000000",
			templateID:  "g-p-syntheticorchard000000000000",
			memoryScope: "project_enabled",
			wantProject: "g-p-syntheticorchard000000000000",
		},
		{
			name: "custom gpt", gizmoType: "gpt",
			gizmoID:    "g-syntheticcustomgpt000000000000",
			templateID: "g-syntheticcustomgpt000000000000",
		},
		{name: "no gizmo"},
		{name: "snorlax without id", gizmoType: "snorlax", memoryScope: "project"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw := []byte(fmt.Sprintf(`[{
				"conversation_id":"synthetic-%s",
				"title":"Synthetic gizmo case",
				"gizmo_type":%q,
				"gizmo_id":%q,
				"conversation_template_id":%q,
				"memory_scope":%q,
				"mapping":{
					"root":{"id":"root","parent":null,"children":["user"],"message":null},
					"user":{"id":"user","parent":"root","children":["agent"],"message":{"author":{"role":"user"},"content":{"parts":["Synthetic prompt."]}}},
					"agent":{"id":"agent","parent":"user","children":[],"message":{"author":{"role":"assistant"},"content":{"parts":["Synthetic answer."]}}}
				}
			}]`, strings.ReplaceAll(test.name, " ", "-"),
				test.gizmoType, test.gizmoID, test.templateID, test.memoryScope))
			records, err := ParseChatGPTWebConversations(bytes.NewReader(raw), FileMeta{})
			if err != nil {
				t.Fatal(err)
			}
			if len(records.Sessions) != 1 {
				t.Fatalf("sessions = %d, want 1", len(records.Sessions))
			}
			session := records.Sessions[0]
			if session.Project != test.wantProject {
				t.Fatalf("project = %q, want %q", session.Project, test.wantProject)
			}
			if test.gizmoType != "" && session.Metadata["gizmo_type"] != test.gizmoType {
				t.Errorf("gizmo_type metadata = %v", session.Metadata["gizmo_type"])
			}
			if test.gizmoID != "" && session.Metadata["gizmo_id"] != test.gizmoID {
				t.Errorf("gizmo_id metadata = %v", session.Metadata["gizmo_id"])
			}
			if test.templateID != "" && session.Metadata["conversation_template_id"] != test.templateID {
				t.Errorf("conversation_template_id metadata = %v", session.Metadata["conversation_template_id"])
			}
			if test.memoryScope != "" && session.Metadata["memory_scope"] != test.memoryScope {
				t.Errorf("memory_scope metadata = %v", session.Metadata["memory_scope"])
			}
			if strings.Contains(fmt.Sprint(session.Metadata), "salud") ||
				strings.Contains(fmt.Sprint(session.Metadata), "trabajo") {
				t.Fatal("parser invented a display name")
			}
		})
	}
}

func parseChatGPTWebFixture(t *testing.T, fixture string) Records {
	t.Helper()
	path := filepath.Join("..", "testdata", fixture)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	records, err := ParseChatGPTWebConversations(bytes.NewReader(raw), FileMeta{
		Path: path, FileName: filepath.Base(path), SourceAgent: "chatgpt-web",
	})
	if err != nil {
		t.Fatal(err)
	}
	return records
}
