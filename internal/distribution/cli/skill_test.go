package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	body, err := os.ReadFile(want)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != skill.Content() {
		t.Fatal("installed file does not match the embedded skill")
	}

	var again strings.Builder
	runSkill(t, &again, "skill", "install", "claude")
	if !strings.Contains(again.String(), "unchanged "+want) {
		t.Fatalf("reinstall did not report unchanged:\n%s", again.String())
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
