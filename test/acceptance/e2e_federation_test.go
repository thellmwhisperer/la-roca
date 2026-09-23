//go:build acceptance

package acceptance

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/cucumber/godog"
	"github.com/thellmwhisperer/la-roca/internal/distribution/bundledplugin"
)

const (
	frozenSnapshotRel = "testdata/e2e-federation/frozen"
	frozenDigestRel   = "testdata/e2e-federation/frozen.sha256"
	vectorModelSHA    = "a5db3381f2e514d3490a3a31fe70eb1a65e95016c85c6c2c23223b810806594f"
	hookSessionInput  = `{"hook_event_name":"SessionStart","tool_name":"","tool_input":{}}`
)

func TestFrozenFederationBytesArePinned(t *testing.T) {
	root, err := acceptanceRoot()
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyFrozenDigest(root); err != nil {
		t.Fatal(err)
	}
	for _, snapshot := range []string{"main", "pill-free", "pr321", "pr324"} {
		db := filepath.Join(root, frozenSnapshotRel, snapshot, ".roca", "roca.db")
		if _, err := os.Stat(db); err != nil {
			t.Fatalf("frozen snapshot %s has no core database: %v", snapshot, err)
		}
	}
}

func TestFrozenFederationMachineLabelsAreSynthetic(t *testing.T) {
	root := mustAcceptanceRoot(t)
	for _, snapshot := range []string{"main", "pill-free"} {
		t.Run(snapshot, func(t *testing.T) {
			path := filepath.Join(root, frozenSnapshotRel, snapshot, ".roca", "plugins", "roca-corpus", "roca-corpus.db")
			db, err := bundledplugin.OpenDatabase(path, true)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			for _, table := range []string{"sessions", "exchanges"} {
				var total, synthetic int
				if err := db.QueryRow("SELECT COUNT(*), COUNT(CASE WHEN machine = 'synthetic-e2e' THEN 1 END) FROM "+table).
					Scan(&total, &synthetic); err != nil {
					t.Fatal(err)
				}
				if total == 0 || synthetic != total {
					t.Fatalf("%s: %d of %d rows have a synthetic machine label", table, synthetic, total)
				}
			}
			var integrity string
			if err := db.QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil || integrity != "ok" {
				t.Fatalf("frozen corpus integrity = %q: %v", integrity, err)
			}
		})
	}
}

type federationLab struct {
	m *world
}

func installFrozenVectorModel(home string) error {
	source := strings.TrimSpace(os.Getenv("ROCA_E2E_VECTOR_MODEL"))
	if source == "" {
		return nil
	}
	info, err := os.Stat(source)
	if err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("pinned embedding model %s is not a regular file: %w", source, err)
	}
	directory := filepath.Join(home, ".roca", "models", "nomic-embed-text-v2-moe")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	return os.Symlink(source, filepath.Join(directory, vectorModelSHA+".gguf"))
}

func prepareFrozenVectorState(home string) error {
	_ = os.Remove(filepath.Join(home, ".roca", "plugins", ".roca-vector.relocation.lock"))
	return os.MkdirAll(filepath.Join(home, ".roca", "plugins", "roca-vector", "state"), 0o700)
}

func (lab *federationLab) installPrefix() error {
	target := theInstalledBinary(lab.m.home)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	raw, err := os.ReadFile(lab.m.binary)
	if err != nil {
		return err
	}
	if err := os.WriteFile(target, raw, 0o755); err != nil {
		return err
	}
	binDir := filepath.Join(lab.m.home, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(binDir, "roca"), raw, 0o755); err != nil {
		return err
	}
	lab.m.installed = target
	command := exec.Command(target, "--db-path", filepath.Join(lab.m.home, ".roca", "roca.db"),
		"--json", "_install-bundled-plugins")
	if err := lab.m.record("roca _install-bundled-plugins", command); err != nil {
		return fmt.Errorf("install bundled plugins in frozen home: %w\n%s", err, lab.m.last.stderr)
	}
	if lab.m.last.code != 0 {
		return fmt.Errorf("install bundled plugins in frozen home: exit %d\n%s", lab.m.last.code, lab.m.last.stderr+lab.m.last.stdout)
	}
	vector, err := os.ReadFile(filepath.Join(filepath.Dir(target), "roca-vector"))
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(binDir, "roca-vector"), vector, 0o755)
}

