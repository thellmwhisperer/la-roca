package parsers

import (
	"strings"
	"testing"
)

// grokTranscript is a synthetic Grok Build chat_history.jsonl: real shape,
// invented content. The system prompt and the compaction history injected as a
// synthetic user turn are runtime machinery, and only the one real human
// message opens an exchange.
const grokTranscript = `
{"type":"system","content":"You are Grok Build, a synthetic fixture assistant."}
{"type":"user","content":[{"type":"text","text":"Ignore the compacted history of the fixture."}],"synthetic_reason":"compaction_meta"}
{"type":"user","content":[{"type":"text","text":"compile the fixture binary"}]}
{"type":"reasoning","id":"r1","summary":[{"type":"summary_text","text":"we have to"},{"type":"summary_text","text":"compile it"}],"encrypted_content":"ciphertext","status":"completed"}
{"type":"assistant","content":"let me try","tool_calls":[{"id":"call-grok-1","name":"shell","arguments":"{\"cmd\":\"make build\"}"},{"id":"call-grok-2","name":"read_file","arguments":"{\"target_file\":\"/synthetic/demo/Makefile\"}"}],"model_id":"grok-fixture-model","model_fingerprint":"fp-fixture","reasoning_effort":"high"}
{"type":"tool_result","tool_call_id":"call-grok-1","content":"exit: 1 Error: target build not found"}
{"type":"tool_result","tool_call_id":"call-grok-2","content":"1->---\nname: fixture"}
{"type":"reasoning","id":"r2","summary":[{"type":"summary_text","text":"the binary failed to compile"}],"encrypted_content":"ciphertext","status":"completed"}
{"type":"assistant","content":"the build failed","model_id":"grok-fixture-model","reasoning_effort":"high"}
`

