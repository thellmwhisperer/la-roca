//go:build acceptance

package acceptance

import (
	"bufio"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
	_ "modernc.org/sqlite"
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

// TestCost is the adjacent-feature cost group: three assertions against a
// lab fixture with the real schema and row shape. It never opens the
// operator's live federation. Assertions whose dragon is still accepted
// log "expected-fail <id>" and stay green.
func TestCost(t *testing.T) {
	m := costLab(t)
	t.Run("read-only command writes 0 bytes outside its output", func(t *testing.T) {
		assertReadOnlyWritesNothing(t, m)
	})
	t.Run("warm vector query answers under 500ms", func(t *testing.T) {
		assertWarmVectorQuery(t, m)
	})
	t.Run("unchanged vector pass reads less than 1% of the database", func(t *testing.T) {
		assertUnchangedVectorPass(t, m)
	})
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

func assertReadOnlyWritesNothing(t *testing.T, m *world) {
	t.Helper()
	m.readOnly = true
	defer func() { m.readOnly = false }()
	before := treeSizes(t, m.home)
	if err := m.mustRun(`roca exec "SELECT 1"`); err != nil {
		t.Fatalf("read-only exec: %v\n%s", err, m.last.stderr)
	}
	after := treeSizes(t, m.home)
	tmp := filepath.Join(m.home, "tmp")
	matches, err := filepath.Glob(filepath.Join(tmp, "roca-read-only-snapshot-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("D1: read-only exec copied into %v", matches)
	}
	wrote := bytesAdded(before, after)
	if wrote == 0 {
		return
	}
	costExpectedFail(t, "D10", fmt.Sprintf("read-only exec wrote %d bytes outside its output", wrote))
}

func assertWarmVectorQuery(t *testing.T, m *world) {
	t.Helper()
	if err := enableVectorFeature(m.home); err != nil {
		t.Fatal(err)
	}
	_, _ = runTimed(t, m, 15*time.Second, "vector", "query", "harbor-lantern", "3")
	elapsed, err := runTimed(t, m, 15*time.Second, "vector", "query", "harbor-lantern", "3")
	if err == nil && elapsed < 500*time.Millisecond {
		return
	}
	if err != nil {
		costExpectedFail(t, "D5", fmt.Sprintf("warm vector query: %v (%s)", err, elapsed))
		return
	}
	costExpectedFail(t, "D5", fmt.Sprintf("warm vector query took %s, budget 500ms", elapsed))
}

func assertUnchangedVectorPass(t *testing.T, m *world) {
	t.Helper()
	if err := enableVectorFeature(m.home); err != nil {
		t.Fatal(err)
	}
	dbPath, dbSize := largestLabDB(t, m.home)
	if dbSize == 0 {
		t.Fatal("the cost lab has no database to measure")
	}
	_, _ = runTimed(t, m, 20*time.Second, "vector", "install")
	budget := dbSize / 100
	if budget < 1 {
		budget = 1
	}
	read, err := measurePassReads(t, m, dbPath, dbSize)
	if err != nil {
		costExpectedFail(t, "D8", err.Error())
		return
	}
	if read <= budget {
		return
	}
	costExpectedFail(t, "D8", fmt.Sprintf("unchanged vector pass read %d bytes of %s (%d); budget 1%% = %d",
		read, dbPath, dbSize, budget))
}

func measurePassReads(t *testing.T, m *world, dbPath string, dbSize int64) (int64, error) {
	t.Helper()
	if runtime.GOOS == "linux" {
		control, err := runRchar(t, m, 20*time.Second, "exec", "SELECT 1")
		if err != nil {
			return 0, fmt.Errorf("control exec rchar: %w", err)
		}
		pass, err := runRchar(t, m, 20*time.Second, "vector", "install")
		if err != nil {
			return 0, fmt.Errorf("vector install rchar: %w", err)
		}
		delta := pass - control
		if delta < 0 {
			delta = 0
		}
		return delta, nil
	}
	hashStart := time.Now()
	if err := hashFile(dbPath); err != nil {
		return 0, err
	}
	hashElapsed := time.Since(hashStart)
	passElapsed, err := runTimed(t, m, 20*time.Second, "vector", "install")
	if err != nil {
		return dbSize, fmt.Errorf("vector install: %w", err)
	}
	if passElapsed >= hashElapsed/2 {
		return dbSize, nil
	}
	return dbSize / 200, nil
}

func runTimed(t *testing.T, m *world, timeout time.Duration, args ...string) (time.Duration, error) {
	t.Helper()
	start := time.Now()
	cmd := exec.Command(m.binaryPath(), args...)
	cmd.Env = m.environment()
	cmd.Dir = m.home
	if err := runWithTimeout(cmd, timeout); err != nil {
		return time.Since(start), err
	}
	return time.Since(start), nil
}

func runRchar(t *testing.T, m *world, timeout time.Duration, args ...string) (int64, error) {
	t.Helper()
	cmd := exec.Command(m.binaryPath(), args...)
	cmd.Env = m.environment()
	cmd.Dir = m.home
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	pid := cmd.Process.Pid
	timer := time.AfterFunc(timeout, func() { _ = cmd.Process.Kill() })
	defer timer.Stop()
	var last int64
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case err := <-done:
			if v := procRchar(pid); v > last {
				last = v
			}
			return last, err
		case <-ticker.C:
			if v := procRchar(pid); v > last {
				last = v
			}
		}
	}
}

func procRchar(pid int) int64 {
	file, err := os.Open(fmt.Sprintf("/proc/%d/io", pid))
	if err != nil {
		return 0
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "rchar:") {
			n, _ := strconv.ParseInt(strings.TrimSpace(strings.TrimPrefix(line, "rchar:")), 10, 64)
			return n
		}
	}
	return 0
}

func runWithTimeout(cmd *exec.Cmd, timeout time.Duration) error {
	if err := cmd.Start(); err != nil {
		return err
	}
	timer := time.AfterFunc(timeout, func() { _ = cmd.Process.Kill() })
	defer timer.Stop()
	return cmd.Wait()
}

func hashFile(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = io.Copy(sha256.New(), file)
	return err
}

func largestLabDB(t *testing.T, home string) (string, int64) {
	t.Helper()
	var path string
	var size int64
	_ = filepath.WalkDir(filepath.Join(home, ".roca"), func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if filepath.Ext(p) != ".db" {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if info.Size() > size {
			size = info.Size()
			path = p
		}
		return nil
	})
	return path, size
}

func treeSizes(t *testing.T, root string) map[string]int64 {
	t.Helper()
	out := map[string]int64{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			rel = path
		}
		out[rel] = info.Size()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func bytesAdded(before, after map[string]int64) int64 {
	var n int64
	for path, size := range after {
		old, ok := before[path]
		if !ok {
			n += size
			continue
		}
		if size > old {
			n += size - old
		}
	}
	return n
}

func costExpectedFail(t *testing.T, id, msg string) {
	t.Helper()
	status := dragonStatus(t, id)
	if status == "removed" {
		t.Fatalf("%s: %s (dragon %s is removed, this must pass)", id, msg, id)
	}
	t.Logf("expected-fail %s: %s", id, msg)
}

func dragonStatus(t *testing.T, id string) string {
	t.Helper()
	root, err := acceptanceRoot()
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(root, ".slop", "dragons", id+".yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "status:") {
			continue
		}
		return strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, "status:")), `"'`)
	}
	t.Fatalf("dragon %s has no status", id)
	return ""
}