func (m *world) outputDigitRunAtLeast(want int) error {
	got := longestDigitRun(m.last.stdout + m.last.stderr)
	if got < want {
		return fmt.Errorf("longest digit run=%d, want at least %d:\n%s", got, want, m.last.stdout+m.last.stderr)
	}
	return nil
}

func (m *world) jsonFieldIsString(field, want string) error {
	document, err := m.json()
	if err != nil {
		return err
	}
	value, ok := lookup(document, field)
	if !ok {
		return fmt.Errorf("the JSON output has no %q: %v", field, document)
	}
	got, ok := value.(string)
	if !ok || got != want {
		return fmt.Errorf("%s = %v (%T), want JSON string %q", field, value, value, want)
	}
	return nil
}

func jsonInteger(value any) (int64, error) {
	switch v := value.(type) {
	case float64:
		n := int64(v)
		if float64(n) != v {
			return 0, fmt.Errorf("not an integer: %v", v)
		}
		return n, nil
	case json.Number:
		return v.Int64()
	case int64:
		return v, nil
	default:
		return 0, fmt.Errorf("%T is not a JSON integer", value)
	}
}

func (m *world) jsonFieldIsJSSafeInteger(field string) error {
	document, err := m.json()
	if err != nil {
		return err
	}
	value, ok := lookup(document, field)
	if !ok {
		return fmt.Errorf("the JSON output has no %q: %v", field, document)
	}
	n, err := jsonInteger(value)
	if err != nil {
		return fmt.Errorf("%s = %v (%T), want a JSON integer: %w", field, value, value, err)
	}
	if n < 1 || n >= 1<<53 || len(strconv.FormatInt(n, 10)) > 12 {
		return fmt.Errorf("%s = %d, want a positive JS-safe integer of at most 12 digits", field, n)
	}
	return nil
}

func (m *world) jsonFieldRuneCountBetween(field string, low, high int) error {
	document, err := m.json()
	if err != nil {
		return err
	}
	value, ok := lookup(document, field)
	if !ok {
		return fmt.Errorf("the JSON output has no %q: %v", field, document)
	}
	text, ok := value.(string)
	if !ok {
		return fmt.Errorf("%s = %v (%T), want a JSON string", field, value, value)
	}
	n := utf8.RuneCountInString(text)
	if n < low || n > high {
		return fmt.Errorf("%s runes=%d, want %d-%d", field, n, low, high)
	}
	return nil
}

func (m *world) iExecSQLMaxCharsJSON(statement string, budget int) error {
	_, err := m.runWith("roca exec --json", []string{"exec", statement, "--max-chars", strconv.Itoa(budget), "--json"})
	return err
}

type codexIdentityCounts struct {
	sessions           int
	exactSourceSession int
	splitSiblings      int
	exchanges          int
	tools              int
	orphanTools        int
	failedTools        int
	controlSessions    int
}

const codexIdentitySQL = `SELECT
 (SELECT COUNT(*) FROM plugin_roca_corpus.sessions WHERE source_agent = 'codex') AS sessions,
 SUM(CASE WHEN session_id = '019aba72-aa57-7d93-a12c-b6e65c0dca6b' THEN 1 ELSE 0 END) AS exact_source_session,
 SUM(CASE WHEN session_id IN ('019aba72-aa57-7d93-a12c-b6e65c0dca60','019aba72-aa57-7d93-a12c-b6e65c0dca61') THEN 1 ELSE 0 END) AS split_siblings,
 (SELECT COUNT(*) FROM plugin_roca_corpus.exchanges WHERE session_id = '019aba72-aa57-7d93-a12c-b6e65c0dca6b') AS exchanges,
 (SELECT COUNT(*) FROM plugin_roca_corpus.tool_uses WHERE session_id = '019aba72-aa57-7d93-a12c-b6e65c0dca6b') AS tools,
 (SELECT COUNT(*) FROM plugin_roca_corpus.tool_uses WHERE session_id = '019aba72-aa57-7d93-a12c-b6e65c0dca6b' AND exchange_number IS NULL) AS orphan_tools,
 (SELECT COUNT(*) FROM plugin_roca_corpus.tool_uses WHERE had_error = 1 AND error_message = 'synthetic failure') AS failed_tools,
 (SELECT COUNT(*) FROM plugin_roca_corpus.sessions WHERE source_agent = 'codex' AND session_id = 'synthetic-correct-thread') AS control_sessions
 FROM plugin_roca_corpus.sessions`

