package vector

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/thellmwhisperer/la-roca/data"
)

// TestCostCoreReader executes a complete source sweep and neighbour retrieval on
// one migrated lab. The shell wrapper accounts for actual helper process starts,
// including any accidental retries or page-by-page launches.
func TestCostCoreReader(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX process accounting wrapper")
	}
	root, err := filepath.Abs("../../../..")
	if err != nil {
		t.Fatal(err)
	}
	buildEnv := os.Environ()
	lab := t.TempDir()
	t.Setenv("HOME", lab)
	for _, key := range []string{"ROCA_DB_PATH", "ROCA_CONFIG", "ROCA_PREFIX", "CLAUDE_CONFIG_DIR", "CODEX_HOME", "CURSOR_HOME", "GROK_HOME", "OPENCODE_CONFIG", "HERMES_HOME", "PI_CODING_AGENT_DIR", "QWEN_HOME"} {
		t.Setenv(key, "")
	}
	t.Setenv("ROCA_READ_ONLY", "0")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(lab, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(lab, "data"))
	branch := filepath.Join(lab, "roca")
	build := exec.Command("go", "build", "-o", branch, "./cmd/roca")
	build.Dir = root
	// Keep the already configured Go toolchain/cache; only the tested programs
	// run with the isolated home, and no user database is selected implicitly.
	build.Env = buildEnv
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v: %s", err, out)
	}
	corePath := filepath.Join(lab, "roca.db")
	db, err := sql.Open("sqlite", corePath)
	if err != nil {
		t.Fatal(err)
	}
	const count = 30000
	_, err = db.Exec(data.Schema + `
INSERT INTO sessions(session_id,source_agent,title) VALUES('lab','fixture','synthetic reader');
WITH RECURSIVE n(i) AS (VALUES(1) UNION ALL SELECT i+1 FROM n WHERE i<30000)
INSERT INTO exchanges(session_id,exchange_number,agent_text,agent_timestamp)
SELECT 'lab',i,'synthetic reader text ' || i,'2026-01-01' FROM n;`)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(branch, "--db-path", corePath, "migrate", "--json").CombinedOutput(); err != nil {
		t.Fatalf("migrate: %v: %s", err, out)
	}
	// Migration establishes custody; a writable service open publishes the
	// vector registry used by the installed reader. Both happen only in the lab.
	if out, err := exec.Command(branch, "--db-path", corePath, "exec", "SELECT 1", "--json").CombinedOutput(); err != nil {
		t.Fatalf("warm lab service: %v: %s", err, out)
	}
	published := os.Getenv("ROCA_READER_PUBLISHED_BIN")
	if published != "" {
		out, err := exec.Command(published, "version").CombinedOutput()
		if err != nil || !strings.Contains(string(out), "v1.84.1") {
			t.Fatalf("published baseline requires v1.84.1: %s (%v)", out, err)
		}
	}
	var baseline time.Duration
	var transcript strings.Builder
	for _, run := range []struct {
		name, binary string
		legacy       bool
	}{
		{"published", published, true}, {"page-path", branch, true}, {"branch", branch, false},
	} {
		if run.binary == "" {
			continue
		}
		accounting := filepath.Join(lab, run.name+".processes")
		wrapper := filepath.Join(lab, run.name+".sh")
		body := "#!/bin/sh\nprintf 'start\\n' >> \"$D4_ACCOUNTING\"\nexec \"$D4_BINARY\" \"$@\"\n"
		if err := os.WriteFile(wrapper, []byte(body), 0700); err != nil {
			t.Fatal(err)
		}
		t.Setenv("D4_ACCOUNTING", accounting)
		t.Setenv("D4_BINARY", run.binary)
		core := CoreCLI{Executable: wrapper, DBPath: corePath}
		if run.legacy {
			core.Run = runCommand
		}
		federation, err := LoadFederation(core, filepath.Join(lab, ".roca", "plugins"), DefaultModel, "test", &recordingEmbedder{}, nil)
		if err != nil {
			t.Fatal(err)
		}
		var reader DeclaredCorpus
		for _, database := range federation.databases {
			if database.Database == "corpus" {
				reader = DeclaredCorpus{Core: core, Database: database}
			}
		}
		ctx, closeReader := withCoreReader(context.Background())
		started := time.Now()
		scope, err := core.ResolveDatabaseScope(ctx, "corpus")
		if err != nil || len(scope.Databases) != 1 {
			closeReader()
			t.Fatalf("%s scope: %+v: %v", run.name, scope, err)
		}
		seen := 0
		var neighbour sourceRow
		err = reader.WalkSources(ctx, "", func(row sourceRow) error {
			if row.kind == "exchanges" {
				seen++
				neighbour = row
			}
			return nil
		})
		if err != nil {
			closeReader()
			t.Fatal(err)
		}
		for range 3 {
			text, err := reader.ResolveSource(ctx, neighbour.kind, neighbour.locator())
			if err != nil || text != neighbour.text {
				closeReader()
				t.Fatalf("neighbour: %q, %v", text, err)
			}
		}
		closeReader()
		elapsed := time.Since(started)
		if seen != count {
			t.Fatalf("%s read %d of %d exchanges", run.name, seen, count)
		}
		starts, err := os.ReadFile(accounting)
		if err != nil {
			t.Fatal(err)
		}
		processes := strings.Count(string(starts), "start\n")
		fmt.Fprintf(&transcript, "%s: 30k exchanges, %d helpers, %s\n", run.name, processes, elapsed.Round(time.Millisecond))
		t.Logf("%s: %d helpers, %s", run.name, processes, elapsed)
		if run.legacy {
			if processes <= 1 {
				t.Fatal("baseline did not reproduce per-page process launches")
			}
			if baseline == 0 || elapsed < baseline {
				baseline = elapsed
			}
		} else {
			if processes > 1 {
				t.Fatalf("D4 returned: %d helper processes", processes)
			}
			if elapsed*5 > baseline {
				t.Fatalf("reader %s must be at least 5x faster than page path %s", elapsed, baseline)
			}
			// Exercise the public operation boundaries without a test-created
			// reader context: each whole ingest/query must own one helper.
			for _, operation := range []string{"ingest", "query"} {
				if err := os.WriteFile(accounting, nil, 0600); err != nil {
					t.Fatal(err)
				}
				if operation == "ingest" {
					delta, err := federation.Ingest(context.Background(), "")
					if err != nil || delta.Added < count {
						t.Fatalf("full ingest: %+v: %v", delta, err)
					}
					fmt.Fprintf(&transcript, "branch ingest result: %d chunks added\n", delta.Added)
				} else {
					result, err := federation.Query(context.Background(), "synthetic reader", 3, "corpus")
					if err != nil || len(result.Results) != 3 {
						t.Fatalf("query: %+v: %v", result, err)
					}
					fmt.Fprintf(&transcript, "branch query result: %d neighbours returned\n", len(result.Results))
				}
				starts, err := os.ReadFile(accounting)
				if err != nil {
					t.Fatal(err)
				}
				if n := strings.Count(string(starts), "start\n"); n != 1 {
					t.Fatalf("whole %s launched %d helpers", operation, n)
				}
				fmt.Fprintf(&transcript, "branch full %s: 1 helper\n", operation)
			}
		}
	}
	dir := os.Getenv("ROCA_READER_EVIDENCE_DIR")
	if dir == "" {
		dir = filepath.Join(root, ".tmp", "d4-evidence")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "comparison.txt"), []byte(transcript.String()), 0600); err != nil {
		t.Fatal(err)
	}
}

// Published baseline adapter: execute the pre-D4 CLI page protocol.
func runCommand(ctx context.Context, executable string, args ...string) ([]byte, error) {
	finished, err := beginTrackedCommand(ctx)
	if err != nil {
		return nil, err
	}
	defer finished()
	command := exec.CommandContext(ctx, executable, args...)
	command.Env = append(os.Environ(), "ROCA_READ_ONLY=1")
	configureCommandCancellation(command)
	raw, err := command.Output()
	if err == nil {
		return raw, nil
	}
	message := ""
	if exit, ok := err.(*exec.ExitError); ok {
		message = strings.TrimSpace(string(exit.Stderr))
	}
	if len(message) > 4096 {
		message = message[:4096] + "…"
	}
	if message != "" {
		return nil, fmt.Errorf("roca exec: %w: %s", err, message)
	}
	return nil, fmt.Errorf("roca exec: %w", err)
}
