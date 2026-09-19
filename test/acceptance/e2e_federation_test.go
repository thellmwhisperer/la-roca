//go:build acceptance

package acceptance

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

const (
	federationCodexID     = "019aba72-aa57-7d93-a12c-b6e65c0dca6b"
	federationPill        = "uso-de-la-roca"
	federationDiscoveryID = "1152921504606846980"
	federationHarborID    = "1152921504606846977"
	frozenArchiveRel      = "testdata/e2e-federation/frozen.tar.gz"
	frozenDigestRel       = "testdata/e2e-federation/frozen.sha256"
	vectorModelSHA        = "a5db3381f2e514d3490a3a31fe70eb1a65e95016c85c6c2c23223b810806594f"
	hookSessionInput      = `{"hook_event_name":"SessionStart","tool_name":"","tool_input":{}}`
)

func TestFrozenFederationBytesArePinned(t *testing.T) {
	root, err := acceptanceRoot()
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyFrozenDigest(root); err != nil {
		t.Fatal(err)
	}
}

func TestFrozenFederationInstalledBinary(t *testing.T) {
	guardLiveHub(t)
	if err := verifyFrozenDigest(mustAcceptanceRoot(t)); err != nil {
		t.Fatal(err)
	}
	seeded := newFederationLab(t, "main")
	t.Run("pr-321-codex-history-collision", func(t *testing.T) { casePR321(t) })
	t.Run("pr-325-pill-delete", func(t *testing.T) { casePR325(t, newFederationLab(t, "pill-free")) })
	t.Run("pr-326-max-chars", func(t *testing.T) { casePR326(t, seeded) })
	t.Run("issue-315-shared-resident", func(t *testing.T) { caseIssue315(t, seeded.m.installed) })
	t.Run("issue-317-unqualified-table", func(t *testing.T) { caseIssue317(t, seeded) })
	t.Run("issue-318-handoff-limit", func(t *testing.T) { caseIssue318(t, seeded) })
	t.Run("issue-319-json-ids", func(t *testing.T) { caseIssue319(t, seeded) })
	t.Run("issue-324-codex-identity", func(t *testing.T) { caseIssue324(t) })
	t.Run("uso-de-la-roca", func(t *testing.T) {
		for _, c := range usoCases {
			t.Run(c.id, func(t *testing.T) { runUsage(t, seeded, c) })
		}
	})
	t.Run("real-usage-hooks-0ms", func(t *testing.T) { caseHooksNeverBlock(t, seeded) })
	t.Run("real-usage-exec-exact-ids", func(t *testing.T) { caseExecExactIDs(t, seeded) })
	t.Run("real-usage-vector-query", func(t *testing.T) { caseVectorQueryBudget(t, seeded) })
	t.Run("real-usage-query-no-silent-degrade", func(t *testing.T) { caseQueryNoSilentDegrade(t, seeded) })
	t.Run("real-usage-handoff-one-per-project", func(t *testing.T) { caseHandoffOnePerProject(t, seeded) })
	t.Run("real-usage-mcp-handoff-refused", func(t *testing.T) { caseMCPHandoffRefused(t, seeded) })
	t.Run("real-usage-e2e-smoke", TestPublishedReleaseUpdateInitSmoke)
	t.Run("real-usage-mcp-health", func(t *testing.T) { caseMCPHealth(t, seeded) })
}

type federationLab struct {
	t *testing.T
	m *world
}

