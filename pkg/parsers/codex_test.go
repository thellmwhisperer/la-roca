package parsers

import (
	"fmt"
	"strings"
	"testing"
)

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

// A rollout whose last event is a user message is a live tail, the same shape Pi
// holds and reports as deferred. It was leaving the turn out of the exchanges,
// out of the discards and out of the deferred count: invisible in all three,
// which reads as a file with nothing new. Deferred and discarded are exclusive:
// a turn the next run will land is not a turn this run threw away.
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
	if got := len(records.Discards); got != 0 {
		t.Errorf("discards = %d, want none: a deferred turn is not a discarded one: %+v",
			got, records.Discards)
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
	// The second line is the question that never completed.
	if discard := records.Discards[0]; discard.Record != 2 || discard.Reason == "" {
		t.Errorf("discard = %+v, want record 2 with a reason", discard)
	}
	if records.Sessions[0].Exchanges[0].HumanText != "asked again" {
		t.Errorf("the completed turn kept the wrong question: %q",
			records.Sessions[0].Exchanges[0].HumanText)
	}
}

func TestCodexAppliesALateVerdictToTheSupersededOrphan(t *testing.T) {
	content := `{"type":"event_msg","payload":{"type":"user_message","message":"first"}}
{"type":"response_item","payload":{"type":"function_call","call_id":"old","name":"shell"}}
{"type":"event_msg","payload":{"type":"user_message","message":"replacement"}}
{"type":"response_item","payload":{"type":"function_call_output","call_id":"old","output":{"metadata":{"exit_code":1}}}}
{"type":"event_msg","payload":{"type":"task_complete","last_agent_message":"done"}}`
	records, err := Parse(KindCodexSession, []byte(content), FileMeta{SessionID: "roll-verdict"})
	if err != nil {
		t.Fatal(err)
	}
	if len(records.Discards) != 1 {
		t.Fatalf("discards = %+v, want only the superseded turn", records.Discards)
	}
	session := records.Sessions[0]
	if len(session.Exchanges) != 1 || len(session.Exchanges[0].Tools) != 0 {
		t.Fatalf("replacement exchange = %+v, want no superseded tool", session.Exchanges)
	}
	if len(session.OrphanedTools) != 1 {
		t.Fatalf("orphaned tools = %+v, want the superseded call", session.OrphanedTools)
	}
	tool := session.OrphanedTools[0]
	if !tool.HadError || tool.ErrorMessage != `{"metadata":{"exit_code":1}}` {
		t.Fatalf("superseded tool verdict = %+v", tool)
	}
}

func TestCodexRecoveryDoesNotAttachToolsFromAnAbortedEventTurn(t *testing.T) {
	content := `{"type":"event_msg","payload":{"type":"user_message","message":"interrupted"}}
{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"interrupted"}]}}
{"type":"response_item","payload":{"type":"function_call","call_id":"aborted","name":"shell"}}
{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"partial answer"}]}}
{"type":"event_msg","payload":{"type":"turn_aborted"}}`
	records, err := Parse(KindCodexSession, []byte(content), FileMeta{SessionID: "recovered-abort"})
	if err != nil {
		t.Fatal(err)
	}
	session := records.Sessions[0]
	if len(session.Exchanges) != 1 || session.Exchanges[0].HumanText != "interrupted" ||
		session.Exchanges[0].AgentText != "partial answer" {
		t.Fatalf("recovered conversation = %+v", session.Exchanges)
	}
	if len(session.Exchanges[0].Tools) != 0 || len(session.OrphanedTools) != 1 ||
		session.OrphanedTools[0].Name != "shell" {
		t.Fatalf("recovered tool ownership = attached:%+v orphaned:%+v",
			session.Exchanges[0].Tools, session.OrphanedTools)
	}
}

