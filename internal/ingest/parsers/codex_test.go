package parsers

import "testing"

const codexRollout = `
{"type":"session_meta","timestamp":"2026-08-01T09:00:00Z","payload":{"id":"c0dec0de","cwd":"/w/demo","timestamp":"2026-08-01T09:00:00Z","cli_version":"1.2.3","model_provider":"openai"}}
{"type":"turn_context","timestamp":"2026-08-01T09:00:01Z","payload":{"turn_id":"t1","model":"gpt-test"}}
{"type":"event_msg","timestamp":"2026-08-01T09:00:02Z","payload":{"type":"user_message","message":"compile the binary"}}
{"type":"response_item","timestamp":"2026-08-01T09:00:03Z","payload":{"type":"reasoning","summary":[{"text":"we have to"},{"text":"compile"}]}}
{"type":"response_item","timestamp":"2026-08-01T09:00:04Z","payload":{"type":"function_call","call_id":"c1","name":"shell","arguments":"{\"cmd\":\"make build\"}"}}
{"type":"response_item","timestamp":"2026-08-01T09:00:05Z","payload":{"type":"function_call_output","call_id":"c1","output":"{\"metadata\":{\"exit_code\":2}}"}}
{"type":"event_msg","timestamp":"2026-08-01T09:02:06Z","payload":{"type":"task_complete","turn_id":"t1","last_agent_message":"it failed"}}
{"type":"event_msg","timestamp":"2026-08-01T09:03:00Z","payload":{"type":"user_message","message":"and now"}}
{"type":"response_item","timestamp":"2026-08-01T09:03:01Z","payload":{"type":"function_call","call_id":"c2","name":"shell","arguments":"{\"cmd\":\"ls\"}"}}
{"type":"event_msg","timestamp":"2026-08-01T09:03:02Z","payload":{"type":"turn_aborted"}}
`

