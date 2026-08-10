package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/store"
)

func TestInitAsksForNewOrAUserSuppliedAdoptionPath(t *testing.T) {
	t.Run("new", func(t *testing.T) {
		home := initChoiceHome(t)
		out, err := runInitChoice(t, true, "new\n", "init")
		if err != nil {
			t.Fatalf("init: %v\n%s", err, out)
		}
		database := filepath.Join(home, ".roca", "roca.db")
		assertTranscriptLines(t, "new", out,
			"no database at "+database,
			"new: create an empty database here, then index the agent history found on this machine",
			"adopt: if you already have a La Roca database elsewhere, type its path and a copy is brought here; the original is never touched",
			"schema: 17 required structures created",
		)
		assertTranscript(t, "new", out, "[new/adopt]", "created a fresh database", "index")
		assertSchemaLine(t, "new", out, "schema: 17 required structures created")
		if _, err := os.Stat(database); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("adopt", func(t *testing.T) {
		home := initChoiceHome(t)
		source := seedCandidate(t, filepath.Join(home, "import", "candidate.db"))
		before, err := os.ReadFile(source)
		if err != nil {
			t.Fatal(err)
		}
		out, err := runInitChoice(t, true, "adopt\n"+source+"\n", "init")
		if err != nil {
			t.Fatalf("init: %v\n%s", err, out)
		}
		assertTranscript(t, "adopt", out,
			"[new/adopt]", "Path to the database to adopt", source, "bytes",
			"original stays untouched", "adopted by copy", filepath.Join(home, ".roca", "roca.db"),
		)
		after, err := os.ReadFile(source)
		if err != nil {
			t.Fatal(err)
		}
		if string(after) != string(before) {
			t.Fatal("the source database changed")
		}
	})
}

func TestInitOffersToKeepOrReinitializeItsHomeDatabase(t *testing.T) {
	home := initChoiceHome(t)
	own := filepath.Join(home, ".roca", "roca.db")
	if out, err := runInitChoice(t, false, "", "init", "--db-path", own); err != nil {
		t.Fatalf("seed init: %v\n%s", err, out)
	}
	out, err := runInitChoice(t, true, "keep\n", "init")
	if err != nil {
		t.Fatalf("keep init: %v\n%s", err, out)
	}
	assertTranscript(t, "keep", out,
		"[keep/reinitialize]", "kept the existing home database")
	assertTranscriptLines(t, "keep", out,
		"keep: use the current database here, then index the agent history found on this machine",
		"reinitialize: permanently replace the current database with an empty one, then index the agent history found on this machine",
		"schema: 17 required structures verified",
	)
	assertSchemaLine(t, "keep", out, "schema: 17 required structures verified")
}

func TestInitNamesAnActualSchemaRepair(t *testing.T) {
	home := initChoiceHome(t)
	own := seedCandidate(t, filepath.Join(home, ".roca", "roca.db"))
	db, err := store.Open(own)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL().Exec(`DROP INDEX idx_memories_layer`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	out, err := runInitChoice(t, true, "keep\n", "init")
	if err != nil {
		t.Fatalf("repair init: %v\n%s", err, out)
	}
	assertSchemaLine(t, "repair", out,
		"schema: 17 required structures verified; repairs applied (1): the index is missing: idx_memories_layer")
}

func TestInitRefusesWithoutATerminalOrAnExplicitLocation(t *testing.T) {
	home := initChoiceHome(t)
	out, err := runInitChoice(t, false, "new\n", "init")
	if err == nil {
		t.Fatalf("non-TTY init silently chose a database:\n%s", out)
	}
	for _, want := range []string{"interactive", "--db-path", "new or adopt"} {
		if !strings.Contains(out+err.Error(), want) {
			t.Errorf("refusal does not contain %q: %v\n%s", want, err, out)
		}
	}
	assertNoHomeDatabase(t, home, "non-TTY refusal")
}

func TestACommandWithoutADatabaseSaysToRunInitAndCreatesNothing(t *testing.T) {
	home := initChoiceHome(t)
	out, err := runInitChoice(t, false, "", "query", "who", "is", "Ana")
	if err == nil {
		t.Fatalf("query opened a database that init never created:\n%s", out)
	}
	if !strings.Contains(out+err.Error(), "run `roca init`") {
		t.Fatalf("missing run-init remedy: %v\n%s", err, out)
	}
	assertNoHomeDatabase(t, home, "query")
}

func assertTranscript(t *testing.T, label, transcript string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(transcript, want) {
			t.Errorf("%s transcript does not contain %q:\n%s", label, want, transcript)
		}
	}
}

func assertTranscriptLines(t *testing.T, label, transcript string, wants ...string) {
	t.Helper()
	lines := strings.Split(transcript, "\n")
	for _, want := range wants {
		found := false
		for _, line := range lines {
			if strings.TrimSpace(line) == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%s transcript does not contain the exact line %q:\n%s", label, want, transcript)
		}
	}
}

func assertSchemaLine(t *testing.T, label, transcript, want string) {
	t.Helper()
	for _, line := range strings.Split(transcript, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "schema:") {
			if strings.TrimSpace(line) != want {
				t.Errorf("%s schema line = %q, want %q", label, strings.TrimSpace(line), want)
			}
			return
		}
	}
	t.Errorf("%s transcript has no schema line:\n%s", label, transcript)
}

func assertNoHomeDatabase(t *testing.T, home, operation string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(home, ".roca", "roca.db")); !os.IsNotExist(err) {
		t.Fatalf("%s created the missing database", operation)
	}
}

func initChoiceHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ROCA_DB_PATH", "")
	t.Setenv("ROCA_CONFIG", "")
	t.Setenv("ROCA_MODELS_ORDER", "none")
	return home
}

func seedCandidate(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Adopt(t.Context(), db, filepath.Join(filepath.Dir(path), "backups")); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func runInitChoice(t *testing.T, tty bool, input string, args ...string) (string, error) {
	t.Helper()
	previous := terminalInput
	terminalInput = func(any) bool { return tty }
	t.Cleanup(func() { terminalInput = previous })
	return runRootErr(t, Build{Version: "test", Commit: "test-sha"},
		strings.NewReader(input), args...)
}
