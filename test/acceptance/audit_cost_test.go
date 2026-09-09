//go:build acceptance

package acceptance

import (
	"bufio"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/thellmwhisperer/la-roca/internal/distribution/bundledplugin"
)

// TestCostAuditDestination observes actual CLI calls on synthetic homes. The
// optional published control must reproduce the retired second destination.
func TestCostAuditDestination(t *testing.T) {
	branch, err := rocaBinary()
	if err != nil {
		t.Fatal(err)
	}
	published := os.Getenv("ROCA_AUDIT_PUBLISHED_BIN")
	if published != "" {
		t.Run("storage-upgrade", func(t *testing.T) { checkAuditStorageUpgrade(t, branch, published) })
	}
	for _, label := range []string{"branch", "published"} {
		if label == "published" && published == "" {
			continue
		}
		t.Run(label, func(t *testing.T) {
			m := aWorldIn(t, "audit-"+label)
			m.binary = branch
			if published != "" {
				m.binary = published
			}
			// A paired branch run upgrades a home initialized by the published
			// product. A branch-only run exercises a fresh current installation.
			if output, code := m.runUnder(t, nil, "init", "--db-path", filepath.Join(m.home, ".roca", "roca.db"), "--json"); code != 0 {
				t.Fatalf("synthetic init: code=%d %s", code, output)
			}
			if label == "branch" {
				m.binary = branch
			}
			version, code := m.runUnder(t, nil, "--version")
			if code != 0 || (label == "published" && !strings.HasPrefix(version, "roca v")) {
				t.Fatalf("invalid %s version: %s", label, version)
			}
			binary, err := os.Open(m.binaryPath())
			if err != nil {
				t.Fatal(err)
			}
			hash := sha256.New()
			_, err = io.Copy(hash, binary)
			binary.Close()
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("%s version=%s sha256=%x", label, strings.TrimSpace(version), hash.Sum(nil))
			if output, code := m.runUnder(t, nil, "exec", "SELECT 1", "--json"); code != 0 {
				t.Fatalf("prepare installed schema: code=%d %s", code, output)
			}
			opsPath := filepath.Join(m.home, ".roca", "plugins", "roca-ops", "roca-ops.db")
			for _, query := range []string{"SELECT 353 AS synthetic_audit", "DELETE FROM plugin_roca_ops.memories"} {
				before, rows := auditRecords(t, m.home), auditRows(t, opsPath, label == "branch")
				digests := durableDigests(t, m.home)
				output, code := m.runUnder(t, nil, "exec", query, "--json")
				ok := strings.HasPrefix(query, "SELECT")
				if (code == 0) != ok || (ok && !strings.Contains(output, "353")) {
					t.Fatalf("exec result: code=%d %s", code, output)
				}
				after := auditRecords(t, m.home)
				var added []string
				for raw, count := range after {
					for i := before[raw]; i < count; i++ {
						added = append(added, raw)
					}
				}
				for raw, count := range before {
					if after[raw] < count {
						t.Fatal("exec removed a fixture audit record")
					}
				}
				if len(added) != 1 {
					t.Fatalf("exec added %d logical JSONL records, want 1", len(added))
				}
				var record struct {
					Command string
					Args    []string
					OK      bool
					Error   string
				}
				if err := json.Unmarshal([]byte(added[0]), &record); err != nil {
					t.Fatal(err)
				}
				if record.Command != "exec" || !reflect.DeepEqual(record.Args, []string{query}) || record.OK != ok || (!ok && record.Error == "") {
					t.Fatalf("wrong execution record: %+v", record)
				}
				growth := auditRows(t, opsPath, label == "branch") - rows
				wantGrowth := 0
				if label == "published" {
					wantGrowth = 1
				}
				if growth != wantGrowth {
					t.Fatalf("%s ops growth=%d, want %d", label, growth, wantGrowth)
				}
				if label == "branch" && !reflect.DeepEqual(digests, durableDigests(t, m.home)) {
					t.Fatal("exec changed durable database bytes")
				}
				t.Logf("%s exec success=%v exit=%d JSONL_records=1 ops_rows_added=%d", label, ok, code, growth)
			}
			if label == "branch" {
				checkAuditDoctor(t, m, opsPath)
			}
		})
	}
}

func auditRows(t *testing.T, path string, absent bool) int {
	t.Helper()
	db, err := bundledplugin.OpenDatabase(path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if absent {
		var count int
		if err := db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name GLOB '*call_history*'").Scan(&count); err != nil || count != 0 {
			t.Fatalf("audit schema objects=%d, err=%v; want absent", count, err)
		}
		return 0
	}
	var count int
	if err := db.QueryRow("SELECT count(*) FROM call_history").Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func auditRecords(t *testing.T, home string) map[string]int {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(home, ".roca", "logs", "*.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	records := map[string]int{}
	for _, path := range paths {
		file, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 4096), 5<<20)
		for scanner.Scan() {
			if !json.Valid(scanner.Bytes()) {
				t.Fatal("malformed audit JSON")
			}
			records[scanner.Text()]++
		}
		file.Close()
		if err := scanner.Err(); err != nil {
			t.Fatal(err)
		}
	}
	return records
}

func checkAuditDoctor(t *testing.T, m *world, opsPath string) {
	t.Helper()
	now := time.Now().UTC()
	path := filepath.Join(m.home, ".roca", "logs", "executions-"+now.Format(time.DateOnly)+"-1.jsonl")
	raw := fmt.Sprintf("{\"timestamp\":%q,\"source\":\"cli\",\"command\":\"query\",\"ok\":false,\"error\":\"synthetic failure\",\"correlation_id\":\"qf_synthetic\"}\n", now.Format(time.RFC3339Nano))
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	before, rows := durableDigests(t, m.home), auditRows(t, opsPath, true)
	output, code := m.runUnder(t, nil, "doctor", "--json")
	if code != 0 || !strings.Contains(output, "qf_synthetic") {
		t.Fatalf("doctor did not read JSONL: code=%d %s", code, output)
	}
	var report struct {
		Failures struct{ Count int } `json:"query_failures"`
	}
	if err := json.Unmarshal([]byte(output), &report); err != nil || report.Failures.Count != 1 {
		t.Fatalf("doctor query failure count: %+v, err=%v", report, err)
	}
	if auditRows(t, opsPath, true) != rows || !reflect.DeepEqual(before, durableDigests(t, m.home)) {
		t.Fatal("doctor mutated ops or backfilled retained JSONL")
	}
	audit := auditRecords(t, m.home)
	if output, code := m.runUnder(t, nil, "doctor", "--report", "--json"); code != 0 {
		t.Fatalf("support report: code=%d %s", code, output)
	}
	if !reflect.DeepEqual(audit, auditRecords(t, m.home)) || !reflect.DeepEqual(before, durableDigests(t, m.home)) {
		t.Fatal("support report changed audit or database bytes")
	}
	t.Log("branch doctor: JSONL failure found; no database mutation; support report emitted no audit")
}