func (m *world) theFrozenCodexIdentityHas(sessions, exactSourceSession, splitSiblings, exchanges, tools, orphanTools, failedTools, controlSessions int) error {
	got, err := m.frozenCodexIdentity()
	if err != nil {
		return err
	}
	want := codexIdentityCounts{sessions: sessions, exactSourceSession: exactSourceSession, splitSiblings: splitSiblings, exchanges: exchanges, tools: tools, orphanTools: orphanTools, failedTools: failedTools, controlSessions: controlSessions}
	if got != want {
		return fmt.Errorf("Codex identity = %+v, want %+v", got, want)
	}
	m.codexIdentityBefore = &got
	return nil
}

func (m *world) theFrozenCodexIdentityIsUnchanged() error {
	if m.codexIdentityBefore == nil {
		return fmt.Errorf("Codex identity has not been captured")
	}
	got, err := m.frozenCodexIdentity()
	if err != nil {
		return err
	}
	if got != *m.codexIdentityBefore {
		return fmt.Errorf("repeat ingest changed Codex identity: before=%+v after=%+v", *m.codexIdentityBefore, got)
	}
	return nil
}

func (m *world) frozenCodexIdentity() (codexIdentityCounts, error) {
	run, err := m.runWith("roca exec --json", []string{"exec", codexIdentitySQL, "--json"})
	if err != nil {
		return codexIdentityCounts{}, err
	}
	if run.code != 0 {
		return codexIdentityCounts{}, fmt.Errorf("Codex identity query exited %d: %s", run.code, run.stderr)
	}
	got, err := decodeCodexIdentityCounts(run.stdout)
	if err != nil {
		return codexIdentityCounts{}, err
	}
	return got, nil
}

func decodeCodexIdentityCounts(stdout string) (codexIdentityCounts, error) {
	var envelope struct {
		Rows []map[string]any `json:"rows"`
	}
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
		return codexIdentityCounts{}, fmt.Errorf("Codex identity JSON: %w\n%s", err, stdout)
	}
	if len(envelope.Rows) != 1 {
		return codexIdentityCounts{}, fmt.Errorf("Codex identity rows=%d, want 1\n%s", len(envelope.Rows), stdout)
	}
	value := func(name string) (int, error) {
		n, ok := envelope.Rows[0][name].(float64)
		if !ok {
			return 0, fmt.Errorf("Codex identity field %q = %v (%T), want number", name, envelope.Rows[0][name], envelope.Rows[0][name])
		}
		return int(n), nil
	}
	var counts codexIdentityCounts
	fields := []struct {
		name string
		dest *int
	}{
		{"sessions", &counts.sessions},
		{"exact_source_session", &counts.exactSourceSession},
		{"split_siblings", &counts.splitSiblings},
		{"exchanges", &counts.exchanges},
		{"tools", &counts.tools},
		{"orphan_tools", &counts.orphanTools},
		{"failed_tools", &counts.failedTools},
		{"control_sessions", &counts.controlSessions},
	}
	for _, field := range fields {
		got, err := value(field.name)
		if err != nil {
			return codexIdentityCounts{}, err
		}
		*field.dest = got
	}
	return counts, nil
}

