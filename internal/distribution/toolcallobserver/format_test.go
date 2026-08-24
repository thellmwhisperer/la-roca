package toolcallobserver

import (
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/pkg/parsers"
)

func TestFormatShowsShellOutputThroughObserveCalls(t *testing.T) {
	cases := []struct {
		name    string
		kind    parsers.Kind
		content string
	}{
		{"claude", parsers.KindClaudeSession, claudeShellLog},
		{"grok", parsers.KindGrokSession, grokShellLog},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			var lines []string
			for _, event := range parsers.ObserveCalls(test.kind, []byte(test.content)) {
				lines = append(lines, Format(event))
			}
			joined := strings.Join(lines, "\n")
			if !strings.Contains(joined, "output") {
				t.Fatalf("shell output is not first-class:\n%s", joined)
			}
			if strings.Contains(joined, "result") {
				t.Fatalf("shell result rendered generically:\n%s", joined)
			}
		})
	}
}

func TestFormatKeepsNonShellResultsCompactThroughObserveCalls(t *testing.T) {
	content := []byte(`{"type":"assistant","timestamp":"2026-08-01T10:00:01Z","message":{"content":[{"type":"tool_use","id":"r1","name":"Read","input":{"path":"/x"}}]}}
{"type":"user","timestamp":"2026-08-01T10:00:02Z","message":{"content":[{"type":"tool_result","tool_use_id":"r1","content":"secret file body"}]}}
`)
	var resultLine string
	for _, event := range parsers.ObserveCalls(parsers.KindClaudeSession, content) {
		if line := Format(event); strings.Contains(line, "Read result") {
			resultLine = line
		}
	}
	if resultLine == "" {
		t.Fatal("no compact result line for the non-shell tool")
	}
	if strings.Contains(resultLine, "secret file body") {
		t.Fatalf("non-shell result dumped output: %q", resultLine)
	}
}

func TestFormatShowsShellOutputFirstClassAndOtherCallsCompactly(t *testing.T) {
	cases := []struct {
		event parsers.CallEvent
		want  []string
		hide  []string
	}{
		{
			event: parsers.CallEvent{
				Timestamp: "2026-08-01T10:00:01Z", Name: "Bash",
				Command: "echo lab", ID: "c1",
			},
			want: []string{"2026-08-01 10:00:01", "shell", "echo lab"},
		},
		{
			event: parsers.CallEvent{
				Timestamp: "2026-08-01T10:00:02Z", ID: "c1",
				Output: "lab\n", IsResult: true, Name: "Bash", Command: "echo lab",
			},
			want: []string{"2026-08-01 10:00:02", "output", "lab"},
		},
		{
			event: parsers.CallEvent{
				Timestamp: "2026-08-01T10:00:03Z", Name: "Read",
				Params: `{"path":"/synthetic/lab/beacon.txt"}`,
			},
			want: []string{"2026-08-01 10:00:03", "Read", `{"path":"/synthetic/lab/beacon.txt"}`},
			hide: []string{"shell"},
		},
		{
			event: parsers.CallEvent{
				Timestamp: "2026-08-01T10:00:04Z", Name: "Bash", Command: "yes",
				Output: strings.Repeat("y\n", 400), IsResult: true,
			},
			want: []string{"[truncated]"},
		},
	}
	for _, test := range cases {
		got := Format(test.event)
		for _, fragment := range test.want {
			if !strings.Contains(got, fragment) {
				t.Errorf("format %q missing %q", got, fragment)
			}
		}
		for _, fragment := range test.hide {
			if strings.Contains(got, fragment) {
				t.Errorf("format %q unexpectedly contains %q", got, fragment)
			}
		}
	}
}
