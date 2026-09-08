//go:build acceptance

package acceptance

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/test/testfixture"
)

func TestVectorCLIQueryResidentEvidence(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shared residency is proven on unix sockets")
	}
	lab := strings.TrimSpace(os.Getenv("ROCA_VECTOR_LAB_HOME"))
	if lab == "" {
		t.Skip("set ROCA_VECTOR_LAB_HOME to a lab copy of the ops federation (never the live ~/.roca)")
	}
	if filepath.Clean(lab) == filepath.Clean(os.Getenv("HOME")) {
		t.Fatal("ROCA_VECTOR_LAB_HOME resolved to the live home")
	}
	query := []string{"vector", "query", "member of technical staff", "3", "--databases", "ops"}
	t.Run("published", func(t *testing.T) {
		binary := publishedRoca(t)
		evidence := measureCLIVectorQuery(t, binary, lab, query, 3, false)
		writeCLIResidentEvidence(t, "published", evidence)
		if evidence.warmMedianMS < 500 {
			t.Fatalf("published warm median = %d ms, want the pre-fix load (at least 500 ms)\n%s",
				evidence.warmMedianMS, evidence.transcript)
		}
	})
	t.Run("branch", func(t *testing.T) {
		binary, err := rocaBinary()
		if err != nil {
			t.Fatal(err)
		}
		evidence := measureCLIVectorQuery(t, binary, lab, query, 5, true)
		writeCLIResidentEvidence(t, "branch", evidence)
		if evidence.warmMedianMS >= 500 {
			t.Fatalf("branch warm median = %d ms, want under 500 ms\n%s",
				evidence.warmMedianMS, evidence.transcript)
		}
		if evidence.logDelta < 1 {
			t.Fatalf("branch query did not reach the resident log\n%s", evidence.transcript)
		}
	})
}

type cliResidentEvidence struct {
	coldMS       int64
	warmRunsMS   []int64
	warmMedianMS int64
	logDelta     int
	residentPS   string
	cliMaxRSS    int64
	transcript   string
}

func measureCLIVectorQuery(t *testing.T, binary, lab string, args []string, warmRuns int, wantResident bool) cliResidentEvidence {
	t.Helper()
	home := lab
	binDir := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	dataDir := filepath.Join(home, ".roca")
	socketDir, err := os.MkdirTemp("/tmp", "rv-335-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(socketDir, 0o700); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(socketDir, "resident.sock")
	logPath := filepath.Join(dataDir, "logs", "vector-resident.log")
	t.Cleanup(func() {
		testfixture.KillResidents(socket)
		_ = os.RemoveAll(socketDir)
	})
	env := isolatedLabEnv(home, binDir, filepath.Dir(binary), socket)
	original := binary
	labBinary, err := installLabRoca(binary, binDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := ensureLabCompanion(t, original, home, binDir, env); err != nil {
		t.Fatal(err)
	}
	binary = labBinary
	_ = os.MkdirAll(filepath.Dir(logPath), 0o700)
	beforeLog := countFileLines(logPath)

	var transcript strings.Builder
	fmt.Fprintf(&transcript, "binary: %s\nlab: %s\nargs: %s\n", binary, lab, strings.Join(args, " "))
	fmt.Fprintf(&transcript, "machine: cold first call starts or misses the resident; later calls are warm.\n")
	fmt.Fprintf(&transcript, "scope: ops only (companion corpus defect excluded)\n")

	cold := timeLabQuery(t, binary, args, env)
	fmt.Fprintf(&transcript, "cold real_ms=%d maxrss_bytes=%d out=%q err=%q\n",
		cold.realMS, cold.maxRSS, trimForEvidence(cold.stdout), trimForEvidence(cold.stderr))
	warms := make([]int64, 0, warmRuns)
	var last timedRun
	for i := 0; i < warmRuns; i++ {
		run := timeLabQuery(t, binary, args, env)
		warms = append(warms, run.realMS)
		last = run
		fmt.Fprintf(&transcript, "warm[%d] real_ms=%d maxrss_bytes=%d out=%q err=%q\n",
			i, run.realMS, run.maxRSS, trimForEvidence(run.stdout), trimForEvidence(run.stderr))
	}
	afterLog := countFileLines(logPath)
	psOutput := testfixture.ResidentPS(t, socket)
	if testfixture.CountResidentLines(psOutput) == 0 {
		psOutput = testfixture.ResidentPS(t, home)
	}
	fmt.Fprintf(&transcript, "resident_log_lines before=%d after=%d\nps:\n%s\n", beforeLog, afterLog, psOutput)
	if wantResident && testfixture.CountResidentLines(psOutput) < 1 {
		t.Fatalf("branch left no resident process\n%s", transcript.String())
	}
	median := medianInt64(warms)
	fmt.Fprintf(&transcript, "warm_median_ms=%d\n", median)
	return cliResidentEvidence{
		coldMS: cold.realMS, warmRunsMS: warms, warmMedianMS: median,
		logDelta: afterLog - beforeLog, residentPS: psOutput, cliMaxRSS: last.maxRSS,
		transcript: transcript.String(),
	}
}

type timedRun struct {
	realMS int64
	maxRSS int64
	stdout string
	stderr string
}

func timeLabQuery(t *testing.T, binary string, args []string, env []string) timedRun {
	t.Helper()
	command := exec.Command("/usr/bin/time", append([]string{"-p", binary}, args...)...)
	command.Env = env
	output, err := command.CombinedOutput()
	text := string(output)
	run := timedRun{stdout: text, stderr: text}
	run.realMS = parseTimeRealMS(text)
	run.maxRSS = parseTimeMaxRSS(text)
	if err != nil && run.realMS == 0 {
		t.Fatalf("time %s %s: %v\n%s", binary, strings.Join(args, " "), err, text)
	}
	return run
}

func isolatedLabEnv(home, binDir, binaryDir, socket string) []string {
	path := binDir + string(os.PathListSeparator) + binaryDir +
		string(os.PathListSeparator) + "/usr/bin" + string(os.PathListSeparator) + "/bin"
	env := []string{
		"HOME=" + home,
		"PATH=" + path,
		"TMPDIR=" + filepath.Join(home, "tmp"),
		"ROCA_PREFIX=" + binDir,
		"ROCA_VECTOR_RESIDENT_SOCKET=" + socket,
	}
	for _, item := range os.Environ() {
		switch {
		case strings.HasPrefix(item, "HOME="),
			strings.HasPrefix(item, "PATH="),
			strings.HasPrefix(item, "ROCA_VECTOR_RESIDENT_SOCKET="),
			strings.HasPrefix(item, "ROCA_VECTOR_RESIDENT_BINARY="),
			strings.HasPrefix(item, "ROCA_PREFIX="):
			continue
		default:
			env = append(env, item)
		}
	}
	_ = os.MkdirAll(filepath.Join(home, "tmp"), 0o700)
	return env
}

func installLabRoca(binary, binDir string) (string, error) {
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return "", err
	}
	dest := filepath.Join(binDir, "roca")
	raw, err := os.ReadFile(binary)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(dest, raw, 0o755); err != nil {
		return "", err
	}
	return dest, nil
}

func ensureLabCompanion(t *testing.T, binary, home, binDir string, env []string) error {
	t.Helper()
	state := filepath.Join(home, ".roca", "plugins", "roca-vector", "state")
	if err := os.MkdirAll(state, 0o700); err != nil {
		return err
	}
	dest := filepath.Join(binDir, "roca-vector")
	candidates := []string{filepath.Join(filepath.Dir(binary), "roca-vector")}
	if root, err := acceptanceRoot(); err == nil {
		candidates = append(candidates, filepath.Join(root, ".tmp", "roca-vector-native"))
	}
	var src string
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			src = candidate
			break
		}
	}
	if src == "" {
		return fmt.Errorf("no roca-vector companion next to %s", binary)
	}
	input, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.WriteFile(dest, input, 0o755); err != nil {
		return err
	}
	command := exec.Command(binary, "vector", "status")
	command.Env = env
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("vector status in lab: %w\n%s", err, output)
	}
	return nil
}