func requireVectorExecuted(stdout string) error {
	var answer struct {
		VectorExecuted bool     `json:"vector_executed"`
		Notices        []string `json:"notices"`
	}
	if err := json.Unmarshal([]byte(stdout), &answer); err != nil {
		return fmt.Errorf("vector query is not JSON: %w\n%s", err, stdout)
	}
	if !answer.VectorExecuted {
		return fmt.Errorf("vector query did not execute the ready index: notices=%v\n%s", answer.Notices, stdout)
	}
	for _, notice := range answer.Notices {
		lower := strings.ToLower(notice)
		if strings.Contains(lower, "fts-only") || strings.Contains(lower, "unavailable") {
			return fmt.Errorf("vector query degraded despite the ready index: %s", notice)
		}
	}
	return nil
}

func guardLiveHub(t *testing.T) {
	t.Helper()
	operator, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	root, err := acceptanceRoot()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(root, string(os.PathSeparator)) {
		t.Fatal("project root is unusable")
	}
	_ = operator
}

func mustAcceptanceRoot(t *testing.T) string {
	t.Helper()
	root, err := acceptanceRoot()
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func verifyFrozenDigest(root string) error {
	want, err := os.ReadFile(filepath.Join(root, frozenDigestRel))
	if err != nil {
		return err
	}
	seen := map[string]string{}
	err = filepath.WalkDir(filepath.Join(root, frozenSnapshotRel), func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() || !strings.HasSuffix(path, ".db") {
			return nil
		}
		rel, err := filepath.Rel(filepath.Join(root, frozenSnapshotRel), path)
		if err != nil {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(raw)
		seen[filepath.ToSlash(rel)] = hex.EncodeToString(sum[:])
		return nil
	})
	if err != nil {
		return err
	}
	wantRows := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(want)), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return fmt.Errorf("frozen digest line %q is not '<sha256> <path>'", line)
		}
		wantRows[fields[1]] = fields[0]
	}
	if len(seen) == 0 {
		return fmt.Errorf("frozen fixture has no .db files under %s", frozenSnapshotRel)
	}
	if len(seen) != len(wantRows) {
		return fmt.Errorf("frozen .db count %d, digest lists %d", len(seen), len(wantRows))
	}
	for rel, digest := range wantRows {
		got, ok := seen[rel]
		if !ok {
			return fmt.Errorf("frozen digest names missing database %s", rel)
		}
		if got != digest {
			return fmt.Errorf("frozen database %s digest %s, want %s; re-run scripts/freeze-e2e-federation.sh only when the bytes are meant to change",
				rel, got, digest)
		}
	}
	return nil
}

func extractFrozenSnapshot(root, home, snapshot string) error {
	src := filepath.Join(root, frozenSnapshotRel, snapshot)
	if _, err := os.Stat(filepath.Join(src, ".roca", "roca.db")); err != nil {
		return fmt.Errorf("frozen snapshot %s has no core database: %w", snapshot, err)
	}
	return copyTree(home, src)
}

func lastExecutionDuration(home, command string) (int64, error) {
	dir := filepath.Join(home, ".roca", "logs")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, err
	}
	var newest string
	var newestTime time.Time
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "executions-") || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return 0, err
		}
		if info.ModTime().After(newestTime) {
			newestTime = info.ModTime()
			newest = filepath.Join(dir, entry.Name())
		}
	}
	if newest == "" {
		return 0, fmt.Errorf("no execution log under %s", dir)
	}
	raw, err := os.ReadFile(newest)
	if err != nil {
		return 0, err
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		var row struct {
			Command    string `json:"command"`
			DurationMS int64  `json:"duration_ms"`
		}
		if err := json.Unmarshal([]byte(lines[i]), &row); err != nil {
			continue
		}
		if row.Command == command {
			return row.DurationMS, nil
		}
	}
	return 0, fmt.Errorf("no %q row in %s", command, newest)
}

func longestDigitRun(s string) int {
	best, cur := 0, 0
	for _, r := range s {
		if r >= '0' && r <= '9' {
			cur++
			if cur > best {
				best = cur
			}
			continue
		}
		cur = 0
	}
	return best
}

func (m *world) aFrozenSyntheticFederationLab() error {
	return m.prepareFrozenSyntheticFederationLab("main")
}

func (m *world) aPillFreeFrozenSyntheticFederationLab() error {
	return m.prepareFrozenSyntheticFederationLab("pill-free")
}

