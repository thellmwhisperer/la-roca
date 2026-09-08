//go:build acceptance

package acceptance

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/thellmwhisperer/la-roca/internal/store"
)

// TestCostMigrate measures only a synthetic home. The optional published binary
// runs against the same verified lab before the branch; neither sees real data.
func TestCostMigrate(t *testing.T) {
	m := aWorldIn(t, "migrate-cost")
	root, err := acceptanceRoot()
	if err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(m.home, ".roca", "roca.db")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0700); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ApplySchema(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL().Exec(`INSERT INTO memories(layer,content,origin) VALUES('project','synthetic migration marker','agent');
 INSERT INTO sessions(session_id,source_agent,title) VALUES('synthetic-session','fixture','migration cost')`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.SQL().Begin()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 128; i++ {
		if _, err := tx.Exec(`INSERT INTO exchanges(session_id,exchange_number,agent_text) VALUES('synthetic-session',?,?)`, i, strings.Repeat("synthetic migration cost ", 3000)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	output, code := m.runUnder(t, nil, "migrate", "--json")
	if code != 0 || !strings.Contains(output, `"verified": true`) {
		t.Fatalf("migrate: code=%d %s", code, output)
	}
	config := filepath.Join(m.home, ".roca", "config.toml")
	if err := os.WriteFile(config, []byte("[layout]\nserving = \"cutover\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	branch := m.binary
	measure := func(label string) migrateCost {
		// Warm installer version checks outside the measured ordinary open.
		if output, code := m.runUnder(t, nil, "exec", "SELECT 1"); code != 0 {
			t.Fatalf("warm %s: %s", label, output)
		}
		result := measureMigrateExec(t, m, root)
		evidence, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		dir := filepath.Join(root, ".tmp", "migrate-evidence")
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, label+".json"), evidence, 0600); err != nil {
			t.Fatal(err)
		}
		t.Logf("%s: %s", label, evidence)
		return result
	}
	if published := os.Getenv("ROCA_PUBLISHED_BIN"); published != "" {
		m.binary = published
		before := measure("published")
		if before.BytesRead < 1000000 {
			t.Fatal("published baseline did not reproduce source reads")
		}
	}
	m.binary = branch
	after := measure("branch")
	// Offline snapshots prove zero source dependency even with warm file caches.
	snapshots := filepath.Join(m.home, ".roca", "backups", "data-split")
	if err := os.Rename(snapshots, snapshots+"-offline"); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"exec", "SELECT 1"}, {"migrate", "--json"}} {
		if output, code := m.runUnder(t, nil, args...); code != 0 {
			t.Fatalf("without frozen sources: %v: %s", args, output)
		}
	}
	if after.BytesRead >= 1000000 || after.ElapsedNS >= int64(100*time.Millisecond) {
		t.Fatalf("D2: ordinary exec read %d bytes in %s; budgets are <1 MB and <100 ms", after.BytesRead, time.Duration(after.ElapsedNS))
	}
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
	command := exec.CommandContext(ctx, m.binary, "exec", "SELECT 1")
	command.Env = m.environment()
	started := time.Now()
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("timed exec: %v: %s", err, output)
	}
	elapsed := time.Since(started)
	return migrateCost{BytesRead: binary.LittleEndian.Uint64(raw), ElapsedNS: int64(elapsed)}
}
