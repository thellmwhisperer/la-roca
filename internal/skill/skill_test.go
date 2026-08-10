package skill_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/skill"
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
	if string(body) != skill.Content() {
		t.Fatal("written skill does not match the embedded canonical text")
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

func TestInstallReplacesStaleContent(t *testing.T) {
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
	if string(body) != skill.Content() {
		t.Fatal("stale skill was not replaced")
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
