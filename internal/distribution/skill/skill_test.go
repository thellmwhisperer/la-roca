package skill_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/artifact"
	"github.com/thellmwhisperer/la-roca/internal/distribution/agentcfg"
	"github.com/thellmwhisperer/la-roca/internal/distribution/skill"
)

func TestContentIsANamedRocaSkill(t *testing.T) {
	body := skill.Content()
	if !strings.HasPrefix(body, "---\n") {
		t.Fatal("skill must open with YAML frontmatter")
	}
	if !strings.Contains(body, "name: roca") {
		t.Fatal("skill name must be roca")
	}
	for _, needle := range []string{
		"roca query", "roca exec", "roca store",
		"roca_query", "who is", "have we done",
	} {
		if !strings.Contains(body, needle) {
			t.Errorf("skill body missing %q", needle)
		}
	}
}

func TestContentTeachesCLIAuthorshipFlags(t *testing.T) {
	body := skill.Content()
	for _, want := range []string{"--agent", "--model", "automatic", "MCP"} {
		if !strings.Contains(body, want) {
			t.Errorf("skill does not teach %q in the authorship contract", want)
		}
	}
}

func TestContentCarriesOperatingCraft(t *testing.T) {
	body := skill.Content()
	for _, needle := range []string{
		`latest handoff for <project>`,
		"current handoff protocol",
		"always store a handoff",
		"Ask bare first",
		"search the whole corpus",
		"sessions` or `exchanges",
		"ORDER BY timestamp ASC",
		"Rows are the truth",
		"Use the layer filter deliberately",
		"coordination layers",
	} {
		if !strings.Contains(body, needle) {
			t.Errorf("skill operating craft missing %q", needle)
		}
	}
}

func TestRuntimesMatchTheFiveAgentcfgKnows(t *testing.T) {
	want := []string{"claude", "codex", "hermes", "opencode", "pi"}
	got := skill.Runtimes()
	if len(got) != len(want) {
		t.Fatalf("runtimes = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("runtimes = %v, want %v", got, want)
		}
	}
}

func TestPathResolvesEachRuntimeUnderATempHome(t *testing.T) {
	home := t.TempDir()
	cases := map[string]string{
		"claude":   filepath.Join(home, ".claude", "skills", "roca", "SKILL.md"),
		"codex":    filepath.Join(home, ".codex", "skills", "roca", "SKILL.md"),
		"opencode": filepath.Join(home, ".config", "opencode", "skills", "roca", "SKILL.md"),
		"hermes":   filepath.Join(home, ".hermes", "skills", "roca", "SKILL.md"),
		"pi":       filepath.Join(home, ".pi", "agent", "skills", "roca", "SKILL.md"),
	}
	for runtime, want := range cases {
		got, err := skill.Path(runtime, home, nil)
		if err != nil {
			t.Fatalf("%s: %v", runtime, err)
		}
		if got != want {
			t.Errorf("%s path = %s, want %s", runtime, got, want)
		}
	}
}

func TestPathHonoursRuntimeEnvOverrides(t *testing.T) {
	home := t.TempDir()
	elsewhere := filepath.Join(home, "elsewhere")
	env := func(key string) string {
		switch key {
		case "CLAUDE_CONFIG_DIR", "CODEX_HOME", "HERMES_HOME", "PI_CODING_AGENT_DIR":
			return elsewhere
		case "OPENCODE_CONFIG":
			return filepath.Join(elsewhere, "opencode.json")
		default:
			return ""
		}
	}
	for _, runtime := range skill.Runtimes() {
		got, err := skill.Path(runtime, home, env)
		if err != nil {
			t.Fatalf("%s: %v", runtime, err)
		}
		if !strings.HasPrefix(got, elsewhere) {
			t.Errorf("%s path = %s, want under %s", runtime, got, elsewhere)
		}
		if !strings.HasSuffix(got, filepath.Join("skills", "roca", "SKILL.md")) {
			t.Errorf("%s path = %s, want …/skills/roca/SKILL.md", runtime, got)
		}
	}
}