func parseTimeRealMS(text string) int64 {
	for _, line := range strings.Split(text, "\n") {
		fields := strings.Fields(line)
		for i, field := range fields {
			if field != "real" || i == 0 {
				continue
			}
			seconds, err := strconv.ParseFloat(fields[i-1], 64)
			if err != nil {
				continue
			}
			return int64(seconds * 1000)
		}
		if len(fields) >= 2 && fields[0] == "real" {
			seconds, err := strconv.ParseFloat(fields[1], 64)
			if err != nil {
				continue
			}
			return int64(seconds * 1000)
		}
	}
	return 0
}

func parseTimeMaxRSS(text string) int64 {
	scanner := bufio.NewScanner(strings.NewReader(text))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.Contains(line, "maximum resident set size") {
			fields := strings.Fields(line)
			if len(fields) == 0 {
				return 0
			}
			value, err := strconv.ParseInt(fields[0], 10, 64)
			if err != nil {
				return 0
			}
			return value
		}
	}
	return 0
}

func countFileLines(path string) int {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	if len(raw) == 0 {
		return 0
	}
	return strings.Count(string(raw), "\n")
}

func medianInt64(values []int64) int64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]int64(nil), values...)
	for i := 1; i < len(sorted); i++ {
		for j := i; j > 0 && sorted[j] < sorted[j-1]; j-- {
			sorted[j], sorted[j-1] = sorted[j-1], sorted[j]
		}
	}
	return sorted[len(sorted)/2]
}

func trimForEvidence(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	if len(text) > 240 {
		return text[:240] + "…"
	}
	return text
}

func writeCLIResidentEvidence(t *testing.T, name string, evidence cliResidentEvidence) {
	t.Helper()
	t.Logf("issue 335 %s evidence:\n%s", name, evidence.transcript)
	dir := os.Getenv("ROCA_CLI_RESIDENT_EVIDENCE_DIR")
	if dir == "" {
		dir = os.Getenv("ROCA_RESIDENT_EVIDENCE_DIR")
	}
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf("point: %s\ncold_ms: %d\nwarm_median_ms: %d\nwarm_runs_ms: %v\nlog_delta: %d\ncli_maxrss: %d\nps:\n%s\n%s",
		name, evidence.coldMS, evidence.warmMedianMS, evidence.warmRunsMS, evidence.logDelta, evidence.cliMaxRSS,
		evidence.residentPS, evidence.transcript)
	if err := os.WriteFile(filepath.Join(dir, name+"-cli-query.txt"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}
