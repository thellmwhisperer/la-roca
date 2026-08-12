package parsers

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestChatGPTWebExportWalksBranchesAndFillsProvenance(t *testing.T) {
	records := parseChatGPTWebFixture(t, "openai-export-v1")
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

func TestChatGPTWebDiscardedNodesDoNotPoisonDescendants(t *testing.T) {
	records := parseChatGPTWebFixture(t, "openai-export-discarded")
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

func parseChatGPTWebFixture(t *testing.T, directory string) Records {
	t.Helper()
	path := filepath.Join("..", "testdata", directory, "conversations.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	records, err := ParseChatGPTWebConversations(bytes.NewReader(raw), FileMeta{
		Path: path, FileName: "conversations.json", SourceAgent: "chatgpt-web",
	})
	if err != nil {
		t.Fatal(err)
	}
	return records
}
