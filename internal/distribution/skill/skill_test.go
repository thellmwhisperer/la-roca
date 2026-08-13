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

func shippedChecksum() string { return artifact.Checksum(skill.Content()) }

func TestContentIsANamedRocaSkill(t *testing.T) {
	body := skill.Content()
	if !strings.HasPrefix(body, "---\n") {
		t.Fatal("skill must open with YAML frontmatter")
	}
	if !strings.Contains(body, "name: roca") {
		t.Fatal("skill name must be roca")
	}
	// The migration recognizes an older release's SKILL.md by this opening, so a
	// shipped skill that stopped carrying it would be adopted as operator bytes.
	if !strings.HasPrefix(body, skill.LegacySignature()) {
		t.Fatalf("the shipped skill no longer opens with %q", skill.LegacySignature())
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

	first, err := skill.InstallWithOptions("claude", path, "", false)
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

	second, err := skill.InstallWithOptions("claude", path, "", false)
	if err != nil {
		t.Fatalf("second install: %v", err)
	}
	if second.Changed {
		t.Fatal("reinstall rewrote an identical skill")
	}

	out, err := skill.UninstallWithChecksum("claude", path, shippedChecksum())
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

	reuninstall, err := skill.UninstallWithChecksum("claude", path, shippedChecksum())
	if err != nil {
		t.Fatalf("re-uninstall: %v", err)
	}
	if reuninstall.Changed {
		t.Fatal("re-uninstall of an already removed skill claims change")
	}
}

func TestInstallRefusesAnUnknownRuntime(t *testing.T) {
	_, err := skill.InstallWithOptions("windsurf", filepath.Join(t.TempDir(), "SKILL.md"), "", false)
	if err == nil {
		t.Fatal("unknown runtime was accepted")
	}
	if !strings.Contains(err.Error(), "windsurf") {
		t.Errorf("error does not name the runtime: %v", err)
	}
}

// earlierRelease is what an older release of this product wrote: recognizable
// by its opening alone, because pre-zone installs carry no markers.
func earlierRelease() string {
	return skill.LegacySignature() + "description: what v1 shipped\n---\n\nolder body\n"
}

// seedSkill puts a pre-zone SKILL.md in a fresh skills directory of its own.
func seedSkill(t *testing.T, content string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "skills", "roca")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func zonesOf(t *testing.T, path string) artifact.Zones {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	zones, err := artifact.Parse(string(body))
	if err != nil {
		t.Fatal(err)
	}
	return zones
}

// Every pre-zone install on a real machine is either an older release's text or
// the operator's. Migrating one must not keep a whole stale copy of the skill
// beside the current one, preserved forever as though the operator wrote it,
// and must not throw away bytes the operator did write.
func TestInstallMigratesPreZoneContentByWhoWroteIt(t *testing.T) {
	for _, test := range []struct{ name, seeded, user string }{
		{"unrecognized bytes are the operator's", "stale\n", "stale\n"},
		{"an earlier release's skill is replaced", earlierRelease(), ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := seedSkill(t, test.seeded)
			out, err := skill.InstallWithOptions("codex", path, "", false)
			if err != nil {
				t.Fatal(err)
			}
			if !out.Changed {
				t.Fatal("the pre-zone skill was left in place")
			}
			if zones := zonesOf(t, path); zones.System != skill.Content() || zones.User != test.user {
				t.Fatalf("migration = %+v", zones)
			}
		})
	}
}

// A registered skill the operator deleted is exactly what an explicit install
// is for. Refusing it as divergence made `roca skill install <runtime>` write
// nothing, say "unchanged" about a file that was not there, and exit clean.
func TestInstallRewritesARegisteredSkillThatIsGone(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".claude", "skills", "roca", "SKILL.md")
	out, err := skill.InstallWithOptions("claude", path, shippedChecksum(), false)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if !out.Changed || out.Diverged || out.Missing {
		t.Fatalf("install of a deleted registered skill = %+v", out)
	}
	if zones := zonesOf(t, path); zones.System != skill.Content() || zones.User != "" {
		t.Fatalf("restored skill = %+v", zones)
	}
}

// Withdrawing only this release's exact bytes left an older release's SKILL.md
// in the runtime's skills directory, still teaching agents to run a binary the
// same uninstall just unlinked, and outside the data dir it was not even
// reported as kept. What La Roca never wrote is still never removed.
func TestUninstallWithdrawsWhatItRecognizesAndNothingElse(t *testing.T) {
	for _, test := range []struct {
		name, seeded        string
		withdrawn, recovery bool
	}{
		// Its opening is all that recognized it, so any lines the operator appended
		// to it before the zones existed have nowhere else to survive.
		{"an earlier release's skill", earlierRelease(), true, true},
		// The exact bytes this release registered are provably ours, so nothing has
		// to be left behind in the operator's skills directory.
		{"our own registered bytes", skill.Content(), true, false},
		{"our own registered zones", artifact.Zoned(skill.Content(), ""), true, false},
		// Their own zone is theirs, and it leaves in the recovery copy rather than
		// as a SKILL.md without frontmatter that the runtime goes on loading.
		{"our bytes around a zone the operator wrote into",
			artifact.Zoned(skill.Content(), "my own note\n"), true, true},
		{"content this product never wrote", "not roca content", false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := seedSkill(t, test.seeded)
			out, err := skill.UninstallWithChecksum("claude", path, shippedChecksum())
			if err != nil {
				t.Fatalf("uninstall: %v", err)
			}
			if out.Changed != test.withdrawn {
				t.Fatalf("withdrawal = %+v, want changed %v", out, test.withdrawn)
			}
			if _, err := os.Stat(path); os.IsNotExist(err) != test.withdrawn {
				t.Fatalf("the skill file's survival contradicts the withdrawal: %v", err)
			}
			if !test.withdrawn {
				if _, err := os.Stat(filepath.Dir(path)); err != nil {
					t.Fatalf("a directory holding a foreign skill was removed: %v", err)
				}
				return
			}
			if (out.Backup != "") != test.recovery {
				t.Fatalf("recovery copy = %q, want one: %v", out.Backup, test.recovery)
			}
			if !test.recovery {
				return
			}
			kept, err := os.ReadFile(out.Backup)
			if err != nil || string(kept) != test.seeded {
				t.Fatalf("the recovery copy does not hold the withdrawn file: %q, err %v", kept, err)
			}
		})
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
	if _, err := skill.InstallWithOptions(agentcfg.RuntimeClaude, path, "", false); err != nil {
		t.Fatalf("install: %v", err)
	}
	mine := filepath.Join(filepath.Dir(path), "notes-of-mine.md")
	if err := os.WriteFile(mine, []byte("my own notes"), 0o600); err != nil {
		t.Fatal(err)
	}

	out, err := skill.UninstallWithChecksum(agentcfg.RuntimeClaude, path, shippedChecksum())
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
