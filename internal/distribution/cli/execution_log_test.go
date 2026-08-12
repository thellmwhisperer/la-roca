package cli

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/thellmwhisperer/la-roca/internal/distribution/logfile"
	"github.com/thellmwhisperer/la-roca/internal/ingest"
	"github.com/thellmwhisperer/la-roca/internal/provider"
	"github.com/thellmwhisperer/la-roca/internal/provider/service"
)

func TestExecutionLogCarriesMetadataWithoutResultRowsAndRedactsFlags(t *testing.T) {
	dataDir := t.TempDir()
	dbPath := filepath.Join(dataDir, "roca.db")
	rawSQL := "<think>shape it</think>\n```sql\n" + queryModeSQL + "\n```"
	frontier := &queryModeProvider{
		answers: []string{"SELECT missing FROM memories LIMIT 1", rawSQL},
		name:    "codex", model: "gpt-frontier",
	}
	local := &queryModeProvider{answers: []string{queryModeProse}, name: "ollama", model: "qwen-local"}
	answer, err := answerQuery(t.Context(), queryModeServiceWithTimeout(t, frontier, 0, local),
		service.QueryRequest{Question: queryModeQuestion}, true)
	if err != nil {
		t.Fatal(err)
	}
	answer.result.Rows[0]["text"] = "private row contents"
	env := &cliEnv{dbPath: dbPath, outcome: answer.result, auditQuery: &answer.result}
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
	for _, want := range []string{`"source":"cli"`, `"command":"query"`,
		`"ok":true`, `"database_path":"` + dbPath + `"`,
		`"question":"` + queryModeQuestion + `"`, `"sql":"` + queryModeSQL + `"`,
		`"path":"model"`, `"row_count":1`,
		`"retried_sql":true`, `"first_model_sql":"SELECT missing FROM memories LIMIT 1"`,
		`"retry_reason":"no such column:`, `missing`, `"sql_retry_inference_ms":`,
		`"sql_retry_provider_latency_ms":`,
		`"sql_provider":"codex"`, `"sql_model":"gpt-frontier"`, `"sql_inference_ms":`,
		`"execution_ms":`, `"interpretation_provider":"ollama"`,
		`"interpretation_model":"qwen-local"`, `"interpretation_ms":`,
		`"api-token":"[REDACTED]"`} {
		if !strings.Contains(text, want) {
			t.Errorf("execution log lacks %q: %s", want, text)
		}
	}
	if strings.Contains(text, "token-private-value") {
		t.Fatalf("credential flag leaked: %s", text)
	}
	if strings.Contains(text, "private row contents") || strings.Contains(text, `"rows"`) || strings.Contains(text, `"interpretation"`) {
		t.Fatalf("result rows leaked into the execution log: %s", text)
	}
	var record struct {
		Args   []string                   `json:"args"`
		RawSQL string                     `json:"raw_sql"`
		Result map[string]json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatal(err)
	}
	if record.Args == nil || len(record.Args) != 0 || record.RawSQL != rawSQL {
		t.Fatalf("args/raw SQL = %#v / %q, want empty args and exact provider output", record.Args, record.RawSQL)
	}
	for _, obsolete := range []string{"engine", "model", "interpret_engine", "interpret_model"} {
		if _, exists := record.Result[obsolete]; exists {
			t.Errorf("obsolete envelope key %q returned: %s", obsolete, raw)
		}
	}
}

func TestUnexpectedCLIErrorIsDurableAndUserVisibleByCorrelationID(t *testing.T) {
	fixtureInstallation(t)
	home := os.Getenv("HOME")
	var out, errs strings.Builder
	code, runErr := execute(contractBuild(), &out, &errs,
		[]string{"exec", "SELECT * FROM synthetic_missing_table"})
	if code != ExitError || runErr == nil || !strings.Contains(runErr.Error(), "correlation_id") {
		t.Fatalf("failure = code %d err %v, want a correlated user-visible error", code, runErr)
	}
	matches, err := filepath.Glob(filepath.Join(home, ".roca", logfile.DirName, "executions-*.jsonl"))
	if err != nil || len(matches) == 0 {
		t.Fatalf("execution logs = %v, err=%v", matches, err)
	}
	raw, err := os.ReadFile(matches[len(matches)-1])
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"source":"cli"`, `"command":"exec"`, `"ok":false`, `"error_type":"`,
		`"correlation_id":"`, "synthetic_missing_table",
	} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("execution failure lacks %q: %s", want, raw)
		}
	}
	var record struct {
		CorrelationID string `json:"correlation_id"`
	}
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatal(err)
	}
	if record.CorrelationID == "" || !strings.Contains(runErr.Error(), record.CorrelationID) {
		t.Fatalf("screen error %q does not match audit correlation %q", runErr, record.CorrelationID)
	}
}

