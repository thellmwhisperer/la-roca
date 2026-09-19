//go:build acceptance

package acceptance

import (
	"database/sql"
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

	_ "github.com/thellmwhisperer/la-roca/internal/store/payloadhash"
	_ "modernc.org/sqlite"
)

const (
	federationCodexID = "019aba72-aa57-7d93-a12c-b6e65c0dca6b"
	federationPill    = "uso-de-la-roca"
)

func TestFrozenFederationInstalledBinary(t *testing.T) {
	guardLiveHub(t)
	seeded := newFederationLab(t, true)
	t.Run("pr-321-codex-history-collision", func(t *testing.T) { casePR321(t) })
	t.Run("pr-325-pill-delete", func(t *testing.T) { casePR325(t, newFederationLab(t, false)) })
	t.Run("pr-326-max-chars", func(t *testing.T) { casePR326(t) })
	t.Run("issue-315-shared-resident", func(t *testing.T) { caseIssue315(t, seeded.m.installed) })
	t.Run("issue-317-unqualified-table", func(t *testing.T) { caseIssue317(t, seeded) })
	t.Run("issue-318-handoff-limit", func(t *testing.T) { caseIssue318(t, seeded) })
	t.Run("issue-319-json-ids", func(t *testing.T) { caseIssue319(t, seeded) })
	t.Run("issue-324-codex-identity", func(t *testing.T) { caseIssue324(t, seeded.clone(t)) })
	t.Run("uso-de-la-roca", func(t *testing.T) {
		for _, c := range usoCases {
			t.Run(c.id, func(t *testing.T) { runUsage(t, seeded, c) })
		}
	})
}

type federationLab struct {
	t    *testing.T
	m    *world
	root string
}

func newFederationLab(t *testing.T, withSources bool) *federationLab {
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
	lab := &federationLab{t: t, m: &world{binary: built, home: home}, root: root}
	if err := lab.installPrefix(); err != nil {
		t.Fatal(err)
	}
	if withSources {
		if err := lab.installSources(); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(home, "tmp"), 0o700); err != nil {
		t.Fatal(err)
	}
	lab.cli(t, 0, "init", "--json", "--db-path", filepath.Join(home, ".roca", "roca.db"))
	if err := os.MkdirAll(filepath.Join(home, ".roca", "plugins", "roca-vector", "state"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := forceVectorOn(home); err != nil {
		t.Fatal(err)
	}
	if withSources {
		lab.cli(t, 0, "ingest", "--json")
		lab.seedMemories(t)
	}
	if err := forceVectorOn(home); err != nil {
		t.Fatal(err)
	}
	return lab
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
	if err := installVectorCompanion(lab.root, filepath.Dir(target), binDir); err != nil {
		return err
	}
	lab.m.installed = target
	return nil
}

func installVectorCompanion(root string, dirs ...string) error {
	src := filepath.Join(root, ".tmp", "roca-vector-native")
	raw, err := os.ReadFile(src)
	if err != nil {
		return nil
	}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, "roca-vector"), raw, 0o755); err != nil {
			return err
		}
	}
	return nil
}

func (lab *federationLab) installSources() error {
	src := filepath.Join(lab.root, "testdata", "e2e-federation", "sources", "codex")
	dst := filepath.Join(lab.m.home, ".codex")
	if err := copyTree(dst, src); err != nil {
		return err
	}
	workspace := filepath.Join(lab.m.home, "workspace", "harbor")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		return err
	}
	encoded := encodeAgentPath(workspace)
	session := filepath.Join(lab.m.home, ".claude", "projects", encoded, claudeAcceptanceSession+".jsonl")
	body := claudeExchange(1, workspace, true, "synthetic-model") +
		fmtClaude("harbor lantern on the dock", "the harbor lantern is recorded", workspace, 2)
	return writeFixture(session, body)
}

func fmtClaude(question, answer, cwd string, turn int) string {
	stamp := "2026-09-07T12:00:0" + string(rune('0'+turn)) + "Z"
	return `{"type":"user","timestamp":"` + stamp + `","cwd":` + jsonString(cwd) + `,"message":{"content":` + jsonString(question) + "}}\n" +
		`{"type":"assistant","timestamp":"` + stamp + `","message":{"content":[{"type":"text","text":` + jsonString(answer) + "}]}}\n"
}

func jsonString(v string) string {
	raw, _ := json.Marshal(v)
	return string(raw)
}

