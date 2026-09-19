package cli

import (
	"strings"
	"testing"
	"time"
)

const runawaySelect = `WITH RECURSIVE costly(n) AS (SELECT 1 UNION ALL SELECT n + 1 FROM costly WHERE n < 100000000) SELECT sum(n) FROM costly`

func TestExecCancelsARunawaySelectAtTheTimeLimit(t *testing.T) {
	fixtureInstallation(t)
	started := time.Now()
	out, err := runRootErr(t, contractBuild(), nil, "exec", runawaySelect, "--timeout-ms", "50")
	elapsed := time.Since(started)
	if err == nil {
		t.Fatalf("runaway exec succeeded:\n%s", out)
	}
	if !strings.Contains(err.Error(), "the validated SQL exceeded the time limit after") {
		t.Fatalf("timeout message = %v\n%s", err, out)
	}
	if elapsed > time.Second {
		t.Fatalf("exec timeout took %s", elapsed)
	}
}

func TestDoctorCompletesWhileABoundedExecIsRunning(t *testing.T) {
	fixtureInstallation(t)
	done := make(chan error, 1)
	go func() {
		_, err := runRootErr(t, contractBuild(), nil, "exec", runawaySelect, "--timeout-ms", "200")
		done <- err
	}()
	time.Sleep(30 * time.Millisecond)
	out, err := runRootErr(t, contractBuild(), nil, "doctor")
	combined := out
	if err != nil {
		combined += err.Error()
	}
	if strings.Contains(combined, "SQLITE_BUSY") {
		t.Fatalf("doctor hit SQLITE_BUSY while exec was bounded:\n%s", combined)
	}
	execErr := <-done
	if execErr == nil {
		t.Fatal("exec completed without the time limit")
	}
	if !strings.Contains(execErr.Error(), "the validated SQL exceeded the time limit after") {
		t.Fatalf("exec = %v", execErr)
	}
}
