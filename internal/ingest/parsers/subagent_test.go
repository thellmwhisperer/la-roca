package parsers

import "testing"

const subagentTranscript = `
{"type":"user","sessionId":"parent-1","agentId":"child-9","timestamp":"2026-08-01T12:00:00Z","message":{"content":[{"type":"text","text":"busca los adaptadores"}]}}
{"type":"assistant","sessionId":"parent-1","agentId":"child-9","timestamp":"2026-08-01T12:00:02Z","message":{"content":[{"type":"text","text":"hay nueve"}]}}
{"type":"assistant","sessionId":"parent-1","agentId":"child-9","timestamp":"2026-08-01T12:00:03Z","message":{"content":[{"type":"text","text":"y ninguno se pierde"}]}}
{"type":"user","sessionId":"parent-1","agentId":"child-9","timestamp":"2026-08-01T12:00:10Z","message":{"content":[{"type":"text","text":"gracias"}]}}
{"type":"assistant","sessionId":"parent-1","agentId":"child-9","timestamp":"2026-08-01T12:00:11Z","message":{"content":[{"type":"text","text":"de nada"}]}}
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
	if got := session.Exchanges[0].AgentText; got != "hay nueve\ny ninguno se pierde" {
		t.Errorf("agent text = %q", got)
	}
	// Every exchange is numbered, which is what lets the unique index defend it
	// instead of a text comparison (PRD M2: one idempotency contract).
	if session.Exchanges[0].Number != 1 || session.Exchanges[1].Number != 2 {
		t.Errorf("numbers = %d, %d", session.Exchanges[0].Number, session.Exchanges[1].Number)
	}
}

func TestSubagentWithoutAnAgentIDKeepsTheSessionAndDeclaresNoParent(t *testing.T) {
	content := `{"type":"user","sessionId":"solo-1","timestamp":"2026-08-01T12:00:00Z","message":{"content":"hola"}}
{"type":"assistant","sessionId":"solo-1","timestamp":"2026-08-01T12:00:01Z","message":{"content":[{"type":"text","text":"adios"}]}}`
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
	content := `{"type":"system","sessionId":"parent-1","agentId":"compact-3","message":{"content":[{"type":"text","text":"resumen: tres decisiones y un rojo"}]}}`
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
	claude := `{"type":"user","sessionId":"s","message":{"content":"hola"}}`
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
