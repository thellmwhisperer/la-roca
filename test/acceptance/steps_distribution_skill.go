//go:build acceptance

package acceptance

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cucumber/godog"
)

func registerDistributionSkillSteps(ctx *godog.ScenarioContext, w *distributionWorld) {
	ctx.When(`^the operator installs the skill for "([^"]*)"$`, w.installSkillFor)
	ctx.Then(`^only "([^"]*)" receives the canonical skill and the output names its path$`, w.onlyChosenAgentHasSkill)
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

var distributionAgents = []string{"claude", "codex", "hermes", "opencode", "pi"}

func distributionSkillPath(agent, home string) (string, error) {
	var parts []string
	switch agent {
	case "claude", "codex", "hermes":
		parts = []string{"." + agent}
	case "opencode":
		parts = []string{".config", "opencode"}
	case "pi":
		parts = []string{".pi", "agent"}
	default:
		return "", fmt.Errorf("unknown agent %q", agent)
	}
	return filepath.Join(append([]string{home}, append(parts, "skills", "roca", "SKILL.md")...)...), nil
}
