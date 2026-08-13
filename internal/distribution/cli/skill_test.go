package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/artifact"
	"github.com/thellmwhisperer/la-roca/internal/distribution/skill"
	"github.com/thellmwhisperer/la-roca/internal/provider/service"
)

func TestSkillBareListsEveryRuntimePath(t *testing.T) {
	home := skillTestHome(t)
	var output strings.Builder
	runSkill(t, &output, "skill")
	text := output.String()
	if !strings.HasPrefix(text, "rows[5]{runtime,path}:\n") {
		t.Fatalf("listing is not the stable TOON shape:\n%s", text)
	}
	for _, runtime := range skill.Runtimes() {
		if !strings.Contains(text, "\n  "+runtime+",") {
			t.Errorf("listing missing runtime %q:\n%s", runtime, text)
		}
	}
	if !strings.Contains(text, filepath.Join(home, ".claude", "skills", "roca", "SKILL.md")) {
		t.Errorf("listing does not show skill paths:\n%s", text)
	}
}

func TestSkillInstallWritesUnderTempHome(t *testing.T) {
	home := skillTestHome(t)
	want := filepath.Join(home, ".claude", "skills", "roca", "SKILL.md")

	var output strings.Builder
	runSkill(t, &output, "skill", "install", "claude")
	if !strings.Contains(output.String(), "wrote "+want) {
		t.Fatalf("install did not narrate the write:\n%s", output.String())
	}
	zones, err := artifact.ParseFile(want)
	if err != nil || zones.System != skill.Content() || zones.User != "" {
		t.Fatalf("installed zones = %+v, err %v", zones, err)
	}
	registry, err := artifact.LoadRegistry(filepath.Join(home, ".roca", "artifacts.json"))
	if err != nil {
		t.Fatal(err)
	}
	entry, ok := registry.Find("skill", "claude", want)
	if !ok || entry.SystemSHA256 != artifact.Checksum(skill.Content()) {
		t.Fatalf("registered skill = %+v, found %v", entry, ok)
	}

	var again strings.Builder
	runSkill(t, &again, "skill", "install", "claude")
	if !strings.Contains(again.String(), "unchanged "+want) {
		t.Fatalf("reinstall did not report unchanged:\n%s", again.String())
	}
}

// The two refusals an explicit install can meet are not the same. A registered
// file the operator deleted has no bytes of theirs to clobber and the install
// they typed is the consent, so it is simply written again; refusing it printed
// "unchanged" about a file that was not there and exited clean. A zoned file no
// registry record stands behind is still refused, because its SYSTEM zone was
// never proven to be ours — and it must not be reported as somebody's edit.
func TestSkillInstallRestoresARemovedFileAndRefusesAnUnregisteredOne(t *testing.T) {
	home := skillTestHome(t)
	path := filepath.Join(home, ".claude", "skills", "roca", "SKILL.md")
	var output strings.Builder
	runSkill(t, &output, "skill", "install", "claude")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}

	var restored, warning strings.Builder
	root := rootCommand(&cliEnv{out: &restored, errOut: &warning})
	root.SetArgs([]string{"skill", "install", "claude"})
	if err := root.Execute(); err != nil {
		t.Fatalf("reinstall over a removed file failed: %v", err)
	}
	if warning.String() != "" {
		t.Fatalf("an install the operator asked for by name warned instead of writing: %q", warning.String())
	}
	if !strings.Contains(restored.String(), "claude: wrote "+path) {
		t.Fatalf("the reinstall did not report the file it wrote:\n%s", restored.String())
	}
	if zones := installedZones(t, path); zones.System != skill.Content() {
		t.Fatalf("the reinstall did not restore the skill: %+v", zones)
	}

	if err := os.Remove(filepath.Join(home, ".roca", "artifacts.json")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, artifact.Zoned("an older release's system\n", "mine\n"))
	warning.Reset()
	root = rootCommand(&cliEnv{out: &restored, errOut: &warning})
	root.SetArgs([]string{"skill", "install", "claude"})
	if err := root.Execute(); err != nil {
		t.Fatalf("install over an unregistered skill failed: %v", err)
	}
	if !strings.Contains(warning.String(), "no record in La Roca's artifact registry") ||
		!strings.Contains(warning.String(), "skill install claude --force") {
		t.Fatalf("an unregistered artifact was reported as an edit: %q", warning.String())
	}
	if zones := installedZones(t, path); zones.System != "an older release's system\n" {
		t.Fatalf("an unregistered skill was replaced without consent: %+v", zones)
	}

	runSkill(t, &restored, "skill", "install", "claude", "--force")
	if zones := installedZones(t, path); zones.System != skill.Content() || zones.User != "mine\n" {
		t.Fatalf("the forced install did not replace SYSTEM and keep USER: %+v", zones)
	}
}

