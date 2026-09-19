//go:build acceptance

package acceptance

import (
	"os/exec"
	"strings"
	"testing"
	"time"
)

const issue432RunawaySQL = `WITH RECURSIVE costly(n) AS (SELECT 1 UNION ALL SELECT n + 1 FROM costly WHERE n < 100000000) SELECT sum(n), (SELECT count(*) FROM plugin_roca_corpus.exchanges) AS exchanges FROM costly`

func TestIssue432ExecTimeLimitOnInstalledBinary(t *testing.T) {
	binary, err := rocaBinary()
	if err != nil {
		t.Fatalf("I cannot find the binary: %v", err)
	}
	home, world := initializedDistributionHome(t, "roca-exec-timeout-", binary)

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
	time.Sleep(50 * time.Millisecond)
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
