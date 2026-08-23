package cli

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/distribution/rocacorpus"
)

// TestInitProvesWordSearchOnHistoryItJustRead is the first-run promise: the
// command does not return until a word taken from this machine's own history
// has been asked back of the index and found.
func TestInitProvesWordSearchOnHistoryItJustRead(t *testing.T) {
	_, dbPath := deepSearchHome(t)

	run := initMustSucceed(t, "init", "--db-path", dbPath)
	if !strings.Contains(run.output, "word search: ready ·") {
		t.Fatalf("init did not prove word search on a machine that has history:\n%s", run.output)
	}
	if !strings.Contains(run.output, "and found it in") {
		t.Errorf("the proof does not say where the word was found:\n%s", run.output)
	}
}

func TestInitRejectsRemovedVectorsFlagWithoutInitializationSideEffects(t *testing.T) {
	home := hermeticHome(t)
	var output, warnings strings.Builder
	env := hermeticCLIEnv(&cliEnv{build: Build{Version: "test"}, out: &output, errOut: &warnings,
		bundledVectorPayload: []byte("synthetic payload")})
	code, err := executeWithEnv(env, []string{"init", "--vectors"}, nil)
	if err == nil || code == ExitOK || !strings.Contains(err.Error(), "unknown flag: --vectors") {
		t.Fatalf("removed flag = code %d err %v output %q", code, err, output.String()+warnings.String())
	}
	for _, path := range []string{
		filepath.Join(home, ".roca", "roca.db"),
		filepath.Join(home, ".roca", "config.toml"),
		filepath.Join(home, ".local", "bin", "roca-vector"),
		filepath.Join(home, ".roca", "plugins", "roca-vector", "plugin.json"),
	} {
		if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
			t.Fatalf("removed flag created %s: %v", path, statErr)
		}
	}
}

// TestInitSaysNothingToSearchYetRatherThanBroken keeps the empty machine out of
// the failure wording: no history is a fact about the machine, not a fault in
// the index.
func TestInitSaysNothingToSearchYetRatherThanBroken(t *testing.T) {
	home := hermeticHome(t)

	out := runRoot(t, Build{Version: "test", Commit: "test-sha"},
		"init", "--db-path", filepath.Join(home, ".roca", "roca.db"))
	if !strings.Contains(out, "word search: nothing to search yet") {
		t.Fatalf("init on an empty machine does not name the empty state:\n%s", out)
	}
	if strings.Contains(out, "word search: did not answer") {
		t.Errorf("init called an empty machine a broken index:\n%s", out)
	}
}

func TestInitRepairsAnEmptiedWordIndexBeforeReturning(t *testing.T) {
	home, dbPath := deepSearchHome(t)
	initMustSucceed(t, "init", "--db-path", dbPath)
	corpusPath := filepath.Join(home, ".roca", "plugins", rocacorpus.Name, rocacorpus.DatabaseFilename)
	emptyWordIndex(t, corpusPath, false)

	run := initMustSucceed(t, "init", "--db-path", dbPath)
	if !strings.Contains(run.output, "word search: ready ·") {
		t.Fatalf("init did not restore word search before returning:\n%s", run.output)
	}
}

func TestInitFailsWithOneNextStepWhenWordIndexRepairFails(t *testing.T) {
	home, dbPath := deepSearchHome(t)
	initMustSucceed(t, "init", "--db-path", dbPath)
	corpusPath := filepath.Join(home, ".roca", "plugins", rocacorpus.Name, rocacorpus.DatabaseFilename)
	emptyWordIndex(t, corpusPath, true)

	run := executeHermeticCLI([]string{"init", "--db-path", dbPath})
	if run.err == nil || run.code == ExitOK {
		t.Fatalf("init succeeded with an index it could not repair:\n%s%s", run.output, run.warnings)
	}
	report := run.output + run.warnings + run.err.Error()
	if !strings.Contains(report, "word search is not working after one rebuild") {
		t.Fatalf("init did not give a plain repair failure: %s", report)
	}
	if count := strings.Count(report, "next step:"); count != 1 {
		t.Fatalf("init printed %d next steps, want one: %s", count, report)
	}
}

func emptyWordIndex(t *testing.T, path string, refuseRepair bool) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, table := range []string{"memories_fts", "exchanges_fts", "thinking_fts", "sessions_fts"} {
		if _, err := db.Exec(`INSERT INTO ` + table + `(` + table + `) VALUES ('delete-all')`); err != nil {
			t.Fatal(err)
		}
	}
	if refuseRepair {
		if _, err := db.Exec(`CREATE TRIGGER refuse_word_index_repair BEFORE DELETE ON search_state
			BEGIN SELECT RAISE(ABORT, 'synthetic repair refusal'); END`); err != nil {
			t.Fatal(err)
		}
	}
}

func deepSearchHome(t *testing.T) (string, string) {
	t.Helper()
	home := hermeticHome(t)
	claudeRoot := filepath.Join(home, "sources", "claude")
	writeConfig(t, home, fmt.Sprintf("[defaults]\nclaude_projects_root = %q\nworkspace_roots = [%q]\n",
		claudeRoot, filepath.Join(home, "workspace")))
	writeFile(t, filepath.Join(claudeRoot, "-synthetic-demo", "66666666-6666-6666-6666-666666666666.jsonl"),
		`{"type":"user","timestamp":"2026-08-01T10:00:00Z","cwd":"/synthetic/demo","message":{"content":"how did we settle the harbour lighting question"}}
{"type":"assistant","timestamp":"2026-08-01T10:00:01Z","message":{"content":[{"type":"text","text":"we kept the harbour lighting on a separate circuit"}]}}
`)
	return home, filepath.Join(home, ".roca", "roca.db")
}
