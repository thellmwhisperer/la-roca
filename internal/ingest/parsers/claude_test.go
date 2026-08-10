package parsers

import "testing"

// The fixtures are entirely synthetic: no real transcript or private vocabulary
// enters a test.

const claudeTranscript = `
{"type":"user","timestamp":"2026-08-01T10:00:00.000Z","cwd":"/w/demo","message":{"content":"how many memories are there"}}
{"type":"assistant","timestamp":"2026-08-01T10:00:02.500Z","message":{"content":[{"type":"thinking","thinking":"two words and a count"},{"type":"text","text":"there are three"},{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"ls -la"}}]}}
{"type":"user","timestamp":"2026-08-01T10:00:03.000Z","message":{"content":[{"type":"tool_result","tool_use_id":"t1","is_error":true,"content":[{"type":"text","text":"no such directory"}]}]}}
{"type":"assistant","timestamp":"2026-08-01T10:00:04.000Z","message":{"content":[{"type":"text","text":"I will fix it"}]}}
{"type":"user","timestamp":"2026-08-01T10:00:10.000Z","message":{"content":[{"type":"text","text":"and now"}]}}
{"type":"assistant","timestamp":"2026-08-01T10:00:11.000Z","message":{"content":[{"type":"text","text":"ready"}]}}
`

func TestClaudeSessionSplitsExchangesOnTheHumanTurn(t *testing.T) {
	records, err := Parse(KindClaudeSession, []byte(claudeTranscript), FileMeta{
		Path:        "/w/.claude/projects/-w-demo/11111111-2222-3333-4444-555555555555.jsonl",
		SessionID:   "11111111-2222-3333-4444-555555555555",
		Project:     "demo",
		SourceAgent: "claude-code",
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(records.Sessions) != 1 {
		t.Fatalf("sessions = %d, want 1", len(records.Sessions))
	}
	session := records.Sessions[0]
	if session.ID != "11111111-2222-3333-4444-555555555555" {
		t.Errorf("session id = %q", session.ID)
	}
	if session.Project != "demo" {
		t.Errorf("project = %q, want demo", session.Project)
	}
	// Tool results are runtime traffic inside the open human turn. Two human turns
	// therefore produce two exchanges even when an answer continues after a tool.
	if len(session.Exchanges) != 2 {
		t.Fatalf("exchanges = %d, want 2", len(session.Exchanges))
	}
	first := session.Exchanges[0]
	if first.Number != 1 {
		t.Errorf("first exchange number = %d, want 1", first.Number)
	}
	if first.HumanText != "how many memories are there" {
		t.Errorf("human text = %q", first.HumanText)
	}
	// The assistant's text blocks are joined, and only the text ones.
	if first.AgentText != "there are three\nI will fix it" {
		t.Errorf("agent text = %q", first.AgentText)
	}
	if second := session.Exchanges[1]; second.HumanText != "and now" || second.AgentText != "ready" {
		t.Errorf("second exchange = %+v", second)
	}
	if first.LatencyMS == nil || *first.LatencyMS != 2500 {
		t.Errorf("latency = %v, want 2500", first.LatencyMS)
	}
	if len(first.Thinking) != 1 || first.Thinking[0].WordCount != 5 {
		t.Fatalf("thinking = %+v", first.Thinking)
	}
	if got := first.Thinking[0].Position; got != 0.5 {
		t.Errorf("position = %v, want 1/2", got)
	}
	if len(first.Tools) != 1 {
		t.Fatalf("tools = %+v", first.Tools)
	}
	// The error is backfilled from the tool_result that answered the call.
	tool := first.Tools[0]
	if tool.Name != "Bash" || !tool.HadError {
		t.Errorf("tool = %+v", tool)
	}
	if tool.ErrorMessage != "no such directory" {
		t.Errorf("error message = %q", tool.ErrorMessage)
	}
	if tool.ParamsSummary != `{"command":"ls -la"}` {
		t.Errorf("params summary = %q", tool.ParamsSummary)
	}
	if session.StartedAt != "2026-08-01T10:00:00.000Z" {
		t.Errorf("started at = %q", session.StartedAt)
	}
	if session.EndedAt != "2026-08-01T10:00:11.000Z" {
		t.Errorf("ended at = %q", session.EndedAt)
	}
	if session.DurationMinutes == nil || *session.DurationMinutes != 0 {
		t.Errorf("duration = %v, want 0", session.DurationMinutes)
	}
}

const claudeCompacted = `
{"type":"user","timestamp":"2026-08-01T10:00:00Z","message":{"content":"first"}}
{"type":"assistant","timestamp":"2026-08-01T10:00:01Z","message":{"content":[{"type":"text","text":"one"}]}}
{"type":"summary","summary":"summary of the previous conversation"}
{"type":"user","timestamp":"2026-08-01T10:05:00Z","message":{"content":"second"}}
{"type":"assistant","timestamp":"2026-08-01T10:05:01Z","message":{"content":[{"type":"thinking","thinking":"continuing"},{"type":"text","text":"two"}]}}
`

func TestClaudeSessionMarksWhatComesAfterACompaction(t *testing.T) {
	records, err := Parse(KindClaudeSession, []byte(claudeCompacted), FileMeta{SessionID: "s1"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	exchanges := records.Sessions[0].Exchanges
	if len(exchanges) != 2 {
		t.Fatalf("exchanges = %d, want 2", len(exchanges))
	}
	if exchanges[0].IsAfterCompaction {
		t.Error("the exchange before the compaction is marked as after it")
	}
	if !exchanges[1].IsAfterCompaction {
		t.Error("the exchange after the compaction is not marked")
	}
	if !exchanges[1].Thinking[0].IsAfterCompaction {
		t.Error("the thinking block does not carry its exchange's compaction mark")
	}
	if records.Sessions[0].Metadata["compactions"] != 1 {
		t.Errorf("metadata = %+v", records.Sessions[0].Metadata)
	}
}

func TestClaudeSessionSurvivesGarbage(t *testing.T) {
	// A live transcript can be truncated mid-line, and a corrupt line cannot
	// cost the whole file.
	broken := "{not json\n" +
		`{"type":"user","timestamp":"2026-08-01T10:00:00Z","message":{"content":"hello"}}` + "\n" +
		`{"type":"assistant","timestamp":"2026-08-01T10:00:01Z","message":{"content":[{"type":"text","text":"bye"}]}}` + "\n" +
		`{"type":"assistant","timestamp":"2026`
	records, err := Parse(KindClaudeSession, []byte(broken), FileMeta{SessionID: "s2"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(records.Sessions[0].Exchanges) != 1 {
		t.Fatalf("exchanges = %d, want 1", len(records.Sessions[0].Exchanges))
	}
}

func TestClaudeSessionWithoutAnyExchangeIsSkipped(t *testing.T) {
	records, err := Parse(KindClaudeSession, []byte(`{"type":"user","message":{"content":"no answer"}}`),
		FileMeta{SessionID: "s3"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	// The session still exists: it was opened, it just has nothing in it yet.
	if len(records.Sessions) != 1 || len(records.Sessions[0].Exchanges) != 0 {
		t.Fatalf("records = %+v", records)
	}
}

func TestClaudeSessionReadsTheFirstDeclaredCwd(t *testing.T) {
	records, err := Parse(KindClaudeSession, []byte(claudeTranscript), FileMeta{SessionID: "s4"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := records.Sessions[0].Metadata["cwd"]; got != "/w/demo" {
		t.Errorf("cwd = %v, want /w/demo", got)
	}
}