func TestCodexSessionClosesOnTaskCompleteAndDiscardsAnAbortedTurn(t *testing.T) {
	records, err := Parse(KindCodexSession, []byte(codexRollout), FileMeta{SessionID: "from-the-path"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	session := records.Sessions[0]
	// The rollout's own id outranks the one decoded from the file name.
	if session.ID != "c0dec0de" {
		t.Errorf("session id = %q, want c0dec0de", session.ID)
	}
	if session.Metadata["cwd"] != "/w/demo" || session.Metadata["model"] != "gpt-test" {
		t.Errorf("metadata = %+v", session.Metadata)
	}
	if len(session.Exchanges) != 1 {
		t.Fatalf("exchanges = %d, want 1: the aborted turn is not an exchange", len(session.Exchanges))
	}
	exchange := session.Exchanges[0]
	if exchange.HumanText != "compile the binary" || exchange.AgentText != "it failed" {
		t.Errorf("exchange = %+v", exchange)
	}
	if len(exchange.Thinking) != 1 || exchange.Thinking[0].Text != "we have to compile" {
		t.Fatalf("thinking = %+v", exchange.Thinking)
	}
	if len(exchange.Tools) != 1 {
		t.Fatalf("tools = %+v", exchange.Tools)
	}
	// The verdict comes from the output's exit code and from nowhere else.
	if tool := exchange.Tools[0]; tool.Name != "shell" || !tool.HadError {
		t.Errorf("tool = %+v", tool)
	}
	if session.StartedAt != "2026-08-01T09:00:00Z" || session.EndedAt != "2026-08-01T09:02:06Z" {
		t.Errorf("span = %q..%q", session.StartedAt, session.EndedAt)
	}
	if session.DurationMinutes == nil || *session.DurationMinutes != 2 {
		t.Errorf("duration = %v, want 2", session.DurationMinutes)
	}
}

func TestCodexToolWithoutAFailingExitCodeIsNotAnError(t *testing.T) {
	rollout := `
{"type":"event_msg","timestamp":"2026-08-01T09:00:02Z","payload":{"type":"user_message","message":"lista"}}
{"type":"response_item","payload":{"type":"function_call","call_id":"c1","name":"shell","arguments":"{}"}}
{"type":"response_item","payload":{"type":"function_call_output","call_id":"c1","output":"error: this word is not a verdict"}}
{"type":"event_msg","timestamp":"2026-08-01T09:00:09Z","payload":{"type":"task_complete","last_agent_message":"hecho"}}
`
	records, err := Parse(KindCodexSession, []byte(rollout), FileMeta{SessionID: "s"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	tools := records.Sessions[0].Exchanges[0].Tools
	if len(tools) != 1 || tools[0].HadError {
		t.Errorf("tools = %+v: only a non-zero exit code is an error", tools)
	}
}

// Pi holds a turn the artefact is still writing and reports it as deferred. A
// rollout whose last event is a user message is the same live tail, and it was
// leaving the turn out of the exchanges, out of the discards and out of the
// deferred count: invisible in all three, which reads as a file with nothing new.
func TestCodexHoldsTheTurnTheRolloutIsStillWriting(t *testing.T) {
	content := `{"type":"session_meta","timestamp":"2026-08-01T10:00:00Z","payload":{"id":"roll-1","cwd":"/w/demo"}}
{"type":"event_msg","timestamp":"2026-08-01T10:00:01Z","payload":{"type":"user_message","message":"closed question"}}
{"type":"event_msg","timestamp":"2026-08-01T10:00:02Z","payload":{"type":"task_complete","last_agent_message":"the answer"}}
{"type":"event_msg","timestamp":"2026-08-01T10:00:03Z","payload":{"type":"user_message","message":"still being answered"}}
`
	records, err := Parse(KindCodexSession, []byte(content), FileMeta{SessionID: "roll-1"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := len(records.Sessions[0].Exchanges); got != 1 {
		t.Fatalf("exchanges = %d, want 1: half a turn is not an exchange", got)
	}
	if records.Deferred != 1 {
		t.Errorf("deferred = %d, want 1 turn still in flight", records.Deferred)
	}
}

// A user message arriving over one that never completed replaces it, and the
// replaced turn is gone for good: it is a discard, not a deferral.
func TestCodexCountsAUserMessageThatSupersedesAnUnclosedTurn(t *testing.T) {
	content := `{"type":"session_meta","timestamp":"2026-08-01T10:00:00Z","payload":{"id":"roll-2","cwd":"/w/demo"}}
{"type":"event_msg","timestamp":"2026-08-01T10:00:01Z","payload":{"type":"user_message","message":"interrupted"}}
{"type":"event_msg","timestamp":"2026-08-01T10:00:02Z","payload":{"type":"user_message","message":"asked again"}}
{"type":"event_msg","timestamp":"2026-08-01T10:00:03Z","payload":{"type":"task_complete","last_agent_message":"the answer"}}
`
	records, err := Parse(KindCodexSession, []byte(content), FileMeta{SessionID: "roll-2"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := len(records.Sessions[0].Exchanges); got != 1 {
		t.Fatalf("exchanges = %d, want 1", got)
	}
	if len(records.Discards) != 1 {
		t.Fatalf("discards = %d, want the superseded turn counted: %+v",
			len(records.Discards), records.Discards)
	}
	if records.Sessions[0].Exchanges[0].HumanText != "asked again" {
		t.Errorf("the completed turn kept the wrong question: %q",
			records.Sessions[0].Exchanges[0].HumanText)
	}
}

// The same rule down the Codex path: a reasoning event whose summary carries no
// text is not a thinking block, and leaving it out has to be counted.
func TestCodexDoesNotStoreReasoningWithNoText(t *testing.T) {
	content := `{"type":"session_meta","timestamp":"2026-08-01T10:00:00Z","payload":{"id":"roll-3","cwd":"/w/demo"}}
{"type":"event_msg","timestamp":"2026-08-01T10:00:01Z","payload":{"type":"user_message","message":"why"}}
{"type":"response_item","timestamp":"2026-08-01T10:00:02Z","payload":{"type":"reasoning","summary":[]}}
{"type":"event_msg","timestamp":"2026-08-01T10:00:03Z","payload":{"type":"task_complete","last_agent_message":"because"}}
`
	records, err := Parse(KindCodexSession, []byte(content), FileMeta{SessionID: "roll-3"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	for _, block := range records.Sessions[0].Exchanges[0].Thinking {
		if block.Text == "" {
			t.Errorf("an empty thinking block landed in the corpus: %+v",
				records.Sessions[0].Exchanges[0].Thinking)
		}
	}
	if len(records.Discards) != 1 {
		t.Errorf("discards = %d, want the empty reasoning counted: %+v",
			len(records.Discards), records.Discards)
	}
}
