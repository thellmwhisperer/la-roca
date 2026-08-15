package parsers

import (
	"strings"
	"testing"
)

// grokUpdates is a synthetic updates.jsonl stream with the structure measured
// from Grok Build's real session store. All identities, paths and content are
// invented.
const grokUpdates = `
{"method":"_x.ai/session/update","params":{"sessionId":"fixture-session","update":{"sessionUpdate":"hook_execution","event_name":"synthetic_hook"}},"timestamp":1785585600}
{"method":"session/update","params":{"sessionId":"fixture-session","update":{"sessionUpdate":"user_message_chunk","content":{"type":"text","text":"compile the fixture"},"_meta":{"modelId":"grok-fixture-model","promptIndex":1}}},"timestamp":1785585601}
{"method":"session/update","params":{"sessionId":"fixture-session","update":{"sessionUpdate":"user_message_chunk","content":{"type":"image","mimeType":"image/png","uri":"fixture://invented-image"},"_meta":{"modelId":"grok-fixture-model","promptIndex":1}}},"timestamp":1785585601}
{"method":"session/update","params":{"sessionId":"fixture-session","_meta":{"promptId":"prompt-1"},"update":{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":"inspect "}}},"timestamp":1785585602}
{"method":"session/update","params":{"sessionId":"fixture-session","_meta":{"promptId":"prompt-1"},"update":{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":"the target"}}},"timestamp":1785585603}
{"method":"session/update","params":{"sessionId":"fixture-session","_meta":{"promptId":"prompt-1"},"update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"I will "}}},"timestamp":1785585604}
{"method":"session/update","params":{"sessionId":"fixture-session","_meta":{"promptId":"prompt-1"},"update":{"sessionUpdate":"tool_call","toolCallId":"tool-1","title":"Run synthetic command","rawInput":{"command":"make fixture"},"_meta":{"x.ai/tool":{"name":"run_terminal_command"}}}},"timestamp":1785585605}
{"method":"session/update","params":{"sessionId":"fixture-session","_meta":{"promptId":"prompt-1"},"update":{"sessionUpdate":"tool_call_update","toolCallId":"tool-1","status":"failed","rawOutput":{"message":"synthetic failure"}}},"timestamp":1785585606}
{"method":"session/update","params":{"sessionId":"fixture-session","_meta":{"promptId":"prompt-1"},"update":{"sessionUpdate":"plan","entries":[{"content":"Inspect the fixture","status":"completed"},{"content":"Repair the fixture","status":"pending"}]}},"timestamp":1785585607}
{"method":"session/update","params":{"sessionId":"fixture-session","_meta":{"promptId":"prompt-1"},"update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"repair it."}}},"timestamp":1785585608}
{"method":"session/update","params":{"sessionId":"fixture-session","update":{"sessionUpdate":"user_message_chunk","content":{"type":"text","text":"verify the fixture"},"_meta":{"modelId":"grok-fixture-model","promptIndex":2}}},"timestamp":1785585610}
{"method":"session/update","params":{"sessionId":"fixture-session","_meta":{"promptId":"prompt-2"},"update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"verified."}}},"timestamp":1785585611}
`

func TestGrokSessionReassemblesTheMeasuredUpdateStream(t *testing.T) {
	path := "/synthetic/home/.grok/sessions/%2Fsynthetic%2Flighthouse/fixture-session/updates.jsonl"
	records, err := Parse(KindGrokSession, []byte(grokUpdates), FileMeta{Path: path})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	session := records.Sessions[0]
	if session.ID != "fixture-session" || session.SourceAgent != "grok" ||
		session.Project != "lighthouse" {
		t.Fatalf("session identity = %+v", session)
	}
	if session.Metadata["cwd"] != "/synthetic/lighthouse" {
		t.Errorf("workspace metadata = %+v", session.Metadata)
	}
	if len(session.Exchanges) != 2 {
		t.Fatalf("exchanges = %d, want two prompts", len(session.Exchanges))
	}
	first := session.Exchanges[0]
	if first.SourceID != "grok-prompt:1" || first.HumanText != "compile the fixture" ||
		first.AgentText != "I will repair it." {
		t.Errorf("first exchange = %+v", first)
	}
	if first.HumanTimestamp != ISOFromEpochSeconds(1785585601) ||
		first.AgentTimestamp != ISOFromEpochSeconds(1785585608) || first.LatencyMS == nil {
		t.Errorf("first timestamps = %q..%q (%v)",
			first.HumanTimestamp, first.AgentTimestamp, first.LatencyMS)
	}
	if first.Provenance.Model != "grok-fixture-model" || first.Provenance.Provider != "xai" {
		t.Errorf("provenance = %+v", first.Provenance)
	}
	if len(first.Thinking) != 2 || first.Thinking[0].Text != "inspect the target" ||
		first.Thinking[1].Text != "Inspect the fixture\nRepair the fixture" {
		t.Errorf("thinking = %+v", first.Thinking)
	}
	if len(first.Tools) != 1 || first.Tools[0].Name != "run_terminal_command" ||
		!first.Tools[0].HadError || !strings.Contains(first.Tools[0].ErrorMessage, "synthetic failure") {
		t.Errorf("tools = %+v", first.Tools)
	}
	if session.StartedAt != first.HumanTimestamp ||
		session.EndedAt != session.Exchanges[1].AgentTimestamp {
		t.Errorf("session span = %q..%q", session.StartedAt, session.EndedAt)
	}
	if len(records.Discards) != 2 {
		t.Fatalf("discards = %+v", records.Discards)
	}
	for _, discard := range records.Discards {
		if !discard.ByDesign {
			t.Errorf("expected only runtime/attachment exclusions: %+v", discard)
		}
	}
}

