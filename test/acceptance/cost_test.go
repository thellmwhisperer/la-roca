//go:build acceptance

package acceptance

import (
	"crypto/sha256"
	"database/sql"
	"gopkg.in/yaml.v3"
	"io/fs"
	_ "modernc.org/sqlite"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestCostTerminalObserver pins S3's zero-byte package budget and executes the
// public help interface in a disposable home. Asking for command help cannot
// start the retired observer even when testing an older published binary.
func TestCostTerminalObserver(t *testing.T) {
	root, err := acceptanceRoot()
	if err != nil {
		t.Fatal(err)
	}
	var record struct {
		Forbid struct{ Paths []string }
	}
	body, err := os.ReadFile(filepath.Join(root, ".slop", "dragons", "S3-terminal-observer.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(body, &record); err != nil {
		t.Fatal(err)
	}
	if len(record.Forbid.Paths) != 1 {
		t.Fatal("S3 must forbid its retired package")
	}
	packagePath := filepath.Join(root, strings.TrimSuffix(record.Forbid.Paths[0], "/**"))
	if _, err := os.Stat(packagePath); !os.IsNotExist(err) {
		t.Fatalf("S3: package budget is 0 bytes; retired directory exists or cannot be inspected: %v", err)
	}
	m := aWorldIn(t, "retired-observer")
	output, code := m.runUnder(t, nil, "--help")
	if code != 0 || strings.Contains(output, "tool-call-observer") {
		t.Fatalf("S3: root help still exposes the observer: code %d\n%s", code, output)
	}
	output, code = m.runUnder(t, nil, "tool-call-observer", "--help")
	if code == 0 || !strings.Contains(output, `unknown command "tool-call-observer"`) {
		t.Fatalf("S3: retired command still resolves: code %d\n%s", code, output)
	}
	t.Log("S3: retained observer package 0 bytes; CLI seat absent")
}

// TestCostReadOnly verifies D1 while copying is impossible, including copies
// that an older implementation deleted before returning. SQLite SHM is allowed.
func TestCostReadOnly(t *testing.T) {
	m := costLab(t)
	m.readOnly = true
	tmp := filepath.Join(m.home, "tmp")
	if err := os.Chmod(tmp, 0500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(tmp, 0700)
	if probe, err := os.MkdirTemp(tmp, "copy-probe-"); err == nil {
		os.Remove(probe)
		t.Fatal("fixture does not deny temporary copies")
	}
	before := durableDigests(t, m.home)
	output, code := m.runUnder(t, nil, "exec", "SELECT content FROM plugin_roca_ops.memories WHERE content LIKE '%harbor lanterns%'", "--json")
	if code != 0 || !strings.Contains(output, "the cost-lab memory about harbor lanterns") {
		t.Fatalf("D1: read-only result with copies denied: code=%d %s", code, output)
	}
	if after := durableDigests(t, m.home); !reflect.DeepEqual(before, after) {
		t.Fatal("D1: read-only query changed durable database bytes")
	}
}

func durableDigests(t *testing.T, home string) map[string][32]byte {
	t.Helper()
	result := map[string][32]byte{}
	err := filepath.WalkDir(filepath.Join(home, ".roca"), func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !(strings.HasSuffix(path, ".db") || strings.HasSuffix(path, ".db-wal")) {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		// An empty WAL contains no durable data; readers may create one with SHM.
		if len(data) > 0 {
			result[path] = sha256.Sum256(data)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result) == 0 {
		t.Fatal("fixture has no durable database bytes")
	}
	return result
}

func costLab(t *testing.T) *world {
	t.Helper()
	m := aWorldIn(t, "cost")
	live, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Clean(m.home) == filepath.Clean(live) {
		t.Fatal("cost lab home is the operator home")
	}
	if err := m.runInit(); err != nil {
		t.Fatalf("lab init: %v\n%s", err, m.last.stderr)
	}
	if m.last.code != 0 {
		t.Fatalf("lab init: code %d\n%s", m.last.code, m.last.stderr)
	}
	if err := m.storeMemoryFixture("project", "the cost-lab memory about harbor lanterns"); err != nil {
		t.Fatalf("lab store: %v\n%s", err, m.last.stderr)
	}
	if err := growLabDatabase(t, m); err != nil {
		t.Fatal(err)
	}
	return m
}

func growLabDatabase(t *testing.T, m *world) error {
	t.Helper()
	path := filepath.Join(m.home, ".roca", "plugins", "roca-corpus", "roca-corpus.db")
	if _, err := os.Stat(path); err != nil {
		path = filepath.Join(m.home, ".roca", "roca.db")
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return err
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS cost_lab_filler (id INTEGER PRIMARY KEY, body TEXT)`); err != nil {
		return err
	}
	blob := strings.Repeat("harbor-lantern-cost-fixture\n", 1024)
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	stmt, err := tx.Prepare(`INSERT INTO cost_lab_filler(body) VALUES (?)`)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	for i := 0; i < 256; i++ {
		if _, err := stmt.Exec(blob); err != nil {
			_ = stmt.Close()
			_ = tx.Rollback()
			return err
		}
	}
	if err := stmt.Close(); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}