func (lab *federationLab) seedMemories(t *testing.T) {
	t.Helper()
	long := strings.Repeat("0123456789", 200) + " branch: lab done: seeded state: testing next: verify budgets"
	lab.cli(t, 0, "store", "--layer", "pill", "--content",
		"How an agent searches La Roca: binary first, vectors first, then qualified exec. Never open the database files.",
		"--metadata", `{"pill_slug":"`+federationPill+`"}`,
		"--origin", "agent", "--agent", "codex")
	lab.cli(t, 0, "store", "--layer", "handoff", "--content",
		"branch: lab scope: harbor done: seeded the frozen federation state: ready next: run the installed binary suite",
		"--origin", "agent", "--agent", "codex", "--project", "harbor")
	lab.cli(t, 0, "store", "--layer", "handoff", "--content",
		"branch: lab scope: dock done: second project receipt state: ready next: cross-project view",
		"--origin", "agent", "--agent", "codex", "--project", "dock")
	lab.cli(t, 0, "store", "--layer", "handoff", "--content", long,
		"--origin", "agent", "--agent", "codex", "--project", "budgets")
	lab.cli(t, 0, "store", "--layer", "discovery", "--content",
		"the harbor lantern marks the synthetic federation row",
		"--origin", "agent", "--agent", "codex")
}

