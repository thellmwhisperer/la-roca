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

// ownedSkillDestinations pairs each skill of the suite with its path resolver,
// so every test that walks them states the pair once.
var ownedSkillDestinations = []struct {
	name string
	path func(string, string, func(string) string) (string, error)
}{
	{skill.SkillName, skill.Path},
	{skill.OperationsName, skill.OperationsPath},
	{skill.VectorName, skill.VectorPath},
	{skill.CatalogName, skill.CatalogPath},
}

func TestRocaSkillIsGeneratedFromTheAgentsPayload(t *testing.T) {
	want, err := os.ReadFile(filepath.Join("..", "..", "..", "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	if skill.Payload() != string(want) {
		t.Fatal("embedded payload drifted from AGENTS.md")
	}
	body := skill.Content()
	_, rest, found := strings.Cut(body, "\n---\n\n")
	if !found || rest != string(want) {
		t.Fatal("roca skill body is not the AGENTS.md payload")
	}
	if !strings.HasPrefix(body, skill.LegacySignature()) {
		t.Fatalf("the generated skill no longer opens with %q", skill.LegacySignature())
	}
}

func TestContentIsANamedRocaSkill(t *testing.T) {
	body := skill.Content()
	if !strings.HasPrefix(body, "---\n") {
		t.Fatal("skill must open with YAML frontmatter")
	}
	if !strings.Contains(body, "name: roca") {
		t.Fatal("skill name must be roca")
	}
	for _, needle := range []string{
		"Must-read on install", "roca init", "roca query", "roca exec",
		"La Roca is an AI agent memory",
	} {
		if !strings.Contains(body, needle) {
			t.Errorf("skill body missing %q", needle)
		}
	}
}

func TestContentTeachesCLIAuthorshipFlags(t *testing.T) {
	body := skill.OperationsContent()
	for _, want := range []string{"--agent", "--model", "automatic", "MCP"} {
		if !strings.Contains(body, want) {
			t.Errorf("operations skill does not teach %q in the authorship contract", want)
		}
	}
}

func TestContentCarriesOperatingCraft(t *testing.T) {
	body := skill.OperationsContent()
	for _, needle := range []string{
		`plugin_roca_ops.memories`,
		"Start project work with the unqualified handoff one-liner",
		"current handoff protocol",
		"always store a handoff",
		"Ask bare first",
		"search the whole corpus",
		"sessions` or `exchanges",
		"ORDER BY timestamp ASC",
		"write the SELECT yourself",
		"Rows are the truth",
		"Use the layer filter deliberately",
		"coordination layers",
		"name: roca-operations",
	} {
		if !strings.Contains(body, needle) {
			t.Errorf("skill operating craft missing %q", needle)
		}
	}
}

func TestContentCanSelfOnboardAnUnsupportedAgent(t *testing.T) {
	body := skill.OperationsContent()
	for _, needle := range []string{
		"Unsupported agent self-onboarding",
		"Never copy real conversation data into a fixture",
		"Before writing a fixture, measure a populated real store read-only",
		"TestRegisteredParsersHarvestPresentAgentStores",
		"docs/agent-parsers.md",
		"Detect",
		"Parse",
		"go test ./pkg/parsers",
		"Open a pull request",
	} {
		if !strings.Contains(body, needle) {
			t.Errorf("skill self-onboarding section missing %q", needle)
		}
	}
}

func TestShippedSkillsCarryTheSearchDoctrine(t *testing.T) {
	for _, test := range []struct {
		name, body   string
		want, refuse []string
	}{
		{
			name: "operations two-branch craft",
			body: skill.OperationsContent(),
			want: []string{
				"name: roca-operations",
				"with or without a vector index",
				"who is X",
				"what happened with Y",
				"## Search craft",
				"completion.json",
				"finished_at",
				"Vector readiness is per selected sidecar",
				"it is not a readiness prerequisite for every\nquery",
				"still returns successfully with notices",
				"unchanged FTS/SQL path",
				"the hybrid loop is mandatory",
				"last resort",
				"Agents never pass `--full`",
				"plugin_roca_ops.memories",
				"Write the SQL yourself",
				"roca exec",
				"roca query",
				"roca explore",
				"Invite the user to build the index",
				"## Hybrid loop",
				"Vector search finds the nearby rows",
				"FTS censuses them",
				"SQL frames them",
				"the shipped RRF hybrid",
				"inference only at the end",
				`roca vector query --databases <scope>`,
				`roca vector query "<first-person phrase or bare word>" 100`,
				"one query can mix a declared plugin hit with a\n   corpus hit",
				"A selected database without a `Vector:` declaration joins\n   here through its unchanged FTS path",
				"names of people",
				"my boss is named",
				"Use them only as a last resort when you cannot write the SELECT yourself",
			},
			refuse: []string{
				"roca vector vocab", "## Hybrid discovery",
				"Declared sidecars are ready only when",
				"refuses when no declared sidecar is ready",
			},
		},
		{
			name: "vector owns the index",
			body: skill.VectorContent(),
			want: []string{
				"name: roca-vector",
				"invite the user to",
				"roca vector install",
				"roca vector ingest --delta",
				"roca vector compact",
				"worker.log",
				"completion.json",
				"finished_at",
				"exit_status == 0",
				"Otherwise treat the index as unavailable",
			},
			refuse: []string{"## Hybrid discovery", "## Hybrid loop", "roca vector vocab"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, needle := range test.want {
				if !strings.Contains(test.body, needle) {
					t.Errorf("missing %q", needle)
				}
			}
			for _, needle := range test.refuse {
				if strings.Contains(test.body, needle) {
					t.Errorf("must not teach %q", needle)
				}
			}
		})
	}
	if strings.Contains(skill.Content(), "roca vector vocab") {
		t.Error("definitive skill still teaches roca vector vocab")
	}
}

func TestPluginGuideDocumentsTheChronologicalVectorAuthorContract(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "..", "docs", "plugins.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"the author contract names the table,\nstable id column, opt-in prose columns, and chronological source",
		"{\"name\": \"receipts\",\n \"id_column\": \"id\",\n \"text_columns\": [\"title\"],\n \"time_columns\": [\"created_at\"]}",
		"a\ndatabase with no `vector` declaration continues to serve through FTS and SQL\nexactly as before",
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("plugin author guide missing %q", want)
		}
	}
}

