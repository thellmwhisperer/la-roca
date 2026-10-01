//go:build acceptance && migrate_100k

package acceptance

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thellmwhisperer/la-roca/data"
	"github.com/thellmwhisperer/la-roca/internal/distribution/rocaops"
)

// TestMigrate100kWSL is the explicit, expensive issue #455 acceptance. Run it
// with ROCA_BIN=<branch binary> go test -tags=acceptance,migrate_100k
// ./test/acceptance -run '^TestMigrate100kWSL$' -count=1 -v. All databases and
// snapshots live under this worktree's ignored .tmp directory.
func TestMigrate100kWSL(t *testing.T) {
	root, err := acceptanceRoot()
	if err != nil {
		t.Fatal(err)
	}
	for _, interrupted := range []bool{false, true} {
		name := "uninterrupted"
		if interrupted {
			name = "sigint-resume"
		}
		t.Run(name, func(t *testing.T) {
			base, err := os.MkdirTemp(filepath.Join(root, ".tmp"), "migrate-455-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.RemoveAll(base) })
			home := filepath.Join(base, "home")
			core := filepath.Join(home, ".roca", "roca.db")
			ops := filepath.Join(home, ".roca", "plugins", rocaops.Name, rocaops.DatabaseFilename)
			if err := os.MkdirAll(filepath.Dir(core), 0700); err != nil {
				t.Fatal(err)
			}
			if _, err := rocaops.Ensure(filepath.Join(home, ".roca", "plugins"),
				filepath.Join(home, ".local", "bin"), "dev"); err != nil {
				t.Fatal(err)
			}
			if err := seedMigrate100k(core, ops); err != nil {
				t.Fatal(err)
			}
			binary, err := rocaBinary()
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
			defer cancel()
			// Pass only fixture-owned locations. Inherited ROCA_* overrides must
			// never redirect a branch binary into the operator's live home.
			env := []string{
				"HOME=" + home,
				"PATH=" + os.Getenv("PATH"),
				"TMPDIR=" + base,
				"ROCA_MODELS_ORDER=none",
			}
			args := []string{"--db-path", core, "migrate"}
			if interrupted {
				first := exec.CommandContext(ctx, binary, args...)
				first.Env = env
				stdout, err := first.StdoutPipe()
				if err != nil {
					t.Fatal(err)
				}
				var stderr bytes.Buffer
				first.Stderr = &stderr
				if err := first.Start(); err != nil {
					t.Fatal(err)
				}
				var transcript strings.Builder
				interruptedAt := false
				scanner := bufio.NewScanner(stdout)
				for scanner.Scan() {
					line := scanner.Text()
					transcript.WriteString(line + "\n")
					if !interruptedAt && strings.Contains(line, "source=plugin:roca-ops batch=10/400") {
						interruptedAt = true
						if err := first.Process.Signal(os.Interrupt); err != nil {
							t.Fatal(err)
						}
					}
				}
				if err := scanner.Err(); err != nil {
					t.Fatal(err)
				}
				waitErr := first.Wait()
				t.Logf("SIGINT first run:\n%s%sresult: %v", transcript.String(), stderr.String(), waitErr)
				if !interruptedAt || waitErr == nil {
					t.Fatalf("SIGINT did not interrupt after a committed batch")
				}
				before := snapshotStats(t, filepath.Join(home, ".roca", "backups", "data-split"))
				args = append(args, "--json")
				start := time.Now()
				resumed := exec.CommandContext(ctx, binary, args...)
				resumed.Env = env
				var out, progress bytes.Buffer
				resumed.Stdout, resumed.Stderr = &out, &progress
				err = resumed.Run()
				t.Logf("resume elapsed=%s stdout:\n%sprogress:\n%s", time.Since(start), out.String(), progress.String())
				if err != nil || !strings.Contains(out.String(), `"verified": true`) ||
					!strings.Contains(progress.String(), "reusing the existing plugin:roca-ops memory snapshot") ||
					!strings.Contains(progress.String(), "source=plugin:roca-ops batch=") {
					t.Fatalf("resume failed: %v", err)
				}
				if count := strings.Count(progress.String(), "stage=data2-import source=plugin:roca-ops batch="); count != 390 {
					t.Fatalf("resume printed %d batch lines, want 390", count)
				}
				after := snapshotStats(t, filepath.Join(home, ".roca", "backups", "data-split"))
				if !sameSnapshotStats(before, after) {
					t.Fatalf("resume rewrote valid snapshots: before=%v after=%v", before, after)
				}
				var verdict struct {
					Verified bool `json:"verified"`
				}
				if err := json.Unmarshal(out.Bytes(), &verdict); err != nil || !verdict.Verified {
					t.Fatalf("resume JSON verdict: %v, %s", err, out.String())
				}
				t.Log("all three snapshot sizes and modification times unchanged on resume")
			} else {
				start := time.Now()
				command := exec.CommandContext(ctx, binary, args...)
				command.Env = env
				output, err := command.CombinedOutput()
				t.Logf("100000-memory migrate elapsed=%s:\n%s", time.Since(start), output)
				if err != nil || !bytes.Contains(output, []byte("migration: verified")) ||
					!bytes.Contains(output, []byte("source=plugin:roca-ops batch=400/400")) {
					t.Fatalf("full migration failed: %v", err)
				}
				if count := bytes.Count(output, []byte("stage=data2-import source=plugin:roca-ops batch=")); count != 400 {
					t.Fatalf("full migration printed %d batch lines, want 400", count)
				}
				for _, stage := range []string{"data2-snapshot", "data2-import", "data2-fts-rebuild",
					"data2-verify", "data3-corpus", "data4-legacy"} {
					if !bytes.Contains(output, []byte("stage="+stage)) {
						t.Fatalf("missing %s progress stage", stage)
					}
				}
			}
			verifyMigrate100k(t, ops)
		})
	}
}

func seedMigrate100k(corePath, opsPath string) error {
	core, err := sql.Open("sqlite", corePath)
	if err != nil {
		return err
	}
	if _, err := core.Exec(data.Schema); err != nil {
		core.Close()
		return err
	}
	if err := core.Close(); err != nil {
		return err
	}
	if err := rocaops.ApplySchema(opsPath); err != nil {
		return err
	}
	ops, err := sql.Open("sqlite", opsPath)
	if err != nil {
		return err
	}
	defer ops.Close()
	_, err = ops.Exec(`WITH RECURSIVE sequence(n) AS (
		VALUES(1) UNION ALL SELECT n+1 FROM sequence WHERE n < 100000
	) INSERT INTO memories (layer, content, origin)
	SELECT 'project', printf('synthetic memory %06d', n), 'agent' FROM sequence`)
	return err
}

type migrateSnapshotStat struct {
	size int64
	mod  time.Time
}

func snapshotStats(t *testing.T, directory string) map[string]migrateSnapshotStat {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	stats := map[string]migrateSnapshotStat{}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".snapshot.db") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			t.Fatal(err)
		}
		stats[entry.Name()] = migrateSnapshotStat{info.Size(), info.ModTime()}
	}
	if len(stats) != 3 {
		t.Fatalf("snapshot count=%d, want 3", len(stats))
	}
	return stats
}