func TestGrokSessionReadsTheActiveExchangeWithItsThinkingToolsAndModel(t *testing.T) {
	records, err := Parse(KindGrokSession, []byte(grokTranscript), FileMeta{SessionID: "from-the-path"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	session := records.Sessions[0]
	if session.SourceAgent != "grok" {
		t.Errorf("source agent = %q, want grok", session.SourceAgent)
	}
	if session.ID != "from-the-path" {
		t.Errorf("session id = %q, want the file name identity without a sidecar", session.ID)
	}
	if len(session.Exchanges) != 1 {
		t.Fatalf("exchanges = %d, want the one real human turn", len(session.Exchanges))
	}
	exchange := session.Exchanges[0]
	if exchange.HumanText != "compile the fixture binary" {
		t.Errorf("human text = %q", exchange.HumanText)
	}
	if exchange.AgentText != "let me try\nthe build failed" {
		t.Errorf("agent text = %q", exchange.AgentText)
	}
	if len(exchange.Thinking) != 2 || exchange.Thinking[0].Text != "we have to compile it" {
		t.Fatalf("thinking = %+v", exchange.Thinking)
	}
	if exchange.Thinking[1].Text != "the binary failed to compile" {
		t.Errorf("second thinking = %+v", exchange.Thinking[1])
	}
	if exchange.Provenance.Model != "grok-fixture-model" {
		t.Errorf("model = %q", exchange.Provenance.Model)
	}
	if len(exchange.Tools) != 2 {
		t.Fatalf("tools = %+v", exchange.Tools)
	}
	// The verdict comes from the stated exit code and from nowhere else.
	first, second := exchange.Tools[0], exchange.Tools[1]
	if first.Name != "shell" || !first.HadError {
		t.Errorf("failed tool = %+v", first)
	}
	if first.ErrorMessage != "exit: 1 Error: target build not found" {
		t.Errorf("error message = %q, want the result's text and not its JSON", first.ErrorMessage)
	}
	if second.Name != "read_file" || second.HadError {
		t.Errorf("clean tool = %+v", second)
	}
	// The system prompt and the injected compaction history are runtime
	// machinery, excluded by name and never exchanges.
	if len(records.Discards) != 2 {
		t.Fatalf("discards = %+v", records.Discards)
	}
	for _, discard := range records.Discards {
		if !discard.ByDesign {
			t.Errorf("runtime record counted as unreadable: %+v", discard)
		}
	}
}

func TestGrokExitCodeStatesAFailureAndNothingElseDoes(t *testing.T) {
	for _, content := range []string{
		"exit: 0 [truncated: showing the fixture]",
		"1->---\nname: stow",
		"the build printed the word error but exited cleanly",
	} {
		if failedGrokExit(content) {
			t.Errorf("exit code found in %q", content)
		}
	}
	for _, content := range []string{"exit: 1 Error: unable to open", "exit: 127 command not found"} {
		if !failedGrokExit(content) {
			t.Errorf("no failing exit code found in %q", content)
		}
	}
}

// grokTranscriptShapes pins the transcripts that must not land whole: a question
// still in flight, an orphan verdict, activity before any question, and two
// complete turns. They are one table because they share one shape — a synthetic
// transcript and the exchanges, deferred count and discard it must produce.
func TestGrokTranscriptShapes(t *testing.T) {
	cases := []struct {
		name            string
		transcript      string
		exchanges       int
		deferred        int
		discardContains string
		discardByDesign bool
	}{
		{
			name: "a record of another shape is unreadable and not left out by design",
			transcript: `
{"type":"user","content":[{"type":"text","text":"read the fixture"}]}
{"type":["assistant"],"content":"a record of another shape"}
`,
			exchanges:       0,
			deferred:        1,
			discardContains: "invalid JSON",
		},
		{
			name: "an open question is deferred and unreadable reasoning is excluded",
			transcript: `
{"type":"user","content":[{"type":"text","text":"finish this fixture"}]}
{"type":"reasoning","id":"r1","summary":[],"encrypted_content":"ciphertext","status":"completed"}
`,
			exchanges:       0,
			deferred:        1,
			discardContains: "kept no readable summary",
			discardByDesign: true,
		},
		{
			name: "an orphan verdict is discarded and never closes a turn",
			transcript: `
{"type":"user","content":[{"type":"text","text":"run the fixture"}]}
{"type":"assistant","content":"running","tool_calls":[],"model_id":"grok-fixture-model"}
{"type":"tool_result","tool_call_id":"call-nobody-made","content":"exit: 0"}
`,
			exchanges:       1,
			discardContains: "unknown call_id",
		},
		{
			name: "agent activity before any human turn is ignored",
			transcript: `
{"type":"assistant","content":"orphaned","tool_calls":[],"model_id":"grok-fixture-model"}
{"type":"reasoning","summary":[{"type":"summary_text","text":"orphaned"}]}
{"type":"tool_result","tool_call_id":"call-orphan","content":"exit: 0"}
`,
			exchanges: 0,
		},
		{
			name: "two real turns make two exchanges",
			transcript: `
{"type":"user","content":[{"type":"text","text":"first fixture question"}]}
{"type":"assistant","content":"first answer","model_id":"grok-fixture-model"}
{"type":"user","content":[{"type":"text","text":"second fixture question"}]}
{"type":"assistant","content":"second answer","model_id":"grok-fixture-model"}
`,
			exchanges: 2,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			records, err := Parse(KindGrokSession, []byte(tc.transcript), FileMeta{})
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			exchanges := records.Sessions[0].Exchanges
			if len(exchanges) != tc.exchanges {
				t.Fatalf("exchanges = %d, want %d", len(exchanges), tc.exchanges)
			}
			if records.Deferred != tc.deferred {
				t.Errorf("deferred = %d, want %d", records.Deferred, tc.deferred)
			}
			if tc.discardContains != "" {
				found := false
				for _, discard := range records.Discards {
					if strings.Contains(discard.Reason, tc.discardContains) {
						found = true
						if discard.ByDesign != tc.discardByDesign {
							t.Errorf("discard %+v: by design = %v, want %v",
								discard, discard.ByDesign, tc.discardByDesign)
						}
					}
				}
				if !found {
					t.Errorf("discards = %+v, want one naming %q", records.Discards, tc.discardContains)
				}
			}
		})
	}
}

func TestGrokSessionMetadataSnapshotsTheSessionsIdentityAndSpan(t *testing.T) {
	summary := `{
  "info": {"id": "22222222-3333-4444-5555-666666666666", "cwd": "/w/demo"},
  "session_summary": "Synthetic Grok fixture.",
  "created_at": "2026-08-01T14:00:00.123456Z",
  "updated_at": "2026-08-01T14:00:30Z",
  "last_active_at": "2026-08-01T14:00:30Z",
  "num_messages": 6,
  "num_chat_messages": 6,
  "current_model_id": "grok-fixture-model",
  "git_root_dir": "/w/demo",
  "git_remotes": ["https://synthetic.example/la-roca"],
  "head_commit": "0123456789abcdef",
  "head_branch": "main",
  "agent_name": "grok-build",
  "sandbox_profile": "off",
  "reasoning_effort": "high",
  "request_id": "req-fixture",
  "chat_format_version": 1,
  "generated_title": "the ninth fixture"
}`
	records, err := Parse(KindGrokSessionMetadata, []byte(summary), FileMeta{SourceAgent: "grok"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	session := records.Sessions[0]
	if !session.Snapshot {
		t.Fatal("metadata reading is not a snapshot")
	}
	if session.ID != "22222222-3333-4444-5555-666666666666" || session.SourceAgent != "grok" {
		t.Errorf("identity = %+v", session)
	}
	if session.StartedAt != "2026-08-01T14:00:00.123456Z" || session.EndedAt != "2026-08-01T14:00:30Z" {
		t.Errorf("span = %q..%q", session.StartedAt, session.EndedAt)
	}
	if session.DurationMinutes == nil || *session.DurationMinutes != 0 {
		t.Errorf("duration = %v, want 0", session.DurationMinutes)
	}
	if session.Title != "the ninth fixture" {
		t.Errorf("title = %q", session.Title)
	}
	if session.Metadata["model"] != "grok-fixture-model" || session.Metadata["cwd"] != "/w/demo" {
		t.Errorf("metadata = %+v", session.Metadata)
	}
	if session.Metadata["head_commit"] != "0123456789abcdef" {
		t.Errorf("head commit missing: %+v", session.Metadata)
	}
}

func TestGrokSessionTakesItsIdentityAndSpanFromTheSidecar(t *testing.T) {
	sidecar := `{
  "info": {"id": "22222222-3333-4444-5555-666666666666", "cwd": "/w/demo"},
  "created_at": "2026-08-01T14:00:00Z",
  "updated_at": "2026-08-01T14:00:30Z",
  "generated_title": "the ninth fixture"
}`
	records, err := Parse(KindGrokSession, []byte(grokTranscript), FileMeta{
		SessionID: "from-the-path",
		Sidecar:   []byte(sidecar),
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	session := records.Sessions[0]
	if session.ID != "22222222-3333-4444-5555-666666666666" {
		t.Errorf("session id = %q, want the sidecar's", session.ID)
	}
	if session.Title != "the ninth fixture" {
		t.Errorf("title = %q", session.Title)
	}
	if session.StartedAt != "2026-08-01T14:00:00Z" || session.EndedAt != "2026-08-01T14:00:30Z" {
		t.Errorf("span = %q..%q", session.StartedAt, session.EndedAt)
	}
}

func TestGrokSessionMetadataWithoutIdentityIsDiscarded(t *testing.T) {
	records, err := Parse(KindGrokSessionMetadata, []byte(`{"info":{"cwd":"/w/demo"}}`), FileMeta{})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(records.Sessions) != 0 || len(records.Discards) != 1 {
		t.Fatalf("records = %+v", records)
	}
}
