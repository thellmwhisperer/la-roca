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

func TestExecutionLogCarriesTheExistingEnvelopeAndRedactedFlags(t *testing.T) {
	dataDir := t.TempDir()
	dbPath := filepath.Join(dataDir, "roca.db")
	env := &cliEnv{dbPath: dbPath, outcome: service.QueryResult{
		Path: service.PathLLM, Engine: "codex", Model: "model", RowCount: 2,
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
	for _, want := range []string{`"command":"query"`, `"database_path":"` + dbPath + `"`, `"path":"llm_fallback"`, `"row_count":2`, `"api-token":"[REDACTED]"`} {
		if !strings.Contains(text, want) {
			t.Errorf("execution log lacks %q: %s", want, text)
		}
	}
	if strings.Contains(text, "token-private-value") {
		t.Fatalf("credential flag leaked: %s", text)
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
