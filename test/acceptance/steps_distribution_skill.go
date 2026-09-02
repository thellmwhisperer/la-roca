//go:build acceptance

package acceptance

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cucumber/godog"
	"github.com/thellmwhisperer/la-roca/internal/artifact"
)

func registerDistributionSkillSteps(ctx *godog.ScenarioContext, w *distributionWorld) {
	ctx.When(`^the operator installs the skill for "([^"]*)"$`, w.installSkillFor)
	ctx.Then(`^only "([^"]*)" receives the canonical skill and the output names its path$`, w.onlyChosenAgentHasSkill)
	ctx.Then(`^only "([^"]*)" receives the generated semantic catalog and no other runtime does$`,
		w.onlyChosenAgentHasCatalog)
	ctx.When(`^the operator writes their own lines into the skill's operator zone$`, w.editSkillOperatorZone)
	ctx.Then(`^the operator's lines survive, the product zone is canonical, and the registry records the skill$`,
		w.skillIsARegisteredArtifact)
	ctx.When(`^the operator requests a skill install without choosing an agent or all agents$`, w.installSkillWithoutChoice)
	ctx.Then(`^the request fails and every agent home remains without the skill$`, w.noAgentReceivedSkill)
	ctx.Given(`^synthetic agent instruction files with operator-owned content$`, w.syntheticInstructionFiles)
	ctx.When(`^the operator initializes La Roca$`, w.initializeWithInstructionFiles)
	ctx.Then(`^prompt.md is created and every agent instruction file is unchanged$`, w.promptIsSeparateFromInstructions)
	ctx.Then(`^init points to prompt.md without printing its contents$`, w.initPointsToPrompt)
}

func (w *distributionWorld) initPointsToPrompt() error {
	prompt := filepath.Join(w.home, ".roca", "prompt.md")
	if !strings.Contains(w.last.stdout, "agent prompt: "+prompt) ||
		!strings.Contains(w.last.stdout, "Paste its contents into the agent instructions you choose.") {
		return fmt.Errorf("init does not give the prompt path and action: %s", w.last.stdout)
	}
	if strings.Contains(w.last.stdout, "## La Roca — local semantic memory") ||
		strings.Contains(w.last.stdout, "La Roca never edits agent instruction files") {
		return fmt.Errorf("init dumped prompt.md into the terminal: %s", w.last.stdout)
	}
	return nil
}

func (w *distributionWorld) installSkillFor(agent string) error {
	if err := w.ensurePrepared(); err != nil {
		return err
	}
	w.last = w.run("skill", "install", agent)
	return nil
}

func (w *distributionWorld) onlyChosenAgentHasSkill(agent string) error {
	if w.last.code != 0 {
		return fmt.Errorf("skill install failed: %s", w.last.stderr)
	}
	wanted, err := distributionSkillPath(agent, w.home)
	if err != nil {
		return err
	}
	if !strings.Contains(w.last.stdout, wanted) {
		return fmt.Errorf("skill output does not name %s: %s", wanted, w.last.stdout)
	}
	for _, runtime := range distributionAgents {
		path, _ := distributionSkillPath(runtime, w.home)
		body, readErr := os.ReadFile(path)
		if runtime == agent {
			if readErr != nil || !strings.Contains(string(body), "name: roca") {
				return fmt.Errorf("%s did not receive the canonical skill at %s: %v", runtime, path, readErr)
			}
			continue
		}
		if !os.IsNotExist(readErr) {
			return fmt.Errorf("unchosen agent %s received %s", runtime, path)
		}
	}
	return nil
}

// The install writes the suite's second skill — the semantic catalog generated
// from the installed plugin manifests — beside the canonical one, so the same
// chosen-runtime discipline holds for it.
func (w *distributionWorld) onlyChosenAgentHasCatalog(agent string) error {
	if w.last.code != 0 {
		return fmt.Errorf("skill install failed: %s", w.last.stderr)
	}
	for _, runtime := range distributionAgents {
		path, _ := distributionCatalogSkillPath(runtime, w.home)
		body, readErr := os.ReadFile(path)
		if runtime == agent {
			if readErr != nil || !strings.Contains(string(body), "name: roca-semantica") {
				return fmt.Errorf("%s did not receive the catalog skill at %s: %v", runtime, path, readErr)
			}
			continue
		}
		if !os.IsNotExist(readErr) {
			return fmt.Errorf("unchosen agent %s received the catalog skill at %s", runtime, path)
		}
	}
	return nil
}

const acceptanceOperatorSkillLine = "My own note beside the roca skill.\n"

