package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/evaluation"
	"github.com/thellmwhisperer/la-roca/internal/provider"
	"github.com/thellmwhisperer/la-roca/internal/provider/config"
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

func TestEvalCommandNeverUsesOperatorState(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		wantError bool
	}{
		{name: "replay", args: []string{"eval"}},
		{name: "validation failure", args: []string{"eval", "--mode", "invalid"}, wantError: true},
		{name: "flag failure", args: []string{"eval", "--unknown"}, wantError: true},
		{name: "live provider failure", args: []string{"eval", "--mode", "live", "--provider", "unknown"}, wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			operatorDir := t.TempDir()
			configPath := filepath.Join(operatorDir, config.FileConfig)
			if err := os.WriteFile(configPath, []byte("malformed = ["), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv(config.EnvDBPath, filepath.Join(operatorDir, "operator.sqlite"))
			t.Setenv(config.EnvConfig, configPath)
			args := append(test.args, "--work-dir", filepath.Join(t.TempDir(), "eval"))
			var out, stderr bytes.Buffer
			code, err := execute(Build{Version: "test"}, &out, &stderr, args)
			if test.wantError != (err != nil) || test.wantError != (code == ExitError) {
				t.Fatalf("eval = code %d err %v stderr %q", code, err, stderr.String())
			}
			entries, err := os.ReadDir(operatorDir)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 1 || entries[0].Name() != config.FileConfig {
				t.Fatalf("eval touched operator state: %v", entries)
			}
		})
	}
}

func TestLiveEvalCanonicalizesProviderAndPreservesModel(t *testing.T) {
	providers, err := evaluationProviders(t.TempDir(), " CODEX ", "custom-eval-model")
	if err != nil {
		t.Fatal(err)
	}
	if len(providers.Providers) != 1 {
		t.Fatalf("providers = %d; want 1", len(providers.Providers))
	}
	want := struct{ name, model string }{provider.NameCodex, "custom-eval-model"}
	got := struct{ name, model string }{
		providers.Providers[0].Name(), providers.Providers[0].ModelID(),
	}
	if got != want {
		t.Fatalf("provider/model = %q/%q; want %q/%q", got.name, got.model, want.name, want.model)
	}
}
