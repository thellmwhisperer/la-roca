package toolcallobserver

import (
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/pkg/parsers"
)

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