func newFederationLab(t *testing.T, snapshot string) *federationLab {
	t.Helper()
	built, err := rocaBinary()
	if err != nil {
		t.Fatal(err)
	}
	home, err := acceptanceTempDir("roca-e2e-federation-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	operator, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Clean(home) == filepath.Clean(operator) {
		t.Fatal("the disposable home resolved to the operator home")
	}
	root, err := acceptanceRoot()
	if err != nil {
		t.Fatal(err)
	}
	lab := &federationLab{t: t, m: &world{binary: built, home: home}}
	if err := extractFrozenSnapshot(root, home, snapshot); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, ".roca", "roca.db")); err != nil {
		t.Fatalf("frozen snapshot %s has no core database: %v", snapshot, err)
	}
	if err := installFrozenVectorModel(home); err != nil {
		t.Fatal(err)
	}
	if err := lab.installPrefix(); err != nil {
		t.Fatal(err)
	}
	if err := prepareFrozenVectorState(home); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, "tmp"), 0o700); err != nil {
		t.Fatal(err)
	}
	return lab
}

func installFrozenVectorModel(home string) error {
	source := strings.TrimSpace(os.Getenv("ROCA_E2E_VECTOR_MODEL"))
	if source == "" {
		return fmt.Errorf("ROCA_E2E_VECTOR_MODEL is required for the ready-index acceptance path")
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
	command.Env = lab.m.environment()
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("install bundled plugins in frozen home: %w\n%s", err, output)
	}
	vector, err := os.ReadFile(filepath.Join(filepath.Dir(target), "roca-vector"))
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(binDir, "roca-vector"), vector, 0o755)
}

func (lab *federationLab) cli(t *testing.T, want int, args ...string) run {
	t.Helper()
	label := "roca " + strings.Join(args, " ")
	if _, err := lab.m.runWith(label, args); err != nil {
		t.Fatalf("%s: %v", label, err)
	}
	if want >= 0 && lab.m.last.code != want {
		t.Fatalf("%s: exit %d, want %d\nstdout:\n%s\nstderr:\n%s",
			label, lab.m.last.code, want, lab.m.last.stdout, lab.m.last.stderr)
	}
	return lab.m.last
}

func (lab *federationLab) output() string {
	return lab.m.last.stdout + lab.m.last.stderr
}

func casePR321(t *testing.T) {
	lab := newFederationLab(t, "pr321")
	out := lab.cli(t, 0, "ingest", "--json")
	if !strings.Contains(out.stdout, `"errors"`) {
		t.Fatalf("ingest JSON missed errors:\n%s", out.stdout)
	}
	if !strings.Contains(out.stdout, `"errors": 0`) && !strings.Contains(out.stdout, `"errors":0`) {
		t.Fatalf("ingest errors were not 0:\n%s", out.stdout)
	}
	got := lab.cli(t, 0, "exec",
		"SELECT session_id, exchange_number FROM plugin_roca_corpus.exchanges WHERE session_id = '"+federationCodexID+"' ORDER BY exchange_number",
		"--json")
	if !strings.Contains(got.stdout, federationCodexID) {
		t.Fatalf("offender exchanges missing:\n%s", got.stdout)
	}
}

func casePR325(t *testing.T, lab *federationLab) {
	lab.cli(t, 0, "store", "--layer", "pill", "--content", "Temporary pill for issue 320 acceptance",
		"--metadata", `{"pill_slug":"tmp-x"}`, "--origin", "agent", "--agent", "codex")
	listed := lab.cli(t, 0, "pill")
	if !strings.Contains(listed.stdout, "tmp-x") {
		t.Fatalf("pill list missed tmp-x:\n%s", listed.stdout)
	}
	deleted := lab.cli(t, 0, "pill", "delete", "tmp-x")
	if !strings.Contains(deleted.stdout, "deleted: 1") {
		t.Fatalf("delete output:\n%s", deleted.stdout)
	}
	after := lab.cli(t, 0, "pill")
	if strings.Contains(after.stdout, "tmp-x") {
		t.Fatalf("pill still listed:\n%s", after.stdout)
	}
	if !strings.Contains(after.stdout, "no active pills") {
		t.Fatalf("pill list after delete was not empty:\n%s", after.stdout)
	}
	count := lab.cli(t, 0, "exec",
		"SELECT count(*) FROM plugin_roca_ops.memories WHERE json_extract(metadata,'$.pill_slug')='tmp-x'")
	if !strings.Contains(count.stdout, "0") {
		t.Fatalf("count after delete:\n%s", count.stdout)
	}
}