func TestADegradedQueryNamesItsAuditLineWithoutAnError(t *testing.T) {
	fixtureInstallation(t)
	home := os.Getenv("HOME")
	writeConfig(t, home, "[models]\nprobe_ms = 200\n\n"+
		"[models.mycorp]\nbase_url = \"https://llm.invalid/v1\"\napi_key = \"sk-synthetic\"\n"+
		"model = \"internal-7b\"\n")
	t.Setenv(provider.EnvOrder, "mycorp")

	var out, errs strings.Builder
	code, runErr := execute(contractBuild(), &out, &errs,
		[]string{"query", "how many synthetic memories are there"})
	if runErr != nil {
		t.Fatalf("a degraded answer became a program error: %v", runErr)
	}
	if code != ExitError {
		t.Fatalf("exit code = %d, want %d: the question needed a model and had none", code, ExitError)
	}
	matches, err := filepath.Glob(filepath.Join(home, ".roca", logfile.DirName, "executions-*.jsonl"))
	if err != nil || len(matches) == 0 {
		t.Fatalf("execution logs = %v, err=%v", matches, err)
	}
	raw, err := os.ReadFile(matches[len(matches)-1])
	if err != nil {
		t.Fatal(err)
	}
	var record struct {
		OK            bool   `json:"ok"`
		ErrorType     string `json:"error_type"`
		CorrelationID string `json:"correlation_id"`
	}
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatal(err)
	}
	if record.OK || record.ErrorType != service.DegradedUnavailable {
		t.Fatalf("audit record = %+v, want a failed %s call", record, service.DegradedUnavailable)
	}
	if record.CorrelationID == "" {
		t.Fatalf("a degraded run left no correlation id in its audit record: %s", raw)
	}
	if !strings.Contains(errs.String(), record.CorrelationID) {
		t.Fatalf("the run does not name its audit line %q on the error stream:\n%s",
			record.CorrelationID, errs.String())
	}
	if strings.Contains(out.String(), record.CorrelationID) {
		t.Fatalf("the correlation id landed in the answer itself:\n%s", out.String())
	}
}

func TestIngestAndMigrationRunsPersistSummariesAndErrorsOutsideSQLite(t *testing.T) {
	tests := []struct {
		name, command, stream string
		outcome               any
		code                  int
		runErr                error
		want                  []string
	}{
		{
			name: "ingest partial failure", command: "ingest", stream: logfile.Ingest,
			outcome: service.IngestResult{Result: ingest.Result{
				RecordsDiscarded: 1,
				DiscardDetails: []ingest.DiscardDetail{{
					Path: "/synthetic/session.jsonl", Parser: "claude_session", Record: 3, Reason: "invalid JSON",
				}, {
					Path: "/synthetic/runtime.jsonl", Parser: "codex_session", Record: 4,
					Reason: "runtime event", ByDesign: true,
				}},
			}},
			code: ExitError, runErr: errors.New("synthetic ingest interrupted"),
			want: []string{`"ok":false`, `"error":"synthetic ingest interrupted"`,
				`"records_discarded":1`, `"parser":"claude_session"`,
				`"reason":"invalid JSON"`, `"by_design":true`},
		},
		{
			name: "schema adoption", command: "init", stream: logfile.Migrations,
			outcome: service.InitResult{Database: "adopted", Verdict: "current",
				Repairs: []string{"add synthetic nullable provenance column"}},
			code: ExitOK,
			want: []string{`"ok":true`, `"database":"adopted"`, `"verdict":"current"`,
				`"repairs":["add synthetic nullable provenance column"]`},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dataDir := t.TempDir()
			env := &cliEnv{dbPath: filepath.Join(dataDir, "roca.db"), outcome: test.outcome}
			root := &cobra.Command{Use: "roca"}
			command := &cobra.Command{Use: test.command}
			root.AddCommand(command)
			if err := env.logExecution(command, time.Now(), test.code, test.runErr); err != nil {
				t.Fatal(err)
			}
			matches, err := filepath.Glob(filepath.Join(dataDir, logfile.DirName, test.stream+"-*.jsonl"))
			if err != nil || len(matches) != 1 {
				t.Fatalf("%s logs = %v, err=%v", test.stream, matches, err)
			}
			raw, err := os.ReadFile(matches[0])
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range test.want {
				if !strings.Contains(string(raw), want) {
					t.Errorf("%s log lacks %q: %s", test.stream, want, raw)
				}
			}
		})
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