func (lab *federationLab) clone(t *testing.T) *federationLab {
	t.Helper()
	home, err := acceptanceTempDir("roca-e2e-federation-copy-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	if err := copyTree(home, lab.m.home); err != nil {
		t.Fatal(err)
	}
	return &federationLab{
		t:    t,
		root: lab.root,
		m:    &world{binary: lab.m.binary, home: home, installed: theInstalledBinary(home)},
	}
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
	lab := newFederationLab(t, false)
	sqlPath := filepath.Join(lab.root, "testdata", "e2e-federation", "seed", "pr321-alias.sql")
	raw, err := os.ReadFile(sqlPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := execCorpusSQL(lab.m.home, string(raw)); err != nil {
		t.Fatal(err)
	}
	if err := lab.installSources(); err != nil {
		t.Fatal(err)
	}
	if err := execCorpusSQL(lab.m.home, `DELETE FROM ingest_file_state WHERE path LIKE '%history.jsonl%'`); err != nil {
		t.Fatal(err)
	}
	out := lab.cli(t, 0, "ingest", "--json")
	var report struct {
		Errors int `json:"errors"`
	}
	if err := json.Unmarshal([]byte(out.stdout), &report); err != nil {
		t.Fatalf("ingest JSON: %v\n%s", err, out.stdout)
	}
	if report.Errors != 0 {
		t.Fatalf("ingest errors=%d\n%s", report.Errors, out.stdout)
	}
	got := lab.cli(t, 0, "exec",
		"SELECT session_id, exchange_number FROM plugin_roca_corpus.exchanges WHERE session_id = '"+federationCodexID+"' ORDER BY exchange_number",
		"--json")
	if !strings.Contains(got.stdout, federationCodexID) {
		t.Fatalf("offender exchanges missing:\n%s", got.stdout)
	}
	if !strings.Contains(got.stdout, `"exchange_number": 1`) && !strings.Contains(got.stdout, `"exchange_number":1`) {
		t.Fatalf("expected reconciled prompts:\n%s", got.stdout)
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

func casePR326(t *testing.T) {
	lab := newFederationLab(t, false)
	long := strings.Repeat("0123456789", 200) + " branch: lab done: seeded state: testing next: verify budgets"
	lab.cli(t, 0, "store", "--layer", "handoff", "--content", long,
		"--origin", "agent", "--agent", "codex")
	toon := lab.cli(t, 0, "exec",
		"SELECT content FROM plugin_roca_ops.memories WHERE layer='handoff' LIMIT 1",
		"--max-chars", "900")
	digitRun := longestDigitRun(toon.stdout)
	if digitRun < 200 {
		t.Fatalf("TOON still clipped near 155 characters (digit run %d):\n%s", digitRun, toon.stdout)
	}
	js := lab.cli(t, 0, "exec",
		"SELECT content FROM plugin_roca_ops.memories WHERE layer='handoff' LIMIT 1",
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
	lab.cli(t, 0, "exec", "SELECT content FROM plugin_roca_ops.memories LIMIT 1")
}

func caseIssue318(t *testing.T, lab *federationLab) {
	limited := lab.cli(t, 0, "handoff", "latest", "--project", "harbor", "--limit", "1")
	if strings.Count(limited.stdout, "branch:") > 1 && strings.Count(limited.stdout, "handoffs[") == 0 {
		if !strings.Contains(limited.stdout, "handoffs[1]") && !strings.Contains(limited.stdout, "harbor") {
			t.Fatalf("limit 1 did not keep a harbor handoff:\n%s", limited.stdout)
		}
	}
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

func caseIssue324(t *testing.T, lab *federationLab) {
	raw, err := os.ReadFile(filepath.Join(lab.root, "internal", "ingest", "testdata", "codex-stem-split.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if err := execCorpusSQL(lab.m.home, `DELETE FROM exchanges; DELETE FROM tool_uses; DELETE FROM thinking_blocks; DELETE FROM sessions;`+string(raw)); err != nil {
		t.Fatal(err)
	}
	lab.cli(t, 0, "ingest", "--json")
	got := lab.cli(t, 0, "exec",
		"SELECT session_id FROM plugin_roca_corpus.sessions WHERE session_id LIKE '019aba72-aa57-7d93-a12c-b6e65c0dca6%' ORDER BY session_id",
		"--json")
	if !strings.Contains(got.stdout, federationCodexID) {
		t.Fatalf("exact source session missing:\n%s", got.stdout)
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
		cmd := exec.Command("roca", "version")
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
	if lab.m.last.code != 0 && strings.Contains(lab.output(), "rerun the command") {
		got = lab.cli(t, -1, args...)
	}
	if want >= 0 && lab.m.last.code != want {
		t.Fatalf("%s: exit %d, want %d\nstdout:\n%s\nstderr:\n%s",
			got.command, lab.m.last.code, want, lab.m.last.stdout, lab.m.last.stderr)
	}
	return lab.m.last
}

func forceVectorOn(home string) error {
	if err := enableVectorFeature(home); err != nil {
		return err
	}
	path := filepath.Join(home, ".roca", "config.toml")
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	body := strings.ReplaceAll(string(raw), "vector = false", "vector = true")
	if !strings.Contains(body, "vector = true") {
		if strings.Contains(body, "[features]") {
			body = strings.Replace(body, "[features]", "[features]\nvector = true", 1)
		} else {
			body += "\n[features]\nvector = true\n"
		}
	}
	if !strings.Contains(body, "vector_consent") {
		body = strings.Replace(body, "[features]", "[features]\nvector_consent = true", 1)
	}
	if !strings.Contains(body, "vector = true") {
		return fmt.Errorf("config still lacks vector = true:\n%s", body)
	}
	return os.WriteFile(path, []byte(body), 0o600)
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

func execCorpusSQL(home, statement string) error {
	db, err := sql.Open("sqlite", filepath.Join(home, ".roca", "plugins", "roca-corpus", "roca-corpus.db"))
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.Exec(statement)
	return err
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
	return m.prepareFrozenSyntheticFederationLab(true)
}

func (m *world) aPillFreeFrozenSyntheticFederationLab() error {
	return m.prepareFrozenSyntheticFederationLab(false)
}

func (m *world) prepareFrozenSyntheticFederationLab(withPill bool) error {
	root, err := acceptanceRoot()
	if err != nil {
		return err
	}
	lab := &federationLab{m: m, root: root}
	if err := lab.installPrefix(); err != nil {
		return err
	}
	if err := lab.installSources(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(m.home, "tmp"), 0o700); err != nil {
		return err
	}
	db := filepath.Join(m.home, ".roca", "roca.db")
	if _, err := m.runWith("roca init --json", []string{"init", "--json", "--db-path", db}); err != nil {
		return err
	}
	if m.last.code != 0 {
		return fmt.Errorf("init: %s%s", m.last.stdout, m.last.stderr)
	}
	if err := os.MkdirAll(filepath.Join(m.home, ".roca", "plugins", "roca-vector", "state"), 0o700); err != nil {
		return err
	}
	if err := forceVectorOn(m.home); err != nil {
		return err
	}
	if _, err := m.runWith("roca ingest --json", []string{"ingest", "--json"}); err != nil {
		return err
	}
	if m.last.code != 0 {
		return fmt.Errorf("ingest: %s%s", m.last.stdout, m.last.stderr)
	}
	if err := forceVectorOn(m.home); err != nil {
		return err
	}
	return lab.seedMemoriesErr(withPill)
}

func (lab *federationLab) seedMemoriesErr(withPill bool) error {
	long := strings.Repeat("0123456789", 200) + " branch: lab done: seeded state: testing next: verify budgets"
	cmds := [][]string{
		{"store", "--layer", "handoff", "--content", "branch: lab scope: harbor done: seeded the frozen federation state: ready next: run the installed binary suite", "--origin", "agent", "--agent", "codex", "--project", "harbor"},
		{"store", "--layer", "handoff", "--content", "branch: lab scope: dock done: second project receipt state: ready next: cross-project view", "--origin", "agent", "--agent", "codex", "--project", "dock"},
		{"store", "--layer", "handoff", "--content", long, "--origin", "agent", "--agent", "codex", "--project", "budgets"},
		{"store", "--layer", "discovery", "--content", "the harbor lantern marks the synthetic federation row", "--origin", "agent", "--agent", "codex"},
	}
	if withPill {
		cmds = append([][]string{{"store", "--layer", "pill", "--content", "How an agent searches La Roca: binary first, vectors first, then qualified exec. Never open the database files.", "--metadata", `{"pill_slug":"` + federationPill + `"}`, "--origin", "agent", "--agent", "codex"}}, cmds...)
	}
	for _, args := range cmds {
		if _, err := lab.m.runWith("roca "+strings.Join(args, " "), args); err != nil {
			return err
		}
		if lab.m.last.code != 0 {
			return fmt.Errorf("%v: %s%s", args, lab.m.last.stdout, lab.m.last.stderr)
		}
	}
	return nil
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
