package cli

import (
	"context"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/distribution/rocacorpus"
	"github.com/thellmwhisperer/la-roca/internal/ingest"
	"github.com/thellmwhisperer/la-roca/internal/provider/service"
	"github.com/thellmwhisperer/la-roca/internal/store/exactdedup"
)

func TestIngestExitsNonZeroWhenAWriteFails(t *testing.T) {
	env := &cliEnv{}
	applyIngestExitCode(env, service.IngestResult{Result: ingest.Result{WriteFailed: 1, Errors: 1}})
	if env.code != ExitError {
		t.Fatalf("code = %d, want %d", env.code, ExitError)
	}
}

func TestIngestKeepsSuccessWhenOnlyAReadFailed(t *testing.T) {
	env := &cliEnv{}
	applyIngestExitCode(env, service.IngestResult{Result: ingest.Result{Errors: 1}})
	if env.code != ExitOK {
		t.Fatalf("code = %d, want %d", env.code, ExitOK)
	}
}

func TestDirectIngestReportsAnIsolatedWriteFailureAndExitsNonZero(t *testing.T) {
	home, claudeRoot, grokRoot, dbPath := initializedIngestCLI(t)
	corpusPath := filepath.Join(home, ".roca", "plugins", rocacorpus.Name, rocacorpus.DatabaseFilename)
	db := openLayoutDatabase(t, corpusPath)
	defer db.Close()
	if err := exactdedup.EnsureGuards(context.Background(), db); err != nil {
		t.Fatal(err)
	}

	project := filepath.Join(claudeRoot, "-synthetic-demo")
	metadataBody := `{"type":"user","timestamp":"2026-08-01T10:00:00Z","cwd":"/synthetic/demo","message":{"content":"metadata question"}}
{"type":"assistant","timestamp":"2026-08-01T10:00:01Z","message":{"content":[{"type":"text","text":"metadata answer"}]}}
`
	writeFailureBody := `{"type":"user","timestamp":"2026-08-01T11:00:00Z","message":{"content":"write question"}}
{"type":"assistant","timestamp":"2026-08-01T11:00:01Z","message":{"content":[{"type":"text","text":"write answer"}]}}
`
	writeFile(t, filepath.Join(project, "11111111-1111-1111-1111-111111111111.jsonl"), metadataBody)
	writeFile(t, filepath.Join(project, "22222222-2222-2222-2222-222222222222.jsonl"), metadataBody)
	writeFile(t, filepath.Join(project, "33333333-3333-3333-3333-333333333333.jsonl"), writeFailureBody)
	writeFile(t, filepath.Join(project, "44444444-4444-4444-4444-444444444444.jsonl"), writeFailureBody)
	seedLaterGrokFixture(t, grokRoot)

	run := executeHermeticCLI([]string{"ingest", "--db-path", dbPath})
	if run.err != nil {
		t.Fatalf("ingest returned an execution error instead of its report: %v\n%s%s",
			run.err, run.output, run.warnings)
	}
	if run.code != ExitError {
		t.Fatalf("ingest exit = %d, want %d:\n%s%s", run.code, ExitError, run.output, run.warnings)
	}
	for _, want := range []string{"skipped: write failed · 1", "✓ grok", "1 error"} {
		if !strings.Contains(run.output, want) {
			t.Errorf("ingest report lacks %q:\n%s", want, run.output)
		}
	}

	var claudeSessions, grokSessions, exactGuards, metadataPreserved int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE source_agent = 'claude'`).Scan(&claudeSessions); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE source_agent = 'grok'`).Scan(&grokSessions); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_sessions_exact_payload'`).Scan(&exactGuards); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM sessions
		WHERE session_id = '22222222-2222-2222-2222-222222222222'
		  AND json_extract(metadata, '$.cwd') IS NULL`).Scan(&metadataPreserved); err != nil {
		t.Fatal(err)
	}
	if claudeSessions != 3 || grokSessions != 1 || exactGuards != 1 || metadataPreserved != 1 {
		t.Fatalf("persisted state: claude=%d grok=%d exact_guard=%d metadata_preserved=%d, want 3/1/1/1",
			claudeSessions, grokSessions, exactGuards, metadataPreserved)
	}
	t.Logf("direct CLI exit=%d; persisted sessions claude=%d grok=%d; exact guard present=%t; colliding metadata unchanged=%t\n%s",
		run.code, claudeSessions, grokSessions, exactGuards == 1, metadataPreserved == 1, run.output)
}

func TestDirectIngestKeepsAReadFailureNonFatal(t *testing.T) {
	home, claudeRoot, _, dbPath := initializedIngestCLI(t)
	bad := filepath.Join(claudeRoot, "-synthetic-demo",
		"66666666-6666-6666-6666-666666666666", "subagents", "foreign.jsonl")
	writeFile(t, bad, `{"event":"turn","payload":{"role":"user","text":"another shape"}}`+"\n")

	run := executeHermeticCLI([]string{"ingest", "--db-path", dbPath})
	if run.err != nil || run.code != ExitOK {
		t.Fatalf("read-only failure = code %d err %v, want a non-fatal report:\n%s%s",
			run.code, run.err, run.output, run.warnings)
	}
	for _, want := range []string{"1 error", "skipped: read or parse failed · 1", "error: foreign.jsonl"} {
		if !strings.Contains(run.output, want) {
			t.Errorf("ingest report lacks %q:\n%s", want, run.output)
		}
	}

	corpusPath := filepath.Join(home, ".roca", "plugins", rocacorpus.Name, rocacorpus.DatabaseFilename)
	db := openLayoutDatabase(t, corpusPath)
	defer db.Close()
	var recorded int
	if err := db.QueryRow(`SELECT COUNT(*) FROM ingest_file_state WHERE last_error IS NOT NULL`).Scan(&recorded); err != nil {
		t.Fatal(err)
	}
	if recorded != 1 {
		t.Fatalf("persisted read failures = %d, want 1", recorded)
	}
	t.Logf("direct CLI exit=%d; persisted read failures=%d\n%s", run.code, recorded, run.output)
}

func initializedIngestCLI(t *testing.T) (home, claudeRoot, grokRoot, dbPath string) {
	t.Helper()
	home = hermeticHome(t)
	claudeRoot = filepath.Join(home, "sources", "claude")
	grokRoot = filepath.Join(home, "sources", "grok")
	writeConfig(t, home, fmt.Sprintf("[defaults]\nclaude_projects_root = %q\ngrok_sessions_root = %q\nworkspace_roots = [%q]\n",
		claudeRoot, grokRoot, filepath.Join(home, "workspace")))
	dbPath = filepath.Join(home, ".roca", "roca.db")

	initMustSucceed(t, "init", "--db-path", dbPath)
	return home, claudeRoot, grokRoot, dbPath
}

type cliExecution struct {
	code             int
	err              error
	output, warnings string
}

func executeHermeticCLI(args []string) cliExecution {
	var output, warnings strings.Builder
	env := hermeticCLIEnv(&cliEnv{build: Build{Version: "test", Commit: "test-sha"},
		out: &output, errOut: &warnings})
	code, err := executeWithEnv(env, args, nil)
	return cliExecution{code: code, err: err, output: output.String(), warnings: warnings.String()}
}

func seedLaterGrokFixture(t *testing.T, root string) {
	t.Helper()
	const sessionID = "55555555-5555-5555-5555-555555555555"
	session := filepath.Join(root, url.PathEscape("/synthetic/later"), sessionID)
	writeFile(t, filepath.Join(session, "summary.json"), `{
  "info":{"id":"55555555-5555-5555-5555-555555555555","cwd":"/synthetic/later"},
  "created_at":"2026-08-01T12:00:00Z","updated_at":"2026-08-01T12:00:02Z"
}`)
	writeFile(t, filepath.Join(session, "updates.jsonl"), `
{"method":"session/update","params":{"sessionId":"55555555-5555-5555-5555-555555555555","update":{"sessionUpdate":"user_message_chunk","content":{"type":"text","text":"later question"}}},"timestamp":1785585601}
{"method":"session/update","params":{"sessionId":"55555555-5555-5555-5555-555555555555","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"later answer"}}},"timestamp":1785585602}
`)
}