func installedZones(t *testing.T, path string) artifact.Zones {
	t.Helper()
	zones, err := artifact.ParseFile(path)
	if err != nil {
		t.Fatalf("installed skill has no zones: %v", err)
	}
	return zones
}

// One runtime whose file nothing can read never decides for the others, and
// the migration that replaced an operator's bytes says where they went.
func TestSkillInstallAllSurvivesOneUnreadableRuntimeAndNamesTheBackup(t *testing.T) {
	home := skillTestHome(t)
	broken := filepath.Join(home, ".codex", "skills", "roca", "SKILL.md")
	writeFile(t, broken, artifact.Zoned(skill.Content(), "")+"appended after the last marker\n")
	migrated := filepath.Join(home, ".hermes", "skills", "roca", "SKILL.md")
	writeFile(t, migrated, "an older release's skill\n")

	var out, warnings strings.Builder
	root := rootCommand(&cliEnv{out: &out, errOut: &warnings})
	root.SetArgs([]string{"skill", "install", "--all"})
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "skill install codex --force") {
		t.Fatalf("the unreadable runtime did not fail with its remedy: %v", err)
	}
	for _, runtime := range []string{"claude", "hermes", "opencode", "pi"} {
		if !strings.Contains(out.String(), runtime+": wrote ") {
			t.Fatalf("%s was skipped because codex could not be read:\n%s", runtime, out.String())
		}
	}
	if !strings.Contains(out.String(), migrated+" (replaced content kept at ") {
		t.Fatalf("the migration did not name the recovery copy:\n%s", out.String())
	}
}

func TestSkillInstallAllNarratesEveryPath(t *testing.T) {
	skillTestHome(t)
	var output strings.Builder
	runSkill(t, &output, "skill", "install", "--all")
	text := output.String()
	for _, runtime := range skill.Runtimes() {
		if !strings.Contains(text, runtime+": wrote ") {
			t.Errorf("missing write line for %s:\n%s", runtime, text)
		}
	}
}

func TestInitMentionsTheSkillWithoutInstallingIt(t *testing.T) {
	var output strings.Builder
	renderBootstrap(&cliEnv{out: &output}, service.InitResult{})
	if !strings.Contains(output.String(), "roca skill install") {
		t.Fatalf("init does not mention the skill:\n%s", output.String())
	}
	if strings.Contains(output.String(), "wrote ") {
		t.Fatalf("init must not install the skill:\n%s", output.String())
	}
}

func TestSkillTeachesTheInvestigationFunnel(t *testing.T) {
	body := skill.Content()
	for _, want := range []string{
		"## Investigation method", "Declare the purpose in one line",
		"roca explore --deep", "single bare word", "Read the terrain",
		"one concept per query", "FTS ANDs", "explicit OR", "whole corpus",
		"--sql-only", "roca exec", "Verdict grounded in rows",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("investigator skill lacks %q", want)
		}
	}
}

func skillTestHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, key := range []string{
		"CLAUDE_CONFIG_DIR", "CODEX_HOME", "OPENCODE_CONFIG",
		"HERMES_HOME", "PI_CODING_AGENT_DIR",
	} {
		t.Setenv(key, "")
	}
	return home
}

func runSkill(t *testing.T, out *strings.Builder, args ...string) {
	t.Helper()
	root := rootCommand(&cliEnv{out: out})
	root.SetArgs(args)
	if err := root.Execute(); err != nil {
		t.Fatalf("roca %s: %v", strings.Join(args, " "), err)
	}
}