func sameSnapshotStats(left, right map[string]migrateSnapshotStat) bool {
	if len(left) != len(right) {
		return false
	}
	for name, before := range left {
		after, ok := right[name]
		if !ok || before.size != after.size || !before.mod.Equal(after.mod) {
			return false
		}
	}
	return true
}

func verifyMigrate100k(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for query, want := range map[string]int{
		"SELECT COUNT(*) FROM memories":                                                     100000,
		"SELECT COUNT(*) FROM memory_records":                                               100000,
		"SELECT COUNT(*) FROM custody_memberships WHERE migration = 'data2-memory-custody'": 100000,
		"SELECT COUNT(*) FROM migration_batches WHERE migration = 'data2-memory-custody'":   400,
	} {
		var got int
		if err := db.QueryRow(query).Scan(&got); err != nil || got != want {
			t.Fatalf("%s: got=%d want=%d err=%v", query, got, want, err)
		}
	}
	t.Log("verified source, destination, and membership counts: 100000; committed batches: 400")
	var state string
	if err := db.QueryRow("SELECT migration_state FROM plugin_migrations WHERE migration = 'data2-memory-custody'").Scan(&state); err != nil || state != "verified" {
		t.Fatalf("ledger state=%q err=%v", state, err)
	}
	for _, probe := range []struct {
		name, query string
		args        []any
	}{
		{"orphans", `SELECT COUNT(*) FROM memory_records AS records WHERE NOT EXISTS
			(SELECT 1 FROM custody_memberships AS memberships WHERE memberships.migration = ?
			AND memberships.destination_key = CAST(records.id AS TEXT))`, []any{"data2-memory-custody"}},
		{"alias", `SELECT records.id FROM memory_records AS records
			JOIN custody_memberships AS represented ON represented.migration = ?
			AND represented.destination_key = CAST(records.id AS TEXT)
			WHERE records.canonical_digest = ? AND represented.source_database <> ?
			AND NOT EXISTS (SELECT 1 FROM custody_memberships AS same_source
			WHERE same_source.migration = ? AND same_source.destination_key = CAST(records.id AS TEXT)
			AND same_source.source_database = ?) ORDER BY records.id`,
			[]any{"data2-memory-custody", "synthetic-digest", "core", "data2-memory-custody", "core"}},
	} {
		rows, err := db.Query("EXPLAIN QUERY PLAN "+probe.query, probe.args...)
		if err != nil {
			t.Fatal(err)
		}
		var details []string
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				t.Fatal(err)
			}
			details = append(details, detail)
			if strings.HasPrefix(detail, "SCAN memberships") || strings.HasPrefix(detail, "SCAN represented") || strings.HasPrefix(detail, "SCAN same_source") {
				t.Fatalf("%s scans custody: %s", probe.name, detail)
			}
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
		t.Logf("EXPLAIN %s:\n%s", probe.name, strings.Join(details, "\n"))
		if !strings.Contains(strings.Join(details, "\n"), "custody_memberships_migration_destination (migration=? AND destination_key=?)") {
			t.Fatalf("%s lacks destination index search", probe.name)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	var orphans int
	err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM memory_records AS records WHERE NOT EXISTS
		(SELECT 1 FROM custody_memberships AS memberships WHERE memberships.migration = ?
		AND CAST(memberships.destination_key AS INTEGER) = records.id)`, "data2-memory-custody").Scan(&orphans)
	t.Logf("old orphan predicate elapsed=%s error=%v", time.Since(start), err)
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("old predicate unexpectedly completed within five seconds: orphans=%d err=%v", orphans, err)
	}
	start = time.Now()
	err = db.QueryRow(`SELECT COUNT(*) FROM memory_records AS records WHERE NOT EXISTS
		(SELECT 1 FROM custody_memberships AS memberships WHERE memberships.migration = ?
		AND memberships.destination_key = CAST(records.id AS TEXT))`, "data2-memory-custody").Scan(&orphans)
	t.Logf("new orphan predicate elapsed=%s orphans=%d error=%v", time.Since(start), orphans, err)
	if err != nil || orphans != 0 {
		t.Fatal(fmt.Errorf("new orphan query: orphans=%d: %w", orphans, err))
	}
}