func (m *world) aFrozenPR321FederationLab() error {
	return m.prepareFrozenSyntheticFederationLab("pr321")
}

func (m *world) aFrozenPR324FederationLab() error {
	return m.prepareFrozenSyntheticFederationLab("pr324")
}

func (m *world) prepareFrozenSyntheticFederationLab(snapshot string) error {
	root, err := acceptanceRoot()
	if err != nil {
		return err
	}
	if err := verifyFrozenDigest(root); err != nil {
		return err
	}
	if err := extractFrozenSnapshot(root, m.home, snapshot); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(m.home, ".roca", "roca.db")); err != nil {
		return fmt.Errorf("frozen snapshot %s has no core database: %w", snapshot, err)
	}
	lab := &federationLab{m: m}
	if err := lab.installPrefix(); err != nil {
		return err
	}
	if err := installFrozenVectorModel(m.home); err != nil {
		return err
	}
	if err := prepareFrozenVectorState(m.home); err != nil {
		return err
	}
	return os.MkdirAll(filepath.Join(m.home, "tmp"), 0o700)
}

func (m *world) theVectorQueryExecutedTheReadyIndex() error {
	return requireVectorExecuted(m.last.stdout)
}

func lookupExecutionDuration(m *world) (int64, error) {
	command := strings.TrimPrefix(m.last.command, "roca ")
	ms, err := lastExecutionDuration(m.home, command)
	if err == nil {
		return ms, nil
	}
	switch {
	case strings.HasPrefix(command, "exec"):
		return lastExecutionDuration(m.home, "exec")
	case strings.HasPrefix(command, "vector"):
		return lastExecutionDuration(m.home, "vector")
	case strings.HasPrefix(command, "query"):
		return lastExecutionDuration(m.home, "query")
	case strings.HasPrefix(command, "hooks run"):
		return lastExecutionDuration(m.home, "hooks run")
	default:
		return 0, err
	}
}

func (m *world) iExecSQL(statement string) error {
	_, err := m.runWith("roca exec", []string{"exec", statement})
	return err
}

func (m *world) iStorePill(slug, content string) error {
	_, err := m.runWith("roca store", []string{
		"store", "--layer", "pill", "--content", content,
		"--metadata", `{"pill_slug":"` + slug + `"}`,
		"--origin", "agent", "--agent", "codex",
	})
	return err
}

func (m *world) iExecSQLMaxChars(statement string, budget int) error {
	_, err := m.runWith("roca exec", []string{"exec", statement, "--max-chars", strconv.Itoa(budget)})
	return err
}

func (m *world) iExecSQLJSON(statement string) error {
	_, err := m.runWith("roca exec --json", []string{"exec", statement, "--json"})
	return err
}

func (m *world) iVectorQuery(phrase string) error {
	args := []string{"vector", "query", phrase, "20", "--databases", "corpus,ops", "--json"}
	if _, err := m.runWith("roca vector query", args); err != nil {
		return err
	}
	if m.last.code != 0 && strings.Contains(m.last.stdout+m.last.stderr, "rerun the command") {
		_, err := m.runWith("roca vector query", args)
		return err
	}
	return nil
}

func (m *world) iWarmThenVectorQuery(phrase string) error {
	if _, err := m.runWith("roca vector query warm-up", []string{"vector", "query", "warm harbor index", "1", "--databases", "corpus,ops", "--json"}); err != nil {
		return err
	}
	return m.iVectorQuery(phrase)
}

func (m *world) iRunClaudeAuthorshipHook() error {
	if err := os.MkdirAll(filepath.Join(m.home, "tmp"), 0o700); err != nil {
		return err
	}
	cmd := exec.Command(m.binaryPath(), "hooks", "run", "claude")
	cmd.Env = m.environment()
	cmd.Stdin = strings.NewReader(hookSessionInput)
	return m.record("roca hooks run claude", cmd)
}

const e2eHangGuard = 60 * time.Second

var errHangGuardKilled = errors.New("killed by 60-second hang guard")