func (w *distributionWorld) editSkillOperatorZone() error {
	path, err := distributionSkillPath("claude", w.home)
	if err != nil {
		return err
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	edited := strings.Replace(string(body), artifact.UserEnd,
		acceptanceOperatorSkillLine+artifact.UserEnd, 1)
	if edited == string(body) {
		return fmt.Errorf("the installed skill has no operator zone to write into: %s", body)
	}
	return os.WriteFile(path, []byte(edited), 0o600)
}

func (w *distributionWorld) skillIsARegisteredArtifact() error {
	if w.last.code != 0 {
		return fmt.Errorf("the second skill install failed: %s", w.last.stderr)
	}
	path, err := distributionSkillPath("claude", w.home)
	if err != nil {
		return err
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	zones, err := artifact.Parse(string(body))
	if err != nil {
		return fmt.Errorf("the refreshed skill lost its zones: %v", err)
	}
	if zones.User != acceptanceOperatorSkillLine {
		return fmt.Errorf("the operator zone was not transplanted verbatim: %q", zones.User)
	}
	if !strings.Contains(zones.System, "name: roca") {
		return fmt.Errorf("the product zone is not the canonical skill: %q", zones.System)
	}
	registry, err := os.ReadFile(filepath.Join(w.home, ".roca", "artifacts.json"))
	if err != nil {
		return fmt.Errorf("the install registered no artifact: %v", err)
	}
	if !strings.Contains(string(registry), path) || !strings.Contains(string(registry), `"skill"`) {
		return fmt.Errorf("the registry does not record the installed skill: %s", registry)
	}
	return nil
}

func (w *distributionWorld) installSkillWithoutChoice() error {
	if err := w.ensurePrepared(); err != nil {
		return err
	}
	w.last = w.run("skill", "install")
	return nil
}

func (w *distributionWorld) noAgentReceivedSkill() error {
	if w.last.code == 0 {
		return fmt.Errorf("skill install without a choice succeeded")
	}
	for _, runtime := range distributionAgents {
		path, _ := distributionSkillPath(runtime, w.home)
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			return fmt.Errorf("%s received a skill without being selected: %v", runtime, err)
		}
		catalog, _ := distributionCatalogSkillPath(runtime, w.home)
		if _, err := os.Stat(catalog); !os.IsNotExist(err) {
			return fmt.Errorf("%s received the catalog skill without being selected: %v", runtime, err)
		}
	}
	return nil
}

func (w *distributionWorld) syntheticInstructionFiles() error {
	w.home = filepath.Join(w.root, "prompt")
	if err := os.MkdirAll(filepath.Join(w.home, ".tmp"), 0o700); err != nil {
		return err
	}
	w.installed = filepath.Join(w.home, "roca")
	if err := copyAcceptanceFile(w.binary, w.installed, 0o755); err != nil {
		return err
	}
	files := []string{
		filepath.Join(w.home, ".claude", "CLAUDE.md"),
		filepath.Join(w.home, ".codex", "AGENTS.md"),
		filepath.Join(w.home, ".config", "opencode", "AGENTS.md"),
		filepath.Join(w.home, ".hermes", "AGENTS.md"),
		filepath.Join(w.home, ".pi", "agent", "AGENTS.md"),
	}
	before := map[string]string{}
	for index, path := range files {
		content := fmt.Sprintf("# Operator instructions %d\nKeep this exact text.\n", index+1)
		if err := writeAcceptanceFixture(path, content); err != nil {
			return err
		}
		before[path] = content
	}
	w.state["instructions"] = before
	return nil
}

func (w *distributionWorld) initializeWithInstructionFiles() error {
	w.last = w.runAt(w.home, w.installed, "init", "--db-path", filepath.Join(w.home, ".roca", "roca.db"))
	return nil
}

func (w *distributionWorld) promptIsSeparateFromInstructions() error {
	if w.last.code != 0 {
		return fmt.Errorf("init failed: %s", w.last.stderr)
	}
	prompt := filepath.Join(w.home, ".roca", "prompt.md")
	body, err := os.ReadFile(prompt)
	if err != nil || !strings.Contains(string(body), "La Roca never edits agent instruction files") {
		return fmt.Errorf("canonical prompt was not created at %s: %v", prompt, err)
	}
	if !strings.Contains(w.last.stdout, prompt) {
		return fmt.Errorf("init did not name prompt.md: %s", w.last.stdout)
	}
	before := w.state["instructions"].(map[string]string)
	for path, expected := range before {
		current, readErr := os.ReadFile(path)
		if readErr != nil || string(current) != expected {
			return fmt.Errorf("agent instruction file changed at %s: %v", path, readErr)
		}
	}
	return nil
}

var distributionAgents = []string{"claude", "codex", "cursor", "grok", "hermes", "opencode", "pi", "qwen", "zcode"}

func distributionSkillPath(agent, home string) (string, error) {
	return distributionSkillFile(agent, home, "roca")
}

func distributionCatalogSkillPath(agent, home string) (string, error) {
	return distributionSkillFile(agent, home, "roca-semantica")
}

func distributionSkillFile(agent, home, skill string) (string, error) {
	var parts []string
	switch agent {
	case "claude", "codex", "cursor", "grok", "hermes", "qwen", "zcode":
		parts = []string{"." + agent}
	case "opencode":
		parts = []string{".config", "opencode"}
	case "pi":
		parts = []string{".pi", "agent"}
	default:
		return "", fmt.Errorf("unknown agent %q", agent)
	}
	return filepath.Join(append([]string{home}, append(parts, "skills", skill, "SKILL.md")...)...), nil
}
