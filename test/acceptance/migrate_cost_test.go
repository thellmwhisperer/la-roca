//go:build acceptance

package acceptance

import (
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/thellmwhisperer/la-roca/data"
	_ "modernc.org/sqlite"
)

// TestCostMigrate measures only a synthetic home. The optional published binary
// runs against the same verified lab before the branch; neither sees real data.
func TestCostMigrate(t *testing.T) {
	var costs, publishedCosts []migrateCost
	for _, fixture := range []struct {
		name string
		rows int
	}{{"small", 10}, {"large", 10000}} {
		t.Run(fixture.name, func(t *testing.T) {
			branch, published := migrateFixtureCost(t, fixture.name, fixture.rows)
			costs = append(costs, branch)
			publishedCosts = append(publishedCosts, published)
		})
	}
	if len(costs) != 2 {
		t.Fatal("both lab sizes must complete")
	}
	if os.Getenv("ROCA_PUBLISHED_BIN") != "" {
		if float64(publishedCosts[1].BytesRead) <= float64(publishedCosts[0].BytesRead)*1.05 || publishedCosts[1].BytesRead <= costs[1].BytesRead {
			t.Fatal("published baseline did not reproduce size-dependent open reads")
		}
	}
	small, large := float64(costs[0].BytesRead), float64(costs[1].BytesRead)
	if large < small*0.95 || large > small*1.05 {
		t.Fatalf("D2: verified open reads grew with lab size: small=%.0f large=%.0f; tolerance 5%%", small, large)
	}
}