func casePR326(t *testing.T, lab *federationLab) {
	toon := lab.cli(t, 0, "exec",
		"SELECT content FROM plugin_roca_ops.memories WHERE project='budgets'",
		"--max-chars", "900")
	digitRun := longestDigitRun(toon.stdout)
	if digitRun < 200 {
		t.Fatalf("TOON still clipped near 155 characters (digit run %d):\n%s", digitRun, toon.stdout)
	}
	js := lab.cli(t, 0, "exec",
		"SELECT content FROM plugin_roca_ops.memories WHERE project='budgets'",
		"--max-chars", "900", "--json")
	var envelope struct {
		Rows []struct {
			Content string `json:"content"`
		} `json:"rows"`
	}
	if err := json.Unmarshal([]byte(js.stdout), &envelope); err != nil {
		t.Fatalf("json: %v\n%s", err, js.stdout)
	}
	if len(envelope.Rows) != 1 {
		t.Fatalf("rows=%d\n%s", len(envelope.Rows), js.stdout)
	}
	n := utf8.RuneCountInString(envelope.Rows[0].Content)
	if n < 800 || n > 900 {
		t.Fatalf("JSON content runes=%d, want 800-900\n%s", n, js.stdout)
	}
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

func caseIssue315(t *testing.T, installed string) {
	if runtime.GOOS == "windows" {
		t.Skip("shared residency is proven on unix sockets")
	}
	fake := buildFakeVectorResident(t)
	evidence := threeServeResidentPS(t, installed, fake, time.Second)
	if evidence.count != 1 {
		t.Fatalf("installed binary residents = %d, want 1\n%s", evidence.count, evidence.ps)
	}
}

func caseIssue317(t *testing.T, lab *federationLab) {
	lab.cli(t, 1, "exec", `SELECT content FROM memories WHERE content LIKE '%PROCESO CARWOW%'`)
	got := lab.output()
	if !strings.Contains(got, `unqualified table "memories"`) {
		t.Fatalf("missing unqualified refusal:\n%s", got)
	}
	if !strings.Contains(got, "plugin_roca_ops.memories") || !strings.Contains(got, "plugin_roca_corpus.memories") {
		t.Fatalf("missing qualified candidates:\n%s", got)
	}
}

func caseIssue318(t *testing.T, lab *federationLab) {
	limited := lab.cli(t, 0, "handoff", "latest", "--project", "harbor", "--limit", "1")
	if !strings.Contains(limited.stdout, "harbor") {
		t.Fatalf("limit 1 missed harbor:\n%s", limited.stdout)
	}
	all := lab.cli(t, 0, "handoff", "latest", "--all-projects")
	if !strings.Contains(all.stdout, "harbor") || !strings.Contains(all.stdout, "dock") {
		t.Fatalf("all-projects missed a project:\n%s", all.stdout)
	}
}

func caseIssue319(t *testing.T, lab *federationLab) {
	got := lab.cli(t, 0, "exec", "SELECT id FROM plugin_roca_ops.memories LIMIT 1", "--json")
	if !regexp.MustCompile(`"id"\s*:\s*"`).MatchString(got.stdout) {
		t.Fatalf("ops id was not a JSON string:\n%s", got.stdout)
	}
}

func caseIssue324(t *testing.T) {
	lab := newFederationLab(t, "pr324")
	lab.cli(t, 0, "ingest", "--json")
	before := codexIdentitySnapshot(t, lab)
	want := codexIdentityCounts{sessions: 2, exactSourceSession: 1, splitSiblings: 0, exchanges: 8, tools: 48, orphanTools: 48, failedTools: 1, controlSessions: 1}
	if before != want {
		t.Fatalf("Codex identity = %+v, want %+v", before, want)
	}
	lab.cli(t, 0, "ingest", "--json")
	after := codexIdentitySnapshot(t, lab)
	if after != before {
		t.Fatalf("repeat ingest changed Codex identity: before=%+v after=%+v", before, after)
	}
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

func codexIdentitySnapshot(t *testing.T, lab *federationLab) codexIdentityCounts {
	t.Helper()
	got := lab.cli(t, 0, "exec", codexIdentitySQL, "--json")
	counts, err := decodeCodexIdentityCounts(got.stdout)
	if err != nil {
		t.Fatal(err)
	}
	return counts
}

func (m *world) theFrozenCodexIdentityHas(sessions, exactSourceSession, splitSiblings, exchanges, tools, orphanTools, failedTools, controlSessions int) error {
	run, err := m.runWith("roca exec --json", []string{"exec", codexIdentitySQL, "--json"})
	if err != nil {
		return err
	}
	if run.code != 0 {
		return fmt.Errorf("Codex identity query exited %d: %s", run.code, run.stderr)
	}
	got, err := decodeCodexIdentityCounts(run.stdout)
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
	run, err := m.runWith("roca exec --json", []string{"exec", codexIdentitySQL, "--json"})
	if err != nil {
		return err
	}
	if run.code != 0 {
		return fmt.Errorf("Codex identity query exited %d: %s", run.code, run.stderr)
	}
	got, err := decodeCodexIdentityCounts(run.stdout)
	if err != nil {
		return err
	}
	if got != *m.codexIdentityBefore {
		return fmt.Errorf("repeat ingest changed Codex identity: before=%+v after=%+v", *m.codexIdentityBefore, got)
	}
	return nil
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

func caseHooksNeverBlock(t *testing.T, lab *federationLab) {
	cmd := exec.Command(lab.m.installed, "hooks", "run", "claude")
	cmd.Env = lab.m.environment()
	cmd.Stdin = strings.NewReader(hookSessionInput)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("hooks run claude: %v\n%s", err, out)
	}
	ms, err := lastExecutionDuration(lab.m.home, "hooks run")
	if err != nil {
		t.Fatal(err)
	}
	if ms != 0 {
		t.Fatalf("hooks run duration_ms=%d, want 0", ms)
	}
}

func caseExecExactIDs(t *testing.T, lab *federationLab) {
	start := time.Now()
	got := lab.cli(t, 0, "exec",
		"SELECT id FROM plugin_roca_ops.memories WHERE id = '"+federationDiscoveryID+"'",
		"--json")
	if time.Since(start) >= 5*time.Second {
		t.Fatalf("exec took %s, want under 5s", time.Since(start))
	}
	if !strings.Contains(got.stdout, `"`+federationDiscoveryID+`"`) {
		t.Fatalf("exact id missing:\n%s", got.stdout)
	}
	ms, err := lastExecutionDuration(lab.m.home, "exec")
	if err != nil {
		t.Fatal(err)
	}
	if ms >= 5000 {
		t.Fatalf("exec duration_ms=%d, want under 5000", ms)
	}
}

func caseVectorQueryBudget(t *testing.T, lab *federationLab) {
	// The resident pays the native model load once; the operator path being
	// budgeted is the ready, already-warm index used by subsequent queries.
	lab.cli(t, 0, "vector", "query", "warm harbor index", "1", "--databases", "corpus,ops", "--json")
	start := time.Now()
	got := lab.cli(t, 0, "vector", "query", "harbor lantern", "20", "--databases", "corpus,ops", "--json")
	if time.Since(start) >= 2*time.Second {
		t.Fatalf("vector query took %s, want under 2s", time.Since(start))
	}
	var answer struct {
		VectorExecuted bool     `json:"vector_executed"`
		Notices        []string `json:"notices"`
	}
	if err := json.Unmarshal([]byte(got.stdout), &answer); err != nil {
		t.Fatalf("vector query is not JSON: %v\n%s", err, got.stdout)
	}
	if !answer.VectorExecuted {
		t.Fatalf("vector query did not execute the ready index: notices=%v\n%s", answer.Notices, got.stdout)
	}
	for _, notice := range answer.Notices {
		if strings.Contains(strings.ToLower(notice), "fts-only") || strings.Contains(strings.ToLower(notice), "unavailable") {
			t.Fatalf("vector query degraded despite the ready index: %s", notice)
		}
	}
	ms, err := lastExecutionDuration(lab.m.home, "vector")
	if err != nil {
		t.Fatal(err)
	}
	if ms >= 2000 {
		t.Fatalf("vector query duration_ms=%d, want under 2000", ms)
	}
}

func caseQueryNoSilentDegrade(t *testing.T, lab *federationLab) {
	start := time.Now()
	got := lab.cli(t, 0, "query", "harbor lantern", "--json")
	if time.Since(start) >= 3*time.Second {
		t.Fatalf("query took %s, want under 3s", time.Since(start))
	}
	if !strings.Contains(got.stdout, "engines") {
		t.Fatalf("query named no engines:\n%s", got.stdout)
	}
	if strings.Contains(got.stdout, "search hybrid") && !strings.Contains(got.stdout, "vector") {
		t.Fatalf("query claimed hybrid without a vector engine:\n%s", got.stdout)
	}
	ms, err := lastExecutionDuration(lab.m.home, "query")
	if err != nil {
		t.Fatal(err)
	}
	if ms >= 3000 {
		t.Fatalf("query duration_ms=%d, want under 3000", ms)
	}
}

func caseHandoffOnePerProject(t *testing.T, lab *federationLab) {
	got := lab.cli(t, 0, "handoff", "latest", "--project", "harbor")
	if !strings.Contains(got.stdout, "handoffs[1]") {
		t.Fatalf("want one current harbor handoff:\n%s", got.stdout)
	}
	if strings.Contains(got.stdout, "handoffs[2]") {
		t.Fatalf("more than one harbor handoff:\n%s", got.stdout)
	}
	if !strings.Contains(got.stdout, federationHarborID) {
		t.Fatalf("harbor id missing:\n%s", got.stdout)
	}
}

func caseMCPHandoffRefused(t *testing.T, lab *federationLab) {
	if err := lab.m.openThePlugAs("glm-5.2 (codex/slopslint-detector-a1)"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(lab.m.closeThePlug)
	if err := lab.m.iStoreASessionHandoffOverMCP(); err != nil {
		t.Fatal(err)
	}
	if err := lab.m.theResponseIsAToolError(); err != nil {
		t.Fatal(err)
	}
	if err := lab.m.theRefusalNamesTheHandoffWriter(); err != nil {
		t.Fatal(err)
	}
}

func caseMCPHealth(t *testing.T, lab *federationLab) {
	if err := lab.m.callTool("roca_health", map[string]any{"max_rows": 2}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(lab.m.closeThePlug)
	if err := lab.m.theResponseIsNotAnError(); err != nil {
		t.Fatal(err)
	}
	text := renderedText(lab.m.plug.last)
	if !strings.Contains(text, "health: pass") {
		t.Fatalf("roca_health:\n%s", text)
	}
}

type usageCase struct {
	id       string
	args     []string
	contains []string
	code     int
}

var usoCases = []usageCase{
	{id: "232991", args: []string{"vector", "query", "harbor lantern", "20", "--databases", "corpus,ops"}},
	{id: "233400", args: []string{"exec", "SELECT content FROM plugin_roca_ops.memories WHERE content LIKE '%harbor lantern%'"}},
	{id: "233508", args: []string{"query", "harbor lantern", "--json"}, contains: []string{"engines"}},
	{id: "10387", args: []string{"exec", "SELECT COUNT(*) AS memories FROM plugin_roca_ops.memories"}},
	{id: "238277", args: []string{"vector", "query", "harbor lantern", "20", "--databases", "corpus,ops"}},
	{id: "244386", args: []string{"exec", "SELECT layer, COUNT(*) AS n FROM plugin_roca_ops.memories GROUP BY layer"}},
	{id: "259288", args: []string{"handoff", "latest", "--project", "harbor"}, contains: []string{"harbor"}},
	{id: "93762", args: []string{"query", "harbor lantern", "--json"}},
	{id: "19944", args: []string{"handoff", "latest", "--project", "harbor"}},
	{id: "7734", args: []string{"doctor"}},
	{id: "5740", args: []string{"vector", "query", "harbor lantern", "20", "--databases", "corpus,ops"}},
	{id: "5950", args: []string{"exec", "SELECT content FROM plugin_roca_ops.memories LIMIT 1"}},
	{id: "4269", args: []string{"version"}, contains: []string{"roca"}},
	{id: "4657", args: []string{"vector", "query", "harbor lantern", "20", "--databases", "corpus,ops"}},
	{id: "44508", args: []string{"query", "harbor lantern"}},
	{id: "125372", args: []string{"vector", "query", "I inspected the harbor lantern", "20", "--databases", "corpus,ops"}},
	{id: "126485", args: []string{"exec", "SELECT content FROM plugin_roca_ops.memories WHERE layer='discovery'"}},
	{id: "127663", args: []string{"doctor"}},
	{id: "296656", args: []string{"doctor"}},
	{id: "297007", args: []string{"doctor"}},
	{id: "1658381", args: []string{"doctor"}},
	{id: "1708690", args: []string{"pill", "show", federationPill}, contains: []string{"vectors first"}},
	{id: "1733215", args: []string{"handoff", "latest", "--project", "harbor"}},
}

func runUsage(t *testing.T, lab *federationLab, c usageCase) {
	t.Helper()
	if c.id == "4269" {
		cmd := exec.Command("sh", "-c", "roca version")
		cmd.Env = []string{
			"HOME=" + lab.m.home,
			"PATH=" + filepath.Dir(lab.m.installed),
			"TMPDIR=" + filepath.Join(lab.m.home, "tmp"),
			"ROCA_MODELS_ORDER=none",
		}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("PATH roca version: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), "roca") {
			t.Fatalf("PATH version:\n%s", out)
		}
		return
	}
	got := lab.cliAllow(t, c.code, c.args...)
	for _, want := range c.contains {
		if !strings.Contains(got.stdout+got.stderr, want) {
			t.Fatalf("missing %q in\n%s", want, got.stdout+got.stderr)
		}
	}
}

func (lab *federationLab) cliAllow(t *testing.T, want int, args ...string) run {
	t.Helper()
	got := lab.cli(t, -1, args...)
	for i := 0; i < 2 && lab.m.last.code != 0 && strings.Contains(lab.output(), "rerun the command"); i++ {
		got = lab.cli(t, -1, args...)
	}
	if want >= 0 && lab.m.last.code != want {
		t.Fatalf("%s: exit %d, want %d\nstdout:\n%s\nstderr:\n%s",
			got.command, lab.m.last.code, want, lab.m.last.stdout, lab.m.last.stderr)
	}
	return lab.m.last
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
	raw, err := os.ReadFile(filepath.Join(root, frozenArchiveRel))
	if err != nil {
		return err
	}
	want, err := os.ReadFile(filepath.Join(root, frozenDigestRel))
	if err != nil {
		return err
	}
	sum := sha256.Sum256(raw)
	got := hex.EncodeToString(sum[:])
	if got != strings.TrimSpace(string(want)) {
		return fmt.Errorf("frozen fixture digest %s, want %s; re-run scripts/freeze-e2e-federation.sh only when the bytes are meant to change",
			got, strings.TrimSpace(string(want)))
	}
	return nil
}

func extractFrozenSnapshot(root, home, snapshot string) error {
	file, err := os.Open(filepath.Join(root, frozenArchiveRel))
	if err != nil {
		return err
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer gz.Close()
	reader := tar.NewReader(gz)
	prefix := snapshot + "/"
	found := false
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		name := strings.TrimPrefix(header.Name, "./")
		if name == snapshot || name == snapshot+"/" {
			found = true
			continue
		}
		rel, ok := strings.CutPrefix(name, prefix)
		if !ok || rel == "" {
			continue
		}
		found = true
		if err := extractTarEntry(home, rel, header, reader); err != nil {
			return err
		}
	}
	if !found {
		return fmt.Errorf("frozen snapshot %s is missing from %s", snapshot, frozenArchiveRel)
	}
	return nil
}

func extractTarEntry(home, rel string, header *tar.Header, reader *tar.Reader) error {
	rel = filepath.Clean(rel)
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return fmt.Errorf("refusing archive path %q", rel)
	}
	target := filepath.Join(home, rel)
	if !strings.HasPrefix(target, filepath.Clean(home)+string(os.PathSeparator)) && target != filepath.Clean(home) {
		return fmt.Errorf("archive path escaped home: %s", rel)
	}
	switch header.Typeflag {
	case tar.TypeDir:
		return os.MkdirAll(target, 0o700)
	case tar.TypeReg, tar.TypeRegA:
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, header.FileInfo().Mode())
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(out, reader)
		closeErr := out.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	default:
		return nil
	}
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
	if err := prepareFrozenVectorState(m.home); err != nil {
		return err
	}
	return os.MkdirAll(filepath.Join(m.home, "tmp"), 0o700)
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
	args := []string{"vector", "query", phrase, "20", "--databases", "corpus,ops"}
	if _, err := m.runWith("roca vector query", args); err != nil {
		return err
	}
	if m.last.code != 0 && strings.Contains(m.last.stdout+m.last.stderr, "rerun the command") {
		_, err := m.runWith("roca vector query", args)
		return err
	}
	return nil
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

func (m *world) theExecutionLogDurationIs(want int) error {
	return m.theExecutionLogDurationUnder(want + 1)
}

func (m *world) theExecutionLogDurationUnder(limit int) error {
	command := strings.TrimPrefix(m.last.command, "roca ")
	ms, err := lastExecutionDuration(m.home, command)
	if err != nil {
		if strings.HasPrefix(command, "exec") {
			ms, err = lastExecutionDuration(m.home, "exec")
		} else if strings.HasPrefix(command, "vector") {
			ms, err = lastExecutionDuration(m.home, "vector")
		} else if strings.HasPrefix(command, "query") {
			ms, err = lastExecutionDuration(m.home, "query")
		} else if strings.HasPrefix(command, "hooks run") {
			ms, err = lastExecutionDuration(m.home, "hooks run")
		}
	}
	if err != nil {
		return err
	}
	if ms >= int64(limit) {
		return fmt.Errorf("duration_ms=%d, want under %d", ms, limit)
	}
	if limit == 1 && ms != 0 {
		return fmt.Errorf("duration_ms=%d, want 0", ms)
	}
	return nil
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
	if runtime.GOOS == "windows" {
		return nil
	}
	root, err := acceptanceRoot()
	if err != nil {
		return err
	}
	cmd := exec.Command("go", "test", "-tags=acceptance", "./test/acceptance",
		"-run", "^TestFrozenFederationInstalledBinary$/issue-315-shared-resident", "-count=1")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "ROCA_BIN="+m.binary)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("shared resident: %v\n%s", err, out)
	}
	return nil
}

func (m *world) iRunTheE2ESmokeOperatorPath() error {
	cmd := exec.Command("go", "test", "-tags=acceptance", "./test/acceptance",
		"-run", "^TestRealBinaryDisposableHomeSmoke$", "-count=1")
	root, err := acceptanceRoot()
	if err != nil {
		return err
	}
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "ROCA_BIN="+m.binary)
	out, err := cmd.CombinedOutput()
	m.last = run{command: "make e2e-smoke", stdout: string(out)}
	if err != nil {
		m.last.code = 1
		m.last.stderr = string(out)
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
