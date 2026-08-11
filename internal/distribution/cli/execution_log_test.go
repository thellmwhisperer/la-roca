package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/thellmwhisperer/la-roca/internal/distribution/logfile"
	"github.com/thellmwhisperer/la-roca/internal/ingest"
	"github.com/thellmwhisperer/la-roca/internal/provider/service"
)

func TestExecutionLogCarriesMetadataWithoutResultRowsAndRedactsFlags(t *testing.T) {
	dataDir := t.TempDir()
	dbPath := filepath.Join(dataDir, "roca.db")
	env := &cliEnv{dbPath: dbPath, outcome: service.QueryResult{
		Question: "what changed", Path: service.PathLLM, Engine: "codex", Model: "model", RowCount: 2,
		Rows: []map[string]any{{"text": "private row contents"}},
	}}
	root := &cobra.Command{Use: "roca"}
	query := &cobra.Command{Use: "query"}
	root.AddCommand(query)
	query.Flags().String("api-token", "", "secret")
	if err := query.Flags().Set("api-token", "token-private-value"); err != nil {
		t.Fatal(err)
	}

	if err := env.logExecution(query, time.Now(), ExitOK, nil); err != nil {
		t.Fatal(err)
	}
	matches, err := filepath.Glob(filepath.Join(dataDir, logfile.DirName, "executions-*.jsonl"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("execution logs = %v, err=%v", matches, err)
	}
	raw, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, want := range []string{`"command":"query"`, `"database_path":"` + dbPath + `"`, `"question":"what changed"`, `"path":"llm_fallback"`, `"row_count":2`, `"api-token":"[REDACTED]"`} {
		if !strings.Contains(text, want) {
			t.Errorf("execution log lacks %q: %s", want, text)
		}
	}
	if strings.Contains(text, "token-private-value") {
		t.Fatalf("credential flag leaked: %s", text)
	}
	if strings.Contains(text, "private row contents") || strings.Contains(text, `"rows"`) {
		t.Fatalf("result rows leaked into the execution log: %s", text)
	}
}

func TestIngestExecutionAlsoPersistsTheDetailedIngestEnvelope(t *testing.T) {
	dataDir := t.TempDir()
	env := &cliEnv{
		dbPath: filepath.Join(dataDir, "roca.db"),
		outcome: service.IngestResult{Result: ingest.Result{
			RecordsDiscarded: 1,
			DiscardDetails: []ingest.DiscardDetail{{
				Path: "/source/session.jsonl", Parser: "claude_session", Record: 3, Reason: "invalid JSON",
			}},
		}},
	}
	root := &cobra.Command{Use: "roca"}
	command := &cobra.Command{Use: "ingest"}
	root.AddCommand(command)
	if err := env.logExecution(command, time.Now(), ExitOK, nil); err != nil {
		t.Fatal(err)
	}
	matches, err := filepath.Glob(filepath.Join(dataDir, logfile.DirName, "ingest-*.jsonl"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("ingest logs = %v, err=%v", matches, err)
	}
	raw, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"records_discarded":1`, `"parser":"claude_session"`, `"reason":"invalid JSON"`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("ingest log lacks %q: %s", want, raw)
		}
	}
}

// The trace is observability, and observability never fails the command.
//
// A log this run cannot write used to be returned as the run's error, so a query
// that had already printed its answer on stdout exited 1 and a script reading the
// exit code concluded the query failed while holding the answer. The command now
// keeps its own honest exit code and says once, on the error stream, that the log
// did not get written.
func TestAnUnwritableLogDoesNotFailTheCommand(t *testing.T) {
	fixtureInstallation(t)
	home := os.Getenv("HOME")

	// The log directory cannot be created because a regular file already holds
	// its name, which is the same failure as a read-only data directory.
	logs := filepath.Join(home, ".roca", logfile.DirName)
	if err := os.RemoveAll(logs); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logs, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}

	var out, errs strings.Builder
	code, err := execute(contractBuild(), &out, &errs,
		[]string{"query", "how many memories are there"})

	if err != nil {
		t.Errorf("a log that could not be written became the run's error: %v", err)
	}
	if code != ExitOK {
		t.Errorf("exit code = %d, want %d: the query itself answered", code, ExitOK)
	}
	if out.Len() == 0 {
		t.Error("the answer never reached stdout")
	}
	// One warning, on the error stream, and not on stdout where a program parses.
	if got := strings.Count(errs.String(), "warning:"); got != 1 {
		t.Errorf("want exactly one stderr warning, got %d:\n%s", got, errs.String())
	}
	if !strings.Contains(errs.String(), "log") {
		t.Errorf("the warning does not say what failed:\n%s", errs.String())
	}
	if strings.Contains(out.String(), "warning:") {
		t.Errorf("the warning leaked into stdout:\n%s", out.String())
	}
}