func TestCodexResponseOnlyRecoveryKeepsToolsOrphaned(t *testing.T) {
	content := `{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"interrupted"}]}}
{"type":"response_item","payload":{"type":"function_call","call_id":"response-only","name":"shell"}}
{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"partial answer"}]}}`
	for _, testCase := range []struct {
		name, suffix string
	}{
		{name: "open"},
		{name: "aborted", suffix: `
{"type":"event_msg","payload":{"type":"turn_aborted"}}`},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			records, err := Parse(KindCodexSession, []byte(content+testCase.suffix),
				FileMeta{SessionID: "response-only"})
			if err != nil {
				t.Fatal(err)
			}
			session := records.Sessions[0]
			if len(session.Exchanges) != 1 || session.Exchanges[0].HumanText != "interrupted" ||
				session.Exchanges[0].AgentText != "partial answer" {
				t.Fatalf("recovered conversation = %+v", session.Exchanges)
			}
			if len(session.Exchanges[0].Tools) != 0 || len(session.OrphanedTools) != 1 ||
				session.OrphanedTools[0].Name != "shell" {
				t.Fatalf("response-only tool ownership = attached:%+v orphaned:%+v",
					session.Exchanges[0].Tools, session.OrphanedTools)
			}
		})
	}
}

func TestCodexAppliesALateVerdictToAResponseOnlyOrphan(t *testing.T) {
	content := `{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"interrupted"}]}}
{"type":"response_item","payload":{"type":"function_call","call_id":"response-only","name":"shell"}}
{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"partial answer"}]}}
{"type":"event_msg","payload":{"type":"user_message","message":"next"}}
{"type":"response_item","payload":{"type":"function_call_output","call_id":"response-only","output":"failure\nProcess exited with code 1"}}
{"type":"event_msg","payload":{"type":"task_complete","last_agent_message":"done"}}`
	records, err := Parse(KindCodexSession, []byte(content), FileMeta{SessionID: "response-only-verdict"})
	if err != nil {
		t.Fatal(err)
	}
	session := records.Sessions[0]
	if len(session.Exchanges) != 1 || session.Exchanges[0].HumanText != "next" ||
		session.Exchanges[0].AgentText != "done" || len(session.Exchanges[0].Tools) != 0 {
		t.Fatalf("completed event turn = %+v", session.Exchanges)
	}
	if len(session.OrphanedTools) != 1 {
		t.Fatalf("orphaned tools = %+v, want the response-only call", session.OrphanedTools)
	}
	tool := session.OrphanedTools[0]
	if !tool.HadError || tool.ErrorMessage != "failure\nProcess exited with code 1" {
		t.Fatalf("response-only tool verdict = %+v", tool)
	}
}

func TestCodexAppliesALateVerdictAfterAResponseOnlyAbort(t *testing.T) {
	content := `{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"first"}]}}
{"type":"response_item","payload":{"type":"function_call","call_id":"first-call","name":"shell"}}
{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"first answer"}]}}
{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"second"}]}}
{"type":"event_msg","payload":{"type":"turn_aborted"}}
{"type":"response_item","payload":{"type":"function_call_output","call_id":"first-call","output":"failure\nProcess exited with code 1"}}`
	records, err := Parse(KindCodexSession, []byte(content), FileMeta{SessionID: "response-only-abort-verdict"})
	if err != nil {
		t.Fatal(err)
	}
	session := records.Sessions[0]
	if len(session.Exchanges) != 1 || session.Exchanges[0].HumanText != "first" ||
		session.Exchanges[0].AgentText != "first answer" || len(session.Exchanges[0].Tools) != 0 {
		t.Fatalf("recovered conversation = %+v", session.Exchanges)
	}
	if len(session.OrphanedTools) != 1 {
		t.Fatalf("orphaned tools = %+v, want the first response-only call", session.OrphanedTools)
	}
	tool := session.OrphanedTools[0]
	if !tool.HadError || tool.ErrorMessage != "failure\nProcess exited with code 1" {
		t.Fatalf("response-only aborted tool verdict = %+v", tool)
	}
}