func e2eFederationScenario(sc *godog.Scenario) bool {
	if sc == nil {
		return false
	}
	for _, tag := range sc.Tags {
		if tag.Name == "@e2e-federation" {
			return true
		}
	}
	return false
}

func hangGuardError(command string, elapsed time.Duration) error {
	if elapsed <= e2eHangGuard {
		return nil
	}
	return hangGuardTimeoutError(command, elapsed)
}

func hangGuardTimeoutError(command string, elapsed time.Duration) error {
	return fmt.Errorf("60-second hang guard: command %q ran %s; this is a hang guard, not a performance budget", command, elapsed)
}

func runWithHangGuard(command *exec.Cmd, limit time.Duration) error {
	if command.SysProcAttr == nil {
		command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	} else {
		command.SysProcAttr.Setpgid = true
	}
	if err := command.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	timer := time.NewTimer(limit)
	defer timer.Stop()
	select {
	case err := <-done:
		return err
	case <-timer.C:
		if command.Process != nil {
			_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
			_ = command.Process.Kill()
		}
		<-done
		return fmt.Errorf("%w: this is a hang guard, not a performance budget", errHangGuardKilled)
	}
}

func (m *world) durationWriter() io.Writer {
	if m.durationOutput != nil {
		return m.durationOutput
	}
	return os.Stdout
}

func reportMeasuredDuration(w io.Writer, command string, wall time.Duration, durationMS *int64) {
	if durationMS != nil {
		fmt.Fprintf(w, "measured duration: command=%q wall_ms=%d duration_ms=%d\n", command, wall.Milliseconds(), *durationMS)
		return
	}
	fmt.Fprintf(w, "measured duration: command=%q wall_ms=%d\n", command, wall.Milliseconds())
}

func (m *world) recordMeasuredOperation(command string, elapsed time.Duration) {
	m.everything = append(m.everything, run{command: command, elapsed: elapsed})
	reportMeasuredDuration(m.durationWriter(), command, elapsed, nil)
}

func (m *world) reportLastDuration() {
	ms, err := lookupExecutionDuration(m)
	var durationMS *int64
	if err == nil {
		durationMS = &ms
	}
	reportMeasuredDuration(m.durationWriter(), m.last.command, m.last.elapsed, durationMS)
}

func TestHangGuardFailsWhenElapsedExceeds60Seconds(t *testing.T) {
	var output bytes.Buffer
	err := hangGuardError("roca exec", 61*time.Second)
	if err == nil {
		t.Fatal("expected 60-second hang guard failure")
	}
	if !strings.Contains(err.Error(), "60-second hang guard") {
		t.Fatalf("error = %q, want 60-second hang guard", err)
	}
	if !strings.Contains(err.Error(), "not a performance budget") {
		t.Fatalf("error = %q, want hang guard labeled as not a performance budget", err)
	}
	reportMeasuredDuration(&output, "roca exec", 61*time.Second, nil)
	want := "measured duration: command=\"roca exec\" wall_ms=61000\n"
	if got := output.String(); got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestHangGuardKillsHungCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process-group hang guard uses Setpgid")
	}
	dir := t.TempDir()
	childPIDFile := filepath.Join(dir, "child.pid")
	cmd := exec.Command("sh", "-c", fmt.Sprintf("sleep 30 & echo $! >%s; wait", strconv.Quote(childPIDFile)))
	err := runWithHangGuard(cmd, 200*time.Millisecond)
	if err == nil {
		t.Fatal("expected hang guard timeout")
	}
	if !errors.Is(err, errHangGuardKilled) {
		t.Fatalf("error = %v, want %v", err, errHangGuardKilled)
	}
	if !strings.Contains(err.Error(), "60-second hang guard") {
		t.Fatalf("error = %q, want 60-second hang guard", err)
	}
	if !strings.Contains(err.Error(), "not a performance budget") {
		t.Fatalf("error = %q, want hang guard labeled as not a performance budget", err)
	}
	if cmd.Process != nil && processAlive(cmd.Process.Pid) {
		t.Fatalf("parent pid %d still running", cmd.Process.Pid)
	}
	raw, readErr := os.ReadFile(childPIDFile)
	if readErr != nil {
		t.Fatalf("child pid file: %v", readErr)
	}
	childPID, convErr := strconv.Atoi(strings.TrimSpace(string(raw)))
	if convErr != nil {
		t.Fatalf("child pid %q: %v", raw, convErr)
	}
	if processAlive(childPID) {
		t.Fatalf("spawned command pid %d still running", childPID)
	}
}

func processAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

func (m *world) iCallHealthOverStdio() error {
	return m.callTool("roca_health", map[string]any{"max_rows": 2})
}

func (m *world) theReadableMCPResponseContains(want string) error {
	text := renderedText(m.plug.last)
	if !strings.Contains(text, want) {
		return fmt.Errorf("MCP response missed %q:\n%s", want, text)
	}
	return nil
}

func (m *world) iStartThreeMCPServeProcesses() error {
	if runtime.GOOS == "windows" {
		m.last = run{command: "roca mcp serve", stdout: "windows skip"}
		return nil
	}
	if m.installed == "" {
		return fmt.Errorf("the installed binary is missing")
	}
	return nil
}

func (m *world) oneVectorResidentProcessExists() error {
	started := time.Now()
	if runtime.GOOS == "windows" {
		m.recordMeasuredOperation("shared resident check", time.Since(started))
		return nil
	}
	if m.installed == "" {
		m.recordMeasuredOperation("shared resident check", time.Since(started))
		return fmt.Errorf("the installed binary is missing")
	}
	count, ps, err := m.countSharedResidents()
	elapsed := time.Since(started)
	m.recordMeasuredOperation("shared resident check", elapsed)
	if guard := hangGuardError("shared resident check", elapsed); guard != nil {
		return guard
	}
	if err != nil {
		return err
	}
	if count != 1 {
		return fmt.Errorf("installed binary residents = %d, want 1\n%s", count, ps)
	}
	return nil
}

func (m *world) iRunTheE2ESmokeOperatorPath() error {
	cmd := exec.Command("go", "test", "-tags=acceptance", "./test/acceptance",
		"-run", "^TestPublishedReleaseUpdateInitSmoke$", "-count=1")
	root, err := acceptanceRoot()
	if err != nil {
		return err
	}
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "ROCA_BIN="+m.binary)
	started := time.Now()
	text, runErr := cmd.CombinedOutput()
	elapsed := time.Since(started)
	reportMeasuredDuration(m.durationWriter(), "make e2e-smoke", elapsed, nil)
	m.last = run{
		command: "make e2e-smoke", stdout: string(text), elapsed: elapsed,
	}
	m.everything = append(m.everything, m.last)
	if strings.Contains(string(text), "set ROCA_PUBLISHED_BIN") {
		m.last.code = 1
		m.last.stderr = string(text)
		return fmt.Errorf("published upgrade was skipped")
	}
	if runErr != nil {
		m.last.code = 1
		m.last.stderr = string(text)
		return nil
	}
	return nil
}

func copyTree(dst, src string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return os.MkdirAll(dst, 0o700)
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		info, err := d.Info()
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode())
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(out, in)
		closeErr := out.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
}

func (m *world) iRunInstalledRocaVersionThroughPATH() error {
	return m.record("roca version through PATH", exec.Command("sh", "-c",
		`PATH="$1" exec roca version`, "sh", filepath.Dir(m.installed)))
}

func (m *world) iStoreDiscoverySupersedingHistoricalID(id string) error {
	return m.callTool("roca_store", map[string]any{
		"layer": "discovery", "project": "la-roca-e2e",
		"content": "MCP historical id replacement", "supersedes": id,
	})
}

func (m *world) theMCPStoredIDIsJSSafe() error {
	id, err := jsonInteger(m.plug.last.Meta["id"])
	if err != nil || id < 1 || id >= 1<<53 || len(strconv.FormatInt(id, 10)) > 12 {
		return fmt.Errorf("MCP stored id = %#v, want a positive JS-safe integer of at most 12 digits: %v", m.plug.last.Meta["id"], err)
	}
	return nil
}
