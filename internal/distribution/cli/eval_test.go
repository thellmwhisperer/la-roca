package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/evaluation"
	"github.com/thellmwhisperer/la-roca/internal/provider"
	"github.com/thellmwhisperer/la-roca/internal/provider/config"
	"github.com/thellmwhisperer/la-roca/internal/store"
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
			workDir := filepath.Join(t.TempDir(), "eval")
			args := append([]string{"eval", "--work-dir", workDir}, test.args...)
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
			logs, err := filepath.Glob(filepath.Join(workDir, "logs", "eval-*.jsonl"))
			if err != nil || len(logs) != 1 {
				t.Fatalf("automatic eval log = %v, err=%v", logs, err)
			}
			raw, err := os.ReadFile(logs[0])
			if err != nil {
				t.Fatal(err)
			}
			var archive evaluation.Archive
			if err := json.Unmarshal(bytes.TrimSpace(raw), &archive); err != nil {
				t.Fatalf("decode automatic eval log: %v\n%s", err, raw)
			}
			if archive.Timestamp.IsZero() || archive.Mode != "replay" ||
				len(archive.PlanProducers) != 1 || len(archive.Report.Cases) != 20 ||
				archive.Formats.Human == "" || archive.Formats.Markdown == "" || archive.Formats.JSON == "" {
				t.Fatalf("automatic eval log is incomplete: %+v", archive)
			}
			if archive.Report.LogPath != logs[0] || !strings.Contains(out.String(), logs[0]) {
				t.Fatalf("output does not end with its log path %q:\n%s", logs[0], out.String())
			}
			saved := archive.Formats.Human
			if test.name == "markdown" {
				saved = archive.Formats.Markdown
			} else if test.name == "json" {
				saved = archive.Formats.JSON
			}
			if strings.TrimSuffix(out.String(), "\n") != saved {
				t.Fatalf("printed %s report differs from its automatic log", test.name)
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
	tests := []struct {
		name, asked, want string
	}{
		{"explicit model", "custom-eval-model", "custom-eval-model"},
		{"provider default", "", provider.DefaultCodexModel},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("ROCA_CODEX_MODEL", "operator-environment-model")
			t.Setenv("ROCA_MODEL", "operator-environment-model")
			providers, err := evaluationProviders(t.TempDir(), " CODEX ", test.asked)
			if err != nil {
				t.Fatal(err)
			}
			if len(providers.Providers) != 1 {
				t.Fatalf("providers = %d; want 1", len(providers.Providers))
			}
			want := struct{ name, model string }{provider.NameCodex, test.want}
			got := struct{ name, model string }{
				providers.Providers[0].Name(), providers.Providers[0].ModelID(),
			}
			if got != want {
				t.Fatalf("provider/model = %q/%q; want %q/%q",
					got.name, got.model, want.name, want.model)
			}
		})
	}
}

func TestEvalCommandUsesExplicitExternalCasesAndStrictReadOnlyDatabase(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "owner.sqlite")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ApplySchema(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureSearchSchema(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL().Exec(`INSERT INTO memories (layer, content, origin, project)
		VALUES ('discovery', 'Ada owns Quartz', 'human', 'quartz')`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dbPath, 0o644); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	casesPath := filepath.Join(dir, "owner-cases.json")
	cases := `{"schema_version":1,"fixture":"owner-private","cases":[{"id":"private-person","category":"person","question":"Who owns Quartz?","expected_kind":"row_contains","expected_marker":"Ada owns Quartz"}]}`
	plans := `{"provider":"recorded","model":"owner-v1","plans":[{"case_id":"private-person","sql":["SELECT content FROM memories WHERE project = 'quartz' LIMIT 5"]}]}`
	if err := os.WriteFile(casesPath, []byte(cases), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(evaluation.RecordedPlansPath(casesPath), []byte(plans), 0o600); err != nil {
		t.Fatal(err)
	}

	workDir := filepath.Join(dir, "eval-work")
	var out, stderr bytes.Buffer
	code, err := execute(Build{Version: "test"}, &out, &stderr,
		[]string{"eval", "--cases", casesPath, "--db", dbPath, "--work-dir", workDir, "--json"})
	if err != nil || code != ExitOK {
		t.Fatalf("external eval = code %d err %v stderr %q", code, err, stderr.String())
	}
	var report evaluation.Report
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("decode external report: %v\n%s", err, out.String())
	}
	if report.Fixture != "owner-private" || report.Metrics.Passed != 1 ||
		len(report.Cases) != 1 || len(report.Cases[0].Attempts[0].ResultRows) != 1 {
		t.Fatalf("external report lost source data: %+v", report)
	}
	after, err := os.ReadFile(dbPath)
	info, statErr := os.Stat(dbPath)
	if err != nil || statErr != nil || !bytes.Equal(before, after) || info.Mode().Perm() != 0o644 {
		t.Fatalf("external database changed: read=%v stat=%v mode=%v", err, statErr, info)
	}

	if err := os.Remove(evaluation.RecordedPlansPath(casesPath)); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			_, _ = w.Write([]byte(`{"models":[{"name":"owner-live","model":"owner-live"}]}`))
		case "/api/chat":
			_, _ = w.Write([]byte(`{"message":{"content":"SELECT content FROM memories WHERE project = 'quartz' LIMIT 5"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	t.Setenv("ROCA_OLLAMA_BASE_URL", server.URL)
	out.Reset()
	stderr.Reset()
	code, err = execute(Build{Version: "test"}, &out, &stderr, []string{
		"eval", "--mode", "live", "--provider", "ollama", "--model", "owner-live",
		"--cases", casesPath, "--db", dbPath, "--work-dir", filepath.Join(dir, "live-work"), "--json",
	})
	if err != nil || code != ExitOK {
		t.Fatalf("live external eval = code %d err %v stderr %q", code, err, stderr.String())
	}
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Mode != "live" || len(report.Producers) != 1 ||
		report.Producers[0].Provider != "ollama" || report.Producers[0].Model != "owner-live" {
		t.Fatalf("live external producer labels = %+v", report)
	}
}

func TestEvalCommandRequiresExternalCasesAndDatabaseTogether(t *testing.T) {
	for _, args := range [][]string{{"eval", "--cases", "cases.json"}, {"eval", "--db", "roca.db"}} {
		var out, stderr bytes.Buffer
		code, err := execute(Build{Version: "test"}, &out, &stderr, args)
		if err == nil || code != ExitError || !strings.Contains(err.Error(), "--cases and --db together") {
			t.Fatalf("eval %v = code %d err %v", args, code, err)
		}
	}
}