func TestCodexKeepsToolTelemetryFromInterruptedTurns(t *testing.T) {
	var rollout strings.Builder
	writeUser := func(message string) {
		fmt.Fprintf(&rollout, `{"type":"event_msg","payload":{"type":"user_message","message":%q}}`, message)
		rollout.WriteByte('\n')
	}
	writeTools := func(prefix string, count int) {
		for index := range count {
			fmt.Fprintf(&rollout, `{"type":"response_item","payload":{"type":"function_call","call_id":"%s-%d","name":"shell","arguments":"{}"}}`, prefix, index)
			rollout.WriteByte('\n')
		}
	}

	writeUser("superseded turn")
	writeTools("superseded", 550)
	writeUser("aborted turn")
	writeTools("aborted", 550)
	rollout.WriteString("{\"type\":\"event_msg\",\"payload\":{\"type\":\"turn_aborted\"}}\n")
	writeUser("completed turn")
	writeTools("attached", 471)
	rollout.WriteString("{\"type\":\"event_msg\",\"payload\":{\"type\":\"task_complete\",\"last_agent_message\":\"done\"}}\n")

	records, err := Parse(KindCodexSession, []byte(rollout.String()), FileMeta{SessionID: "interrupt-heavy"})
	if err != nil {
		t.Fatal(err)
	}
	session := records.Sessions[0]
	if len(session.Exchanges) != 1 || len(session.Exchanges[0].Tools) != 471 {
		t.Fatalf("recognized exchanges/tools = %d/%d, want 1/471",
			len(session.Exchanges), len(session.Exchanges[0].Tools))
	}
	if len(session.OrphanedTools) != 1100 {
		t.Fatalf("orphaned tools = %d, want 1100 of 1571 total", len(session.OrphanedTools))
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
	// Nothing is stored, not merely nothing empty: a parser that synthesized
	// whitespace for the missing summary would satisfy a per-block check.
	if got := len(records.Sessions[0].Exchanges[0].Thinking); got != 0 {
		t.Errorf("thinking blocks = %d, want none: %+v", got,
			records.Sessions[0].Exchanges[0].Thinking)
	}
	if len(records.Discards) != 1 {
		t.Fatalf("discards = %d, want the empty reasoning counted: %+v",
			len(records.Discards), records.Discards)
	}
	// It is counted as the exclusion it is: the rollout keeps that reasoning
	// encrypted and writes no summary of it, so there was never anything to read.
	if discard := records.Discards[0]; discard.Record != 3 || discard.Reason == "" || !discard.ByDesign {
		t.Errorf("discard = %+v, want record 3 excluded by design with a reason", discard)
	}
}

// The rollout shapes a newer Codex writes. Every one of these used to be
// discarded: the reasoning events as unsupported, the agent's own message as
// unsupported, the turn context because its `summary` is a policy word and not a
// list of blocks, and a whole conversation because its process died before it
// could write a `task_complete`.
func TestCodexReadsTheNewerRolloutShapes(t *testing.T) {
	for _, want := range []struct {
		name         string
		content      string
		human, agent string
		model        string
		thinking     []string
		tokensIn     int
		statesTokens bool
	}{
		{
			name: "a streamed reasoning event is a thinking block",
			content: `{"type":"event_msg","payload":{"type":"user_message","message":"why"}}
{"type":"event_msg","payload":{"type":"agent_reasoning","text":"because of the index"}}
{"type":"event_msg","payload":{"type":"task_complete","last_agent_message":"the index"}}`,
			human: "why", agent: "the index", thinking: []string{"because of the index"},
		},
		{
			// The event and the response item carry the same words, and the corpus
			// keeps one of them.
			name: "the persisted copy of the same reasoning is not stored twice",
			content: `{"type":"event_msg","payload":{"type":"user_message","message":"why"}}
{"type":"event_msg","payload":{"type":"agent_reasoning","text":"because of the index"}}
{"type":"response_item","payload":{"type":"reasoning","summary":[{"text":"because of the index"}]}}
{"type":"event_msg","payload":{"type":"task_complete","last_agent_message":"the index"}}`,
			human: "why", agent: "the index", thinking: []string{"because of the index"},
		},
		{
			name: "a summary written as bare strings still reads",
			content: `{"type":"event_msg","payload":{"type":"user_message","message":"why"}}
{"type":"response_item","payload":{"type":"reasoning","summary":["first","second"]}}
{"type":"event_msg","payload":{"type":"task_complete","last_agent_message":"done"}}`,
			human: "why", agent: "done", thinking: []string{"first second"},
		},
		{
			// The turn context is where the model's name lives, and a strict read of
			// its `summary` key threw the whole record away.
			name: "a turn context whose summary is a policy word still names the model",
			content: `{"type":"turn_context","payload":{"model":"gpt-new","effort":"high","summary":"auto"}}
{"type":"event_msg","payload":{"type":"user_message","message":"why"}}
{"type":"event_msg","payload":{"type":"task_complete","last_agent_message":"done"}}`,
			human: "why", agent: "done", model: "gpt-new",
		},
		{
			name: "the agent's own message answers a turn that completes with no text",
			content: `{"type":"event_msg","payload":{"type":"user_message","message":"why"}}
{"type":"event_msg","payload":{"type":"agent_message","message":"the index is stale"}}
{"type":"event_msg","payload":{"type":"task_complete"}}`,
			human: "why", agent: "the index is stale",
		},
		{
			name: "the token counts of a turn add up into its usage",
			content: `{"type":"event_msg","payload":{"type":"user_message","message":"why"}}
{"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":10,"output_tokens":2,"reasoning_output_tokens":1}}}}
{"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":5,"output_tokens":3,"reasoning_output_tokens":2}}}}
{"type":"event_msg","payload":{"type":"task_complete","last_agent_message":"done"}}`,
			human: "why", agent: "done", tokensIn: 15, statesTokens: true,
		},
		{
			// A rollout whose process died still holds the conversation it had.
			name: "a rollout with no completed turn recovers its response items",
			content: `{"type":"response_item","payload":{"type":"message","role":"developer","content":[{"type":"input_text","text":"the system prompt"}]}}
{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"why"}]}}
{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"the index"}]}}`,
			human: "why", agent: "the index",
		},
	} {
		t.Run(want.name, func(t *testing.T) {
			records, err := Parse(KindCodexSession, []byte(want.content), FileMeta{SessionID: "roll"})
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			exchanges := records.Sessions[0].Exchanges
			if len(exchanges) != 1 {
				t.Fatalf("exchanges = %d, want exactly one: %+v", len(exchanges), exchanges)
			}
			got := exchanges[0]
			if got.HumanText != want.human || got.AgentText != want.agent {
				t.Errorf("turn = %q / %q, want %q / %q",
					got.HumanText, got.AgentText, want.human, want.agent)
			}
			if got.Provenance.Model != want.model {
				t.Errorf("model = %q, want %q", got.Provenance.Model, want.model)
			}
			if len(got.Thinking) != len(want.thinking) {
				t.Fatalf("thinking = %+v, want %v", got.Thinking, want.thinking)
			}
			for i, text := range want.thinking {
				if got.Thinking[i].Text != text {
					t.Errorf("thinking %d = %q, want %q", i, got.Thinking[i].Text, text)
				}
			}
			if got.Provenance.TokensIn == nil {
				if want.statesTokens {
					t.Errorf("tokens_in is absent, want %d", want.tokensIn)
				}
			} else if !want.statesTokens || *got.Provenance.TokensIn != want.tokensIn {
				t.Errorf("tokens_in = %d, want %d (stated=%t)",
					*got.Provenance.TokensIn, want.tokensIn, want.statesTokens)
			}
		})
	}
}

// The response items are the second reading of the same conversation, and using
// both would file every turn twice.
func TestCodexDoesNotReadTheSameTurnTwice(t *testing.T) {
	content := `{"type":"event_msg","payload":{"type":"user_message","message":"why"}}
{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"why"}]}}
{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"the index"}]}}
{"type":"event_msg","payload":{"type":"task_complete","last_agent_message":"the index"}}`
	records, err := Parse(KindCodexSession, []byte(content), FileMeta{SessionID: "roll"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := len(records.Sessions[0].Exchanges); got != 1 {
		t.Fatalf("exchanges = %d, want exactly one", got)
	}
}

// A record this build does not read is an exclusion and not a failure to read
// one, and the report has to be able to tell them apart.
func TestCodexNamesTheRecordsItLeavesOutByDesign(t *testing.T) {
	content := `{"type":"event_msg","payload":{"type":"user_message","message":"why"}}
{"type":"event_msg","payload":{"type":"task_started","turn_id":"t1"}}
{"type":"event_msg","payload":{"type":"task_complete","last_agent_message":"done"}}
{"type":"not a record shape this build reads"}`
	records, err := Parse(KindCodexSession, []byte(content), FileMeta{SessionID: "roll"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(records.Discards) != 2 {
		t.Fatalf("discards = %+v, want the two unread records", records.Discards)
	}
	for _, discard := range records.Discards {
		if !discard.ByDesign {
			t.Errorf("%q is reported as a failure and not as an exclusion", discard.Reason)
		}
	}
}

// A fossil rollout states its own metadata and keeps its prompts as the typeless
// records the older build wrote. Both live in the same file, in whichever order
// that build left them, and reading one may not cost the other.
func TestCodexRecoversLegacyPromptsFromAMetadataBearingRollout(t *testing.T) {
	content := `{"type":"session_meta","timestamp":"2025-11-17T09:42:00Z","payload":{"id":"legacy-one","cwd":"/synthetic/archive","timestamp":"2025-11-17T09:42:00Z","cli_version":"0.58.0","model_provider":"fixture-provider"}}
{"session_id":"legacy-one","ts":1763372540,"text":"inspect the synthetic archive"}
not json
{"type":"compacted","payload":{}}
{"session_id":"legacy-one","ts":1763372660,"text":"verify the synthetic archive"}
{"ts":1763372700,"text":"   "}`
	records, err := Parse(KindCodexSession, []byte(content), FileMeta{SessionID: "fossil"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(records.Sessions) != 1 {
		t.Fatalf("sessions = %d, want the rollout's own: %+v", len(records.Sessions), records.Sessions)
	}
	session := records.Sessions[0]
	if session.ID != "legacy-one" || !session.HistoryFallback || len(session.Exchanges) != 2 {
		t.Fatalf("fossil session = %+v", session)
	}
	if session.Metadata["cwd"] != "/synthetic/archive" ||
		session.Metadata["cli_version"] != "0.58.0" ||
		session.Metadata["model_provider"] != "fixture-provider" {
		t.Errorf("fossil metadata = %+v", session.Metadata)
	}
	if session.StartedAt != "2025-11-17T09:42:00Z" || session.EndedAt != "2025-11-17T09:44:20Z" {
		t.Errorf("fossil span = %q..%q", session.StartedAt, session.EndedAt)
	}
	if exchange := session.Exchanges[1]; exchange.Number != 2 ||
		exchange.HumanText != "verify the synthetic archive" || exchange.AgentText != "" ||
		exchange.HumanTimestamp != "2025-11-17T09:44:20Z" ||
		exchange.AgentTimestamp != "" || !exchange.Provenance.Empty() ||
		!strings.HasPrefix(exchange.SourceID, "codex-history:") {
		t.Errorf("second recovered exchange = %+v", exchange)
	}
	if len(records.Discards) != 3 {
		t.Fatalf("discards = %+v, want the malformed line and the two unread records",
			records.Discards)
	}
	if records.Discards[0].ByDesign || !records.Discards[1].ByDesign ||
		!records.Discards[2].ByDesign {
		t.Errorf("discards report an exclusion as a failure to read: %+v", records.Discards)
	}
}