func TestOnlyThePreviouslyShippedSkillHasALegacySignature(t *testing.T) {
	embedded := skill.EmbeddedSkills()
	if embedded[0].Name != skill.SkillName || embedded[0].Legacy == "" {
		t.Fatalf("definitive skill legacy signature = %q", embedded[0].Legacy)
	}
	for _, shipped := range embedded[1:] {
		if shipped.Legacy != "" {
			t.Errorf("new skill %s has legacy signature %q", shipped.Name, shipped.Legacy)
		}
		_, legacy := skill.ContentForPath(filepath.Join("skills", shipped.Name, "SKILL.md"))
		if legacy != "" {
			t.Errorf("new skill %s path has legacy signature %q", shipped.Name, legacy)
		}
	}
}

func TestDetectedNamesOnlyExistingRoots(t *testing.T) {
	home := t.TempDir()
	if got := skill.Detected(home, nil); len(got) != 0 {
		t.Fatalf("empty home detected %v", got)
	}
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o700); err != nil {
		t.Fatal(err)
	}
	got := skill.Detected(home, nil)
	if len(got) != 2 || got[0] != "claude" || got[1] != "codex" {
		t.Fatalf("detected = %v, want [claude codex]", got)
	}
}

func TestDetectedFindsCursorFromTheConfigRootWithoutASkillsDir(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".cursor"), 0o700); err != nil {
		t.Fatal(err)
	}
	got := skill.Detected(home, nil)
	if len(got) != 1 || got[0] != "cursor" {
		t.Fatalf("detected = %v, want [cursor]", got)
	}
	path, err := skill.Path("cursor", home, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(path, filepath.Join(".cursor", "skills", "roca")) {
		t.Fatalf("cursor skill path = %s, want ~/.cursor/skills/roca", path)
	}
	if strings.Contains(path, "skills-cursor") {
		t.Fatal("cursor skill path must not write into the built-in skills-cursor directory")
	}
}

