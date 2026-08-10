package parsers

import "testing"

const subagentTranscript = `
{"type":"user","sessionId":"parent-1","agentId":"child-9","timestamp":"2026-08-01T12:00:00Z","message":{"content":[{"type":"text","text":"find the adapters"}]}}
{"type":"assistant","sessionId":"parent-1","agentId":"child-9","timestamp":"2026-08-01T12:00:02Z","message":{"content":[{"type":"text","text":"there are nine"}]}}
{"type":"assistant","sessionId":"parent-1","agentId":"child-9","timestamp":"2026-08-01T12:00:03Z","message":{"content":[{"type":"text","text":"and none is lost"}]}}
{"type":"user","sessionId":"parent-1","agentId":"child-9","timestamp":"2026-08-01T12:00:10Z","message":{"content":[{"type":"text","text":"thanks"}]}}
{"type":"assistant","sessionId":"parent-1","agentId":"child-9","timestamp":"2026-08-01T12:00:11Z","message":{"content":[{"type":"text","text":"you are welcome"}]}}
`

func TestSubagentIsItsOwnSessionUnderItsParent(t *testing.T) {
	records, err := Parse(KindSubagent, []byte(subagentTranscript), FileMeta{
		Path:    "/w/.claude/projects/-w-demo/sess/subagents/child-9.jsonl",
		Project: "demo",
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	session := records.Sessions[0]
	// The child's work is credited to the child. Filing it under the parent's id
	// is filing it under somebody else's name.
	if session.ID != "child-9" {
		t.Errorf("session id = %q, want child-9", session.ID)
	}
	if session.ParentID != "parent-1" {
		t.Errorf("parent = %q, want parent-1", session.ParentID)
	}
	if len(session.Exchanges) != 2 {
		t.Fatalf("exchanges = %d, want 2", len(session.Exchanges))
	}
	// Consecutive agent lines are one answer.
	if got := session.Exchanges[0].AgentText; got != "there are nine\nand none is lost" {
		t.Errorf("agent text = %q", got)
	}
	// Every exchange is numbered so the unique index can defend identity without
	// text comparison.
	if session.Exchanges[0].Number != 1 || session.Exchanges[1].Number != 2 {
		t.Errorf("numbers = %d, %d", session.Exchanges[0].Number, session.Exchanges[1].Number)
	}
}

func TestSubagentWithoutAnAgentIDKeepsTheSessionAndDeclaresNoParent(t *testing.T) {
	content := `{"type":"user","sessionId":"solo-1","timestamp":"2026-08-01T12:00:00Z","message":{"content":"hello"}}
{"type":"assistant","sessionId":"solo-1","timestamp":"2026-08-01T12:00:01Z","message":{"content":[{"type":"text","text":"bye"}]}}`
	records, err := Parse(KindSubagent, []byte(content), FileMeta{Path: "/x/subagents/a.jsonl"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	session := records.Sessions[0]
	if session.ID != "solo-1" || session.ParentID != "" {
		t.Errorf("session = %+v", session)
	}
}

func TestSubagentCompactLandsAsAThinkingBlock(t *testing.T) {
	content := `{"type":"system","sessionId":"parent-1","agentId":"compact-3","message":{"content":[{"type":"text","text":"summary: three decisions and a red"}]}}`
	records, err := Parse(KindSubagent, []byte(content),
		FileMeta{Path: "/w/.claude/projects/-w-demo/subagents/acompact-3.jsonl"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	session := records.Sessions[0]
	if len(session.Exchanges) != 0 {
		t.Errorf("a compact is not a conversation: %+v", session.Exchanges)
	}
	if len(session.Thinking) != 1 || session.Thinking[0].Depth != "compact" {
		t.Fatalf("thinking = %+v", session.Thinking)
	}
	if session.Thinking[0].WordCount != 6 {
		t.Errorf("word count = %d, want 6", session.Thinking[0].WordCount)
	}
}

func TestClassifySubagentReadsTheKindOffTheName(t *testing.T) {
	cases := map[string]string{
		"/x/subagents/aside_question-1.jsonl": "aside_question",
		"/x/subagents/acompact-2.jsonl":       "compact",
		"/x/subagents/compact-3.jsonl":        "compact",
		"/x/subagents/agent-4.jsonl":          "subagent",
	}
	for path, want := range cases {
		if got := ClassifySubagent(path); got != want {
			t.Errorf("%s: %q, want %q", path, got, want)
		}
	}
}

func TestLooksLikeSubagentRejectsAForeignTranscriptUnderASharedRoot(t *testing.T) {
	claude := `{"type":"user","sessionId":"s","message":{"content":"hello"}}`
	if confirmed, known := LooksLikeSubagent([]byte(claude)); !confirmed || !known {
		t.Error("a Claude subagent transcript was not recognized")
	}
	foreign := `{"event":"turn","payload":{"role":"user"}}`
	if confirmed, known := LooksLikeSubagent([]byte(foreign)); confirmed || !known {
		t.Error("a foreign transcript was not rejected")
	}
	// A file with nothing parseable in it is the ordinary skip, not a contract
	// failure: there is nothing to be wrong about.
	if confirmed, known := LooksLikeSubagent([]byte("no json here\n")); confirmed || known {
		t.Error("an unparseable file should be unknown, not foreign")
	}
}

// The discard counter is the contract: every source record the parser leaves out
// is counted with its position and its reason. Positional pairing drops the
// surplus side of an unbalanced transcript, and those turns were dropped in
// silence, so `records_discarded` under-reported what the corpus lost.
func TestSubagentCountsTheTurnsPositionalPairingCannotUse(t *testing.T) {
	// Three human turns, one answer: two humans have nowhere to go.
	content := `{"type":"user","sessionId":"p","agentId":"c","timestamp":"2026-08-01T12:00:00Z","message":{"content":"first"}}
{"type":"assistant","sessionId":"p","agentId":"c","timestamp":"2026-08-01T12:00:01Z","message":{"content":"only answer"}}
{"type":"user","sessionId":"p","agentId":"c","timestamp":"2026-08-01T12:00:02Z","message":{"content":"second"}}
{"type":"user","sessionId":"p","agentId":"c","timestamp":"2026-08-01T12:00:03Z","message":{"content":"third"}}
`
	records, err := Parse(KindSubagent, []byte(content), FileMeta{
		Path: "/w/.claude/projects/-w-demo/sess/subagents/c.jsonl",
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(records.Sessions[0].Exchanges) != 1 {
		t.Fatalf("exchanges = %d, want 1", len(records.Sessions[0].Exchanges))
	}
	if len(records.Discards) != 2 {
		t.Fatalf("discards = %d, want 2 unpaired turns: %+v",
			len(records.Discards), records.Discards)
	}
	for _, discard := range records.Discards {
		if discard.Record == 0 || discard.Reason == "" {
			t.Errorf("a discard names neither a record nor a reason: %+v", discard)
		}
	}
}

// A transcript whose turns pair into nothing still has to say what it left out,
// or an operator reads "no exchanges" as "the file was empty".
func TestSubagentWithNoPairableTurnCountsWhatItDropped(t *testing.T) {
	content := `{"type":"user","sessionId":"p","agentId":"c","timestamp":"2026-08-01T12:00:00Z","message":{"content":"nobody answered"}}
`
	records, err := Parse(KindSubagent, []byte(content), FileMeta{
		Path: "/w/.claude/projects/-w-demo/sess/subagents/c.jsonl",
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(records.Discards) != 1 {
		t.Fatalf("discards = %d, want 1: %+v", len(records.Discards), records.Discards)
	}
}