func migrateFixtureCost(t *testing.T, label string, rows int) (migrateCost, migrateCost) {
	m := aWorldIn(t, "migrate-cost-"+label)
	root, err := acceptanceRoot()
	if err != nil {
		t.Fatal(err)
	}
	evidenceDir := os.Getenv("ROCA_MIGRATE_EVIDENCE_DIR")
	if evidenceDir == "" {
		evidenceDir = filepath.Join(root, ".tmp", "migrate-evidence")
	}
	if err := os.MkdirAll(evidenceDir, 0700); err != nil {
		t.Fatal(err)
	}
	var transcript strings.Builder
	t.Cleanup(func() {
		if err := os.WriteFile(filepath.Join(evidenceDir, label+"-cli.txt"), []byte(transcript.String()), 0600); err != nil {
			t.Error(err)
		}
	})
	dbPath := filepath.Join(m.home, ".roca", "roca.db")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), data.Schema); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO memories(layer,content,origin) VALUES('project','synthetic migration marker','agent');
 INSERT INTO sessions(session_id,source_agent,title) VALUES('synthetic-session','fixture','migration cost');
 CREATE TABLE garden_messages(id INTEGER PRIMARY KEY,content TEXT)`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < rows; i++ {
		if _, err := tx.Exec(`INSERT INTO garden_messages(id,content) VALUES(?,?)`, i, strings.Repeat("synthetic legacy cost ", 50)); err != nil {
			t.Fatal(err)
		}
	}
	exchanges := 10
	if rows > 10 {
		exchanges = 128
	}
	if _, err := tx.Exec(`WITH RECURSIVE sequence(n) AS (
  VALUES(0) UNION ALL SELECT n+1 FROM sequence WHERE n+1 < ?
 ) INSERT INTO exchanges(session_id,exchange_number,agent_text)
 SELECT 'synthetic-session',n,? FROM sequence`, exchanges, strings.Repeat("synthetic migration cost ", 3000)); err != nil {
		t.Fatal(err)
	}

	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	output, code := m.runUnder(t, nil, "migrate", "--json")
	fmt.Fprintf(&transcript, "$ roca migrate --json\n%s[exit %d]\n", output, code)
	if code != 0 || !strings.Contains(output, `"verified": true`) {
		t.Fatalf("migrate: code=%d %s", code, output)
	}
	config := filepath.Join(m.home, ".roca", "config.toml")
	if err := os.WriteFile(config, []byte("[layout]\nserving = \"cutover\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	branch := m.binary
	measure := func(binaryLabel string) migrateCost {
		// Warm installer version checks outside the measured ordinary open.
		if output, code := m.runUnder(t, nil, "exec", "SELECT 1"); code != 0 {
			t.Fatalf("warm %s: %s", binaryLabel, output)
		}
		result := measureMigrateExec(t, m, root)
		evidence, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(evidenceDir, label+"-"+binaryLabel+".json"), evidence, 0600); err != nil {
			t.Fatal(err)
		}
		t.Logf("%s: %s", binaryLabel, evidence)
		return result
	}
	var before migrateCost
	if published := os.Getenv("ROCA_PUBLISHED_BIN"); published != "" {
		m.binary = published
		before = measure("published")
	}
	m.binary = branch
	after := measure("branch")
	// Offline snapshots prove zero source dependency even with warm file caches.
	snapshots := filepath.Join(m.home, ".roca", "backups", "data-split")
	if err := os.Rename(snapshots, snapshots+"-offline"); err != nil {
		t.Fatal(err)
	}
	transcript.WriteString("\nFrozen snapshots moved offline; backups retained.\n")
	for _, args := range [][]string{{"exec", "SELECT 1"}, {"migrate", "--json"}} {
		output, code := m.runUnder(t, nil, args...)
		fmt.Fprintf(&transcript, "$ roca %s\n%s[exit %d]\n", strings.Join(args, " "), output, code)
		if code != 0 {
			t.Fatalf("without frozen sources: %v: %s", args, output)
		}
	}
	query := "SELECT COUNT(*) AS migrated_exchanges FROM plugin_roca_corpus.exchanges"
	output, code = m.runUnder(t, nil, "exec", query, "--json")
	fmt.Fprintf(&transcript, "$ roca exec %q --json\n%s[exit %d]\n", query, output, code)
	var result struct {
		Rows []struct {
			Exchanges int `json:"migrated_exchanges"`
		} `json:"rows"`
	}
	if err := json.Unmarshal([]byte(output), &result); err != nil || code != 0 || len(result.Rows) != 1 || result.Rows[0].Exchanges != exchanges {
		t.Fatalf("migrated exchanges without frozen sources: code=%d %s", code, output)
	}
	if after.ElapsedNS >= int64(100*time.Millisecond) {
		t.Fatalf("D2: ordinary exec took %s; budget is <100 ms", time.Duration(after.ElapsedNS))
	}
	return after, before
}

type migrateCost struct {
	BytesRead uint64 `json:"bytes_read"`
	ElapsedNS int64  `json:"elapsed_ns"`
}

func measureMigrateExec(t *testing.T, m *world, root string) migrateCost {
	t.Helper()
	if runtime.GOOS == "linux" {
		cmd := exec.Command("python3", filepath.Join(root, "testdata", "migrate-cost", "measure-linux.py"), m.binary, "exec", "SELECT 1")
		cmd.Env = m.environment()
		raw, err := cmd.Output()
		if err != nil {
			t.Fatal(err)
		}
		var result migrateCost
		if err := json.Unmarshal(raw, &result); err != nil {
			t.Fatal(err)
		}
		if result.BytesRead == 0 {
			t.Fatal("missing process read counter")
		}
		return result
	}
	if runtime.GOOS != "darwin" {
		t.Skip("process read measurement requires Linux or Darwin")
	}
	library := filepath.Join(t.TempDir(), "reads.dylib")
	cmd := exec.Command("clang", "-dynamiclib", "-o", library, filepath.Join(root, "testdata", "migrate-cost", "reads-darwin.c"))
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("read counter: %v: %s", err, output)
	}
	counter := filepath.Join(t.TempDir(), "reads")
	output, code := m.runUnder(t, []string{"DYLD_INSERT_LIBRARIES=" + library, "ROCA_COST_COUNTER=" + counter}, "exec", "SELECT 1")
	if code != 0 {
		t.Fatalf("measured exec: %s", output)
	}
	raw, err := os.ReadFile(counter)
	if err != nil || len(raw) != 8 {
		t.Fatalf("missing process read counter: %v", err)
	}
	// Timing is a separate uninstrumented execution: tracing adds its own writes.
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	timed := exec.CommandContext(ctx, m.binaryPath(), "exec", "SELECT 1")
	timed.Env = m.environment()
	started := time.Now()
	_, err = timed.Output()
	elapsed := time.Since(started)
	if err != nil {
		t.Fatal(err)
	}
	return migrateCost{BytesRead: binary.LittleEndian.Uint64(raw), ElapsedNS: int64(elapsed)}
}
