package parsers

import "testing"

func TestObserveCallsReadsShellCommandsFromClaudeAndGrokFixtures(t *testing.T) {
	claude := []byte(`{"type":"assistant","timestamp":"2026-08-01T10:00:01Z","message":{"content":[{"type":"tool_use","id":"call-1","name":"Bash","input":{"command":"ls /synthetic/lab"}}]}}
{"type":"user","timestamp":"2026-08-01T10:00:02Z","message":{"content":[{"type":"tool_result","tool_use_id":"call-1","content":"beacon.txt\n"}]}}
{"type":"assistant","timestamp":"2026-08-01T10:00:03Z","message":{"content":[{"type":"tool_use","id":"call-2","name":"Read","input":{"path":"/synthetic/lab/beacon.txt"}}]}}
`)
	grok := []byte(grokUpdates)

	cases := []struct {
		kind     Kind
		content  []byte
		wantName string
		command  string
		output   string
	}{
		{KindClaudeSession, claude, "Bash", "ls /synthetic/lab", "beacon.txt\n"},
		{KindGrokSession, grok, "run_terminal_command", "make fixture", `{"message":"synthetic failure"}`},
	}
	for _, test := range cases {
		events := ObserveCalls(test.kind, test.content)
		var call, result CallEvent
		for _, event := range events {
			if event.Name == test.wantName && !event.IsResult {
				call = event
			}
			if event.ID != "" && event.ID == call.ID && event.IsResult {
				result = event
			}
		}
		if call.Name != test.wantName {
			t.Fatalf("%s: missing %s call in %#v", test.kind, test.wantName, events)
		}
		if call.Command != test.command {
			t.Errorf("%s command = %q, want %q", test.kind, call.Command, test.command)
		}
		if result.Name != test.wantName {
			t.Errorf("%s result name = %q, want %q", test.kind, result.Name, test.wantName)
		}
		if result.Command != test.command {
			t.Errorf("%s result command = %q, want %q", test.kind, result.Command, test.command)
		}
		if result.Output != test.output {
			t.Errorf("%s output = %q, want %q", test.kind, result.Output, test.output)
		}
	}
}

func TestObserveCallsIgnoresUnknownKinds(t *testing.T) {
	if events := ObserveCalls(KindClaudeMemory, []byte("not a session")); len(events) != 0 {
		t.Fatalf("unknown kind events = %#v", events)
	}
}

func TestObserveCallsEmitsPiBashAsCommandThenResult(t *testing.T) {
	content := []byte(`{"type":"message","id":"m1","timestamp":"2026-08-01T10:00:01Z","message":{"role":"bashExecution","command":"echo hi","output":"hi\n"}}` + "\n")
	events := ObserveCalls(KindPiSession, content)
	if len(events) != 2 {
		t.Fatalf("events = %#v, want a call and a result", events)
	}
	call, result := events[0], events[1]
	if call.IsResult || call.Name != "bash" || call.Command != "echo hi" {
		t.Fatalf("call = %#v", call)
	}
	if !result.IsResult || result.Name != "bash" || result.Command != "echo hi" || result.Output != "hi" {
		t.Fatalf("result = %#v", result)
	}
}