func TestGrokUpdateShapes(t *testing.T) {
	cases := []struct {
		name            string
		stream          string
		exchanges       int
		deferred        int
		discardContains string
		discardByDesign bool
	}{
		{
			name:     "a live prompt without agent activity is deferred",
			stream:   `{"method":"session/update","params":{"sessionId":"fixture","update":{"sessionUpdate":"user_message_chunk","content":{"type":"text","text":"wait"},"_meta":{"promptIndex":1}}},"timestamp":1785585600}`,
			deferred: 1,
		},
		{
			name: "user chunks without a prompt index remain one turn",
			stream: `{"method":"session/update","params":{"sessionId":"fixture","update":{"sessionUpdate":"user_message_chunk","content":{"type":"text","text":"split "}}},"timestamp":1785585600}` + "\n" +
				`{"method":"session/update","params":{"sessionId":"fixture","update":{"sessionUpdate":"user_message_chunk","content":{"type":"text","text":"question"}}},"timestamp":1785585600}` + "\n" +
				`{"method":"session/update","params":{"sessionId":"fixture","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"one answer"}}},"timestamp":1785585601}`,
			exchanges: 1,
		},
		{
			name: "an unindexed prompt after an answer opens the next turn",
			stream: `{"method":"session/update","params":{"sessionId":"fixture","update":{"sessionUpdate":"user_message_chunk","content":{"type":"text","text":"first question"}}},"timestamp":1785585600}` + "\n" +
				`{"method":"session/update","params":{"sessionId":"fixture","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"first answer"}}},"timestamp":1785585601}` + "\n" +
				`{"method":"session/update","params":{"sessionId":"fixture","update":{"sessionUpdate":"user_message_chunk","content":{"type":"text","text":"second question"}}},"timestamp":1785585602}` + "\n" +
				`{"method":"session/update","params":{"sessionId":"fixture","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"second answer"}}},"timestamp":1785585603}`,
			exchanges: 2,
		},
		{
			name:            "agent content before any prompt is a failure, not an exclusion",
			stream:          `{"method":"session/update","params":{"sessionId":"fixture","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"orphan answer"}}},"timestamp":1785585600}`,
			discardContains: "before any user prompt",
		},
		{
			name: "a malformed line does not cost the rest of the file",
			stream: "{not-json}\n" +
				`{"method":"session/update","params":{"sessionId":"fixture","update":{"sessionUpdate":"user_message_chunk","content":{"type":"text","text":"question"},"_meta":{"promptIndex":1}}},"timestamp":1785585600}` + "\n" +
				`{"method":"session/update","params":{"sessionId":"fixture","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"answer"}}},"timestamp":1785585601}`,
			exchanges: 1, discardContains: "invalid JSON",
		},
		{
			name:            "an unknown content update is unreadable",
			stream:          `{"method":"session/update","params":{"sessionId":"fixture","update":{"sessionUpdate":"future_semantic_surface"}},"timestamp":1785585600}`,
			discardContains: "unknown Grok content update",
		},
		{
			name:            "runtime machinery stays excluded",
			stream:          `{"method":"_x.ai/session/update","params":{"sessionId":"fixture","update":{"sessionUpdate":"turn_completed"}},"timestamp":1785585600}`,
			discardContains: "runtime update", discardByDesign: true,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			records, err := Parse(KindGrokSession, []byte(testCase.stream), FileMeta{SessionID: "fixture"})
			if err != nil {
				t.Fatal(err)
			}
			if got := len(records.Sessions[0].Exchanges); got != testCase.exchanges {
				t.Errorf("exchanges = %d, want %d", got, testCase.exchanges)
			}
			if records.Deferred != testCase.deferred {
				t.Errorf("deferred = %d, want %d", records.Deferred, testCase.deferred)
			}
			if testCase.discardContains == "" {
				return
			}
			found := false
			for _, discard := range records.Discards {
				if strings.Contains(discard.Reason, testCase.discardContains) {
					found = true
					if discard.ByDesign != testCase.discardByDesign {
						t.Errorf("discard = %+v", discard)
					}
				}
			}
			if !found {
				t.Errorf("discards = %+v, want %q", records.Discards, testCase.discardContains)
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
}

func TestGrokUpdateStreamTakesOnlyItsTitleFromTheSidecar(t *testing.T) {
	sidecar := `{
  "info": {"id": "another-id", "cwd": "/w/demo"},
  "created_at": "2026-08-01T14:00:00Z",
  "updated_at": "2026-08-01T14:00:30Z",
  "generated_title": "the ninth fixture"
}`
	records, err := Parse(KindGrokSession, []byte(grokUpdates), FileMeta{
		Path:    "/home/op/.grok/sessions/%2Fw%2Fdemo/path-session/updates.jsonl",
		Sidecar: []byte(sidecar),
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	session := records.Sessions[0]
	if session.ID != "path-session" {
		t.Errorf("session id = %q, want directory identity", session.ID)
	}
	if session.Title != "the ninth fixture" {
		t.Errorf("title = %q", session.Title)
	}
	if session.StartedAt == "2026-08-01T14:00:00Z" || session.EndedAt == "2026-08-01T14:00:30Z" {
		t.Errorf("primary stream took sidecar timestamps: %q..%q", session.StartedAt, session.EndedAt)
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
