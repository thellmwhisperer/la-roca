//go:build acceptance

package acceptance

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The seams the sandbox never crosses and a real machine does.
//
// Every scenario in this suite runs under a temporary HOME made of ASCII with no
// spaces in it, in the suite runner's own timezone, with `roca mcp serve` never
// running. Those three assumptions are exactly the ones an operator breaks on
// the first day, so they are measured here against the real binary.

// A timezone fourteen hours from the runner's and a locale whose rules for
// upper case are not the ASCII ones. What the product stores has to be the same
// instant either way: two installations whose timestamps drift with the
// operator's TZ cannot have their corpora compared, or merged, or aged.
func TestTheStoredClockDoesNotMoveWithTheOperatorsTimezone(t *testing.T) {
	m := aWorldIn(t, "husos")
	if err := m.mustRun(m.initCommand(false)); err != nil {
		t.Fatalf("init: %v\n%s", err, m.last.stderr)
	}

	// Kiritimati is +14 and Etc/GMT+12 is -12: twenty-six hours apart, so a
	// clock that followed the operator would land on two different days.
	// Turkish is the locale whose upper case of `i` is not `I`, which is what
	// breaks a fold that asks the platform instead of doing it itself.
	for _, zone := range []string{"Pacific/Kiritimati", "Etc/GMT+12"} {
		locale := []string{"TZ=" + zone, "LC_ALL=tr_TR.UTF-8", "LANG=tr_TR.UTF-8"}
		output, code := m.runUnder(t, locale, "store", "--layer", "project",
			"--content", "la nota del huso "+zone+" con longitud suficiente")
		if code != 0 {
			t.Fatalf("store under TZ=%s: code %d\n%s", zone, code, output)
		}
	}

	if err := m.mustRun("roca exec 'SELECT created_at FROM memories'"); err != nil {
		t.Fatalf("exec: %v\n%s", err, m.last.stderr)
	}
	stamps := theStamps(m.last.stdout)
	if len(stamps) < 2 {
		t.Fatalf("fewer than two memories were stored:\n%s", m.last.stdout)
	}
	if stamps[0][:13] != stamps[1][:13] {
		t.Errorf("the clock moved with the timezone: %q and %q", stamps[0], stamps[1])
	}
}

// `roca mcp serve` is a foreground process the agent owns, not a daemon, so the
// purge has nothing to stop. What it must not do is fail because of it, and what
// it must do is converge: a second run on the machine the first one left behind
// ends clean.
func TestAPurgeWithServeAliveDoesNotFailAndConverges(t *testing.T) {
	m := aWorldIn(t, "serve-alive")
	if err := m.installBinary(); err != nil {
		t.Fatal(err)
	}
	if err := m.mustRun(m.initCommand(false)); err != nil {
		t.Fatalf("init: %v\n%s", err, m.last.stderr)
	}

	// The real MCP over stdio, with its input held open so it stays alive with
	// the database in its hands.
	serve := exec.Command(m.binaryPath(), "mcp", "serve")
	serve.Env = m.environment()
	held, err := serve.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := serve.Start(); err != nil {
		t.Fatalf("roca mcp serve: %v", err)
	}
	t.Cleanup(func() { held.Close(); serve.Process.Kill(); serve.Wait() })
	time.Sleep(300 * time.Millisecond)

	// A copy of its own for each run, because `roca uninstall` deletes the binary
	// it is running from (PRD I3) and the live `serve` needs the installed one to
	// stay where it is.
	if code := m.purgeWith(t, aPurgerCopy(t, m.binary)); code != 0 {
		t.Fatalf("the purge failed with serve alive (code %d):\n%s\n%s",
			code, m.last.stdout, m.last.stderr)
	}

	held.Close()
	serve.Wait()

	// The second run over whatever the first one and the live process left.
	if code := m.purgeWith(t, aPurgerCopy(t, m.binary)); code != 0 {
		t.Fatalf("the second purge failed (code %d):\n%s\n%s",
			code, m.last.stdout, m.last.stderr)
	}
	if strings.Contains(m.last.stdout, "did not create it") {
		t.Errorf("the purge reports its own files as somebody else's:\n%s", m.last.stdout)
	}
	if _, err := os.Stat(filepath.Join(m.home, ".roca")); !os.IsNotExist(err) {
		left, _ := os.ReadDir(filepath.Join(m.home, ".roca"))
		var names []string
		for _, entry := range left {
			names = append(names, entry.Name())
		}
		t.Errorf("the data directory survived two purges carrying %v", names)
	}
}

// --- helpers ---

// aWorldIn is one installation under a HOME the caller names, so a case can put
// whatever a real home directory is allowed to carry into that name.
func aWorldIn(t *testing.T, name string) *world {
	t.Helper()
	binary, err := rocaBinary()
	if err != nil {
		t.Fatalf("I cannot find the binary: %v", err)
	}
	home := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(filepath.Join(home, "tmp"), 0o700); err != nil {
		t.Fatal(err)
	}
	// The zero model world is `ROCA_MODELS_ORDER=none`, which is what keeps these
	// cases from measuring whatever Ollama the machine happens to be running.
	return &world{binary: binary, home: home}
}

// aPurgerCopy is one binary for one uninstall to delete itself with.
func aPurgerCopy(t *testing.T, binary string) string {
	t.Helper()
	copied := filepath.Join(t.TempDir(), "roca")
	if err := os.WriteFile(copied, mustRead(binary), 0o755); err != nil {
		t.Fatal(err)
	}
	return copied
}

// purgeWith runs a non-interactive purge from a binary of the caller's choosing.
func (m *world) purgeWith(t *testing.T, binary string) int {
	t.Helper()
	command := exec.Command(binary, "uninstall", "--purge")
	if err := m.record("roca uninstall --purge", command); err != nil {
		t.Fatal(err)
	}
	return m.last.code
}

// runUnder runs the binary with the scenario's environment plus the case's own,
// which is how a seam varies the machine instead of the command.
func (m *world) runUnder(t *testing.T, environment []string, arguments ...string) (string, int) {
	t.Helper()
	command := exec.Command(m.binaryPath(), arguments...)
	command.Env = append(m.environment(), environment...)

	output, err := command.CombinedOutput()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return string(output), exit.ExitCode()
	}
	if err != nil {
		t.Fatalf("run %v: %v", arguments, err)
	}
	return string(output), 0
}

// theStamps reads the created_at values out of the readable table `roca exec`
// prints.
func theStamps(output string) []string {
	var stamps []string
	inRows := false
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, "rows[") && strings.HasSuffix(line, "{created_at}:") {
			inRows = true
			continue
		}
		if !inRows {
			continue
		}
		if !strings.HasPrefix(line, "  ") {
			break
		}
		value := strings.TrimSpace(line)
		if unquoted, err := strconv.Unquote(value); err == nil {
			value = unquoted
		}
		stamps = append(stamps, value)
	}
	return stamps
}
