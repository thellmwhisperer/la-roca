//go:build acceptance

package acceptance

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thellmwhisperer/la-roca/data"
)

// TestCostQualifiedFTSCLI compares two binaries against one disposable migrated
// lab, never the live federation. Setup and installer warmup are outside timing.
func TestCostQualifiedFTSCLI(t *testing.T) {
	m := aWorldIn(t, "fts-cost")
	core := filepath.Join(m.home, ".roca", "roca.db")
	if err := os.MkdirAll(filepath.Dir(core), 0700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", core)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(data.Schema+`INSERT INTO sessions(session_id,source_agent,title) VALUES('lab','fixture','quartz');
 WITH RECURSIVE n(i) AS (VALUES(1) UNION ALL SELECT i+1 FROM n WHERE i<30000)
 INSERT INTO exchanges(session_id, exchange_number, agent_text)
 SELECT 'lab', i, ? FROM n;`, strings.Repeat("synthetic quartz laboratory text ", 100))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if output, code := m.runUnder(t, nil, "migrate", "--json"); code != 0 {
		t.Fatalf("migrate: %s", output)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(core), "config.toml"), []byte("[layout]\nserving = \"cutover\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	branch := m.binary
	published := os.Getenv("ROCA_FTS_PUBLISHED_BIN")
	var transcript strings.Builder
	root, err := acceptanceRoot()
	if err != nil {
		t.Fatal(err)
	}
	dir := os.Getenv("ROCA_FTS_EVIDENCE_DIR")
	if dir == "" {
		dir = filepath.Join(root, ".tmp", "fts-evidence")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	evidenceFile := "branch.txt"
	if published != "" {
		evidenceFile = "comparison.txt"
	}
	t.Cleanup(func() {
		if err := os.WriteFile(filepath.Join(dir, evidenceFile), []byte(transcript.String()), 0600); err != nil {
			t.Error(err)
		}
	})
	for _, run := range []struct{ label, binary string }{{"published", published}, {"branch", branch}} {
		if run.binary == "" {
			continue
		}
		m.binary = run.binary
		version, code := m.runUnder(t, nil, "version")
		if code != 0 {
			t.Fatalf("version: %s", version)
		}
		if run.label == "published" && !strings.Contains(version, "v1.84.0") {
			t.Fatalf("published evidence requires v1.84.0: %s", version)
		}
		if output, code := m.runUnder(t, nil, "exec", "SELECT 1"); code != 0 {
			t.Fatalf("warm %s: %s", run.label, output)
		}
		start := time.Now()
		output, code := m.runUnder(t, nil, "exec", "SELECT COUNT(*) AS hits FROM plugin_roca_corpus.exchanges_fts WHERE exchanges_fts MATCH 'quartz'", "--json")
		elapsed := time.Since(start)
		fmt.Fprintf(&transcript, "%s %squalified FTS (30k synthetic exchanges, about 90 MB text): %s\n%s\n", run.label, version, elapsed.Round(time.Millisecond), output)
		if code != 0 || !strings.Contains(output, `"hits": 30000`) {
			t.Fatalf("%s FTS: %s", run.label, output)
		}
		if run.label == "branch" && elapsed >= 200*time.Millisecond {
			t.Fatalf("branch qualified FTS: %s; budget 200 ms", elapsed)
		}
		if run.label == "published" && elapsed < 200*time.Millisecond {
			t.Fatalf("published binary did not reproduce D3: %s", elapsed)
		}
		t.Logf("%s qualified FTS: %s", run.label, elapsed)
	}
	output, code := m.runUnder(t, nil, "exec", "SELECT COUNT(*) FROM exchanges_fts WHERE exchanges_fts MATCH 'quartz'", "--json")
	fmt.Fprintf(&transcript, "branch unqualified FTS (exit %d):\n%s\n", code, output)
	if code == 0 || !strings.Contains(output, "plugin_roca_corpus.exchanges_fts") {
		t.Fatalf("unqualified FTS must suggest its owner: %s", output)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(core), "config.toml"), []byte("[layout]\nserving = \"shadow-equal\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	output, code = m.runUnder(t, nil, "exec", "SELECT 1", "--json")
	fmt.Fprintf(&transcript, "branch retired shadow-equal layout (exit %d):\n%s\n", code, output)
	if code == 0 || !strings.Contains(output, "shadow-equal validation is retired") {
		t.Fatalf("retired layout must explain the serving choice: %s", output)
	}
}