func TestInstallWritesTheSkillAndIsIdempotent(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".claude", "skills", "roca", "SKILL.md")

	first, err := skill.Install("claude", path)
	if err != nil {
		t.Fatalf("first install: %v", err)
	}
	if !first.Changed || first.Path != path {
		t.Fatalf("first = %+v", first)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(body), "---\n# ROCA SYSTEM BEGIN\n") {
		t.Fatalf("installed skill no longer opens with YAML frontmatter: %q", string(body[:min(len(body), 40)]))
	}
	zones, err := artifact.Parse(string(body))
	if err != nil || zones.System != skill.Content() || zones.User != "" {
		t.Fatalf("written skill zones = %+v, err %v", zones, err)
	}

	second, err := skill.Install("claude", path)
	if err != nil {
		t.Fatalf("second install: %v", err)
	}
	if second.Changed {
		t.Fatal("reinstall rewrote an identical skill")
	}

	out, err := skill.Uninstall("claude", path)
	if err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if !out.Changed {
		t.Fatal("uninstall of an installed skill changed nothing")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("skill file survived uninstall")
	}
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatal("skill directory survived uninstall")
	}

	reuninstall, err := skill.Uninstall("claude", path)
	if err != nil {
		t.Fatalf("re-uninstall: %v", err)
	}
	if reuninstall.Changed {
		t.Fatal("re-uninstall of an already removed skill claims change")
	}
}

func TestInstallRefusesAnUnknownRuntime(t *testing.T) {
	_, err := skill.Install("windsurf", filepath.Join(t.TempDir(), "SKILL.md"))
	if err == nil {
		t.Fatal("unknown runtime was accepted")
	}
	if !strings.Contains(err.Error(), "windsurf") {
		t.Errorf("error does not name the runtime: %v", err)
	}
}

func TestInstallAdoptsUnrecognizedLegacyContentIntoTheUserZone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "skills", "roca", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("stale\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := skill.Install("codex", path)
	if err != nil {
		t.Fatal(err)
	}
	if !out.Changed {
		t.Fatal("stale skill was left in place")
	}
	body, _ := os.ReadFile(path)
	zones, err := artifact.Parse(string(body))
	if err != nil {
		t.Fatal(err)
	}
	if zones.System != skill.Content() || zones.User != "stale\n" {
		t.Fatalf("legacy adoption = %+v", zones)
	}
}

func TestUninstallLeavesForeignContentAlone(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "roca")
	path := filepath.Join(dir, "SKILL.md")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("not roca content"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := skill.Uninstall("claude", path); err != nil {
		t.Fatalf("uninstall: %v", err)
	} else if out.Changed {
		t.Fatal("uninstall removed a skill it did not write")
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatal("foreign skill dir was removed")
	}
}

// D-7's second half: what La Roca did not create is never deleted. The withdrawal
// took the whole skill directory with os.RemoveAll, so anything the operator had
// put beside the canonical SKILL.md went with it. The parent-directory cleanup
// right below already had the correct shape: remove, which only succeeds when
// nothing else is left.
func TestWithdrawingASkillLeavesTheOperatorsOwnFilesAlone(t *testing.T) {
	home := t.TempDir()
	path, err := skill.Path(agentcfg.RuntimeClaude, home, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := skill.Install(agentcfg.RuntimeClaude, path); err != nil {
		t.Fatalf("install: %v", err)
	}
	mine := filepath.Join(filepath.Dir(path), "notes-of-mine.md")
	if err := os.WriteFile(mine, []byte("my own notes"), 0o600); err != nil {
		t.Fatal(err)
	}

	out, err := skill.Uninstall(agentcfg.RuntimeClaude, path)
	if err != nil {
		t.Fatalf("uninstall: %v", err)
	}

	if _, err := os.Stat(mine); err != nil {
		t.Errorf("a file La Roca did not create was deleted: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("the canonical SKILL.md survived: %v", err)
	}
	for _, removed := range out.Removed {
		if removed == filepath.Dir(path) {
			t.Errorf("the report claims it removed %s, which is still there", removed)
		}
	}
}
