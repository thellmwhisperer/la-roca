//go:build acceptance

package acceptance

import (
	"database/sql"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

const issue432RunawaySQL = `WITH RECURSIVE costly(n) AS (SELECT min(id) FROM main.memories UNION ALL SELECT n + 1 FROM costly CROSS JOIN main.memories WHERE n < 100000000) SELECT sum(n), (SELECT count(*) FROM plugin_roca_corpus.exchanges) AS exchanges FROM costly`

func TestIssue432ExecTimeLimitOnInstalledBinary(t *testing.T) {
	binary, err := rocaBinary()
	if err != nil {
		t.Fatalf("I cannot find the binary: %v", err)
	}
	home, world := initializedDistributionHome(t, "roca-exec-timeout-", binary)
	seedMainWAL(t, home)

	started := time.Now()
	run := world.runAt(home, binary, "exec", issue432RunawaySQL)
	elapsed := time.Since(started)
	combined := run.stdout + run.stderr
	if run.code == 0 {
		t.Fatalf("runaway exec succeeded in %s:\n%s", elapsed, combined)
	}
	if !strings.Contains(combined, "the validated SQL exceeded the time limit after 5s") {
		t.Fatalf("timeout message missing after %s: code %d\n%s", elapsed, run.code, combined)
	}
	if elapsed > 6*time.Second {
		t.Fatalf("exec ran %s, want <= 6s", elapsed)
	}

	done := make(chan distributionRun, 1)
	go func() {
		done <- world.runAt(home, binary, "exec", issue432RunawaySQL)
	}()
	waitForExecReader(t, home, done)
	seedPendingAdoption(t, home)
	doctor := world.runAt(home, binary, "doctor")
	doctorOut := doctor.stdout + doctor.stderr
	if doctor.code != 0 {
		t.Fatalf("doctor failed while exec was bounded: code %d\n%s", doctor.code, doctorOut)
	}
	if strings.Contains(doctorOut, "SQLITE_BUSY") {
		t.Fatalf("doctor hit SQLITE_BUSY while exec was bounded:\n%s", doctorOut)
	}
	execRun := <-done
	if execRun.code == 0 {
		t.Fatal("overlapping exec completed without the time limit")
	}
	if !strings.Contains(execRun.stdout+execRun.stderr, "the validated SQL exceeded the time limit after 5s") {
		t.Fatalf("overlapping exec did not time out:\n%s%s", execRun.stdout, execRun.stderr)
	}

	ps := exec.Command("ps", "-axo", "etime,command")
	out, err := ps.Output()
	if err != nil {
		t.Fatalf("ps: %v", err)
	}
	marker := binary + " exec"
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.Contains(line, marker) {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if etimeOlderThan(fields[0], 10*time.Second) {
			t.Fatalf("roca exec still running after the bound: %s", line)
		}
	}
}

func waitForExecReader(t *testing.T, home string, done <-chan distributionRun) {
	t.Helper()
	paths := []string{filepath.Join(home, ".roca", "roca.db")}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case run := <-done:
			t.Fatalf("exec finished before holding a database read lock: %d\n%s%s", run.code, run.stdout, run.stderr)
		default:
		}
		for _, path := range paths {
			blocked, err := sqliteReaderActive(path)
			if err != nil {
				t.Fatalf("probe %s: %v", path, err)
			}
			if blocked {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("exec never established a database read lock")
}

func seedMainWAL(t *testing.T, home string) {
	t.Helper()
	path := filepath.Join(home, ".roca", "roca.db")
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO memories(layer, content, origin)
		VALUES ('discovery', 'exec timeout lock probe', 'agent')`); err != nil {
		t.Fatalf("seed WAL: %v", err)
	}
}

func seedPendingAdoption(t *testing.T, home string) {
	t.Helper()
	path := filepath.Join(home, ".roca", "roca.db")
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(0)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("DROP INDEX idx_memories_layer"); err != nil {
		t.Fatalf("seed pending adoption: %v", err)
	}
}

func sqliteReaderActive(path string) (bool, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(0)")
	if err != nil {
		return false, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`UPDATE memories SET metadata = COALESCE(metadata, '')
		WHERE id = (SELECT min(id) FROM memories)`); err != nil {
		if sqliteBusy(err) {
			return true, nil
		}
		return false, err
	}
	var busy, logFrames, checkpointed int
	if err := db.QueryRow("PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &logFrames, &checkpointed); err != nil {
		if sqliteBusy(err) {
			return true, nil
		}
		return false, err
	}
	return busy != 0, nil
}

func sqliteBusy(err error) bool {
	if err == nil {
		return false
	}
	upper := strings.ToUpper(err.Error())
	return strings.Contains(upper, "BUSY") || strings.Contains(upper, "LOCKED")
}

func etimeOlderThan(etime string, limit time.Duration) bool {
	etime = strings.TrimSpace(etime)
	parts := strings.Split(etime, "-")
	var days time.Duration
	if len(parts) == 2 {
		days = time.Duration(atoi(parts[0])) * 24 * time.Hour
		etime = parts[1]
	}
	chunks := strings.Split(etime, ":")
	var elapsed time.Duration
	switch len(chunks) {
	case 3:
		elapsed = time.Duration(atoi(chunks[0]))*time.Hour +
			time.Duration(atoi(chunks[1]))*time.Minute +
			time.Duration(atoi(chunks[2]))*time.Second
	case 2:
		elapsed = time.Duration(atoi(chunks[0]))*time.Minute +
			time.Duration(atoi(chunks[1]))*time.Second
	default:
		return false
	}
	return days+elapsed > limit
}

func atoi(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
	}
	return n
}
