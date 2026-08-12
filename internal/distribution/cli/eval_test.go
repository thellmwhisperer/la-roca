package cli

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/evaluation"
)

func TestEvalCommandRendersTheRecordedBaseline(t *testing.T) {
	tests := []struct {
		name   string
		args   []string
		marker string
		json   bool
	}{
		{"human", nil, "HEADROOM headroom-typo", false},
		{"markdown", []string{"--format", "markdown"}, "| hit@5 |", false},
		{"json", []string{"--json"}, `"mode": "replay"`, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var out, stderr bytes.Buffer
			args := append([]string{"eval", "--work-dir", filepath.Join(t.TempDir(), "eval")}, test.args...)
			code, err := execute(Build{Version: "test", Commit: "abc123"}, &out, &stderr, args)
			if err != nil || code != ExitOK {
				t.Fatalf("eval = code %d err %v stderr %q", code, err, stderr.String())
			}
			if !strings.Contains(out.String(), test.marker) {
				t.Fatalf("eval output lacks %q:\n%s", test.marker, out.String())
			}
			if test.json {
				var report evaluation.Report
				if err := json.Unmarshal(out.Bytes(), &report); err != nil {
					t.Fatalf("decode report: %v\n%s", err, out.String())
				}
				if report.Mode != "replay" || report.Metrics.Cases != 20 || report.Metrics.Passed != 17 {
					t.Fatalf("unexpected report: %+v", report)
				}
			}
		})
	}
}