func TestRuntimesAreTheSkillSeatsThisProductMeasured(t *testing.T) {
	want := []string{"claude", "codex", "cursor", "grok", "hermes", "opencode", "pi", "qwen"}
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

// Every skill of the suite resolves under the same measured roots, so one
// table states each runtime's roca path and its roca-semantica path.
func TestPathResolvesEachRuntimeUnderATempHome(t *testing.T) {
	home := t.TempDir()
	roots := map[string][]string{
		"claude":   {".claude"},
		"codex":    {".codex"},
		"cursor":   {".cursor"},
		"grok":     {".grok"},
		"hermes":   {".hermes"},
		"opencode": {".config", "opencode"},
		"pi":       {".pi", "agent"},
		"qwen":     {".qwen"},
	}
	for runtime, dir := range roots {
		for _, owned := range ownedSkillDestinations {
			want := filepath.Join(append(append([]string{home}, dir...), "skills", owned.name, "SKILL.md")...)
			got, err := owned.path(runtime, home, nil)
			if err != nil {
				t.Fatalf("%s: %v", runtime, err)
			}
			if got != want {
				t.Errorf("%s %s path = %s, want %s", runtime, owned.name, got, want)
			}
		}
	}
}

func TestPathHonoursRuntimeEnvOverrides(t *testing.T) {
	home := t.TempDir()
	elsewhere := filepath.Join(home, "elsewhere")
	env := func(key string) string {
		switch key {
		case "CLAUDE_CONFIG_DIR", "CODEX_HOME", "CURSOR_HOME", "GROK_HOME", "HERMES_HOME",
			"PI_CODING_AGENT_DIR", "QWEN_HOME":
			return elsewhere
		case "OPENCODE_CONFIG":
			return filepath.Join(elsewhere, "opencode.json")
		default:
			return ""
		}
	}
	for _, runtime := range skill.Runtimes() {
		for _, owned := range ownedSkillDestinations {
			got, err := owned.path(runtime, home, env)
			if err != nil {
				t.Fatalf("%s: %v", runtime, err)
			}
			if !strings.HasPrefix(got, elsewhere) {
				t.Errorf("%s %s path = %s, want under %s", runtime, owned.name, got, elsewhere)
			}
			wantSuffix := filepath.Join("skills", owned.name, "SKILL.md")
			if !strings.HasSuffix(got, wantSuffix) {
				t.Errorf("%s %s path = %s, want …/%s", runtime, owned.name, got, wantSuffix)
			}
		}
	}
}

// Both skills of the suite share one zoned install contract, so one table
// walks each of them through the same write, idempotent rewrite and withdrawal.
func TestInstallWritesTheSkillAndIsIdempotent(t *testing.T) {
	catalog := catalogFixture()
	home := t.TempDir()
	skills := []struct {
		name     string
		path     string
		content  string
		install  func(string, string, bool) (skill.Outcome, error)
		checksum func() string
	}{
		{
			name:    skill.SkillName,
			path:    filepath.Join(home, ".claude", "skills", "roca", "SKILL.md"),
			content: skill.Content(),
			install: func(path, previous string, force bool) (skill.Outcome, error) {
				return skill.InstallWithOptions("claude", path, previous, force)
			},
			checksum: shippedChecksum,
		},
		{
			name:    skill.OperationsName,
			path:    filepath.Join(home, ".claude", "skills", "roca-operations", "SKILL.md"),
			content: skill.OperationsContent(),
			install: func(path, previous string, force bool) (skill.Outcome, error) {
				return skill.InstallNamed("claude", path, skill.OperationsContent(),
					"", previous, force, true)
			},
			checksum: func() string { return artifact.Checksum(skill.OperationsContent()) },
		},
		{
			name:    skill.VectorName,
			path:    filepath.Join(home, ".claude", "skills", "roca-vector", "SKILL.md"),
			content: skill.VectorContent(),
			install: func(path, previous string, force bool) (skill.Outcome, error) {
				return skill.InstallNamed("claude", path, skill.VectorContent(),
					"", previous, force, true)
			},
			checksum: func() string { return artifact.Checksum(skill.VectorContent()) },
		},
		{
			name:    skill.CatalogName,
			path:    filepath.Join(home, ".claude", "skills", "roca-semantica", "SKILL.md"),
			content: catalog,
			install: func(path, previous string, force bool) (skill.Outcome, error) {
				return skill.InstallCatalogWithOptions("claude", path, catalog, previous, force, true)
			},
			checksum: func() string { return artifact.Checksum(catalog) },
		},
	}
	for _, owned := range skills {
		t.Run(owned.name, func(t *testing.T) {
			first, err := owned.install(owned.path, "", false)
			if err != nil {
				t.Fatalf("first install: %v", err)
			}
			if !first.Changed || first.Path != owned.path {
				t.Fatalf("first = %+v", first)
			}
			body, err := os.ReadFile(owned.path)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(string(body), "---\n# ROCA SYSTEM BEGIN\n") {
				t.Fatalf("installed skill no longer opens with YAML frontmatter: %q", string(body[:min(len(body), 40)]))
			}
			zones, err := artifact.Parse(string(body))
			if err != nil || zones.System != owned.content || zones.User != "" {
				t.Fatalf("written skill zones = %+v, err %v", zones, err)
			}

			second, err := owned.install(owned.path, "", false)
			if err != nil {
				t.Fatalf("second install: %v", err)
			}
			if second.Changed {
				t.Fatal("reinstall rewrote an identical skill")
			}

			out, err := skill.UninstallWithChecksum("claude", owned.path, owned.checksum())
			if err != nil {
				t.Fatalf("uninstall: %v", err)
			}
			if !out.Changed {
				t.Fatal("uninstall of an installed skill changed nothing")
			}
			if _, err := os.Stat(owned.path); !os.IsNotExist(err) {
				t.Fatal("skill file survived uninstall")
			}
			if _, err := os.Stat(filepath.Dir(owned.path)); !os.IsNotExist(err) {
				t.Fatal("skill directory survived uninstall")
			}

			reuninstall, err := skill.UninstallWithChecksum("claude", owned.path, owned.checksum())
			if err != nil {
				t.Fatalf("re-uninstall: %v", err)
			}
			if reuninstall.Changed {
				t.Fatal("re-uninstall of an already removed skill claims change")
			}
		})
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
	zones, err := artifact.ParseFile(path)
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

func TestUninstallDoesNotApplyRocaLegacyOwnershipToNewSkills(t *testing.T) {
	home := t.TempDir()
	for _, embedded := range skill.EmbeddedSkills()[1:] {
		path, err := skill.NamedPath("claude", embedded.Name, home, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(earlierRelease()), 0o600); err != nil {
			t.Fatal(err)
		}
		out, err := skill.UninstallWithChecksum("claude", path, artifact.Checksum(embedded.Body))
		if err != nil {
			t.Fatal(err)
		}
		if out.Changed || out.Backup != "" {
			t.Errorf("uninstall claimed unproven %s skill: %+v", embedded.Name, out)
		}
		if _, err := os.Stat(path); err != nil {
			t.Errorf("uninstall removed unproven %s skill: %v", embedded.Name, err)
		}
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
