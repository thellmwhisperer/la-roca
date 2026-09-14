//go:build acceptance

package acceptance

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
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
	ctx.Given(`^every supported harness already has a session hook of its own$`,
		w.harnessesWithTheirOwnSessionHook)
	ctx.When(`^the operator installs the La Roca session hooks for every supported harness$`,
		w.installSessionHooksEverywhere)
	ctx.Then(`^every harness carries the La Roca session hook beside the hook it already had$`,
		w.everyHarnessCarriesTheSessionHook)
	ctx.Then(`^withdrawing them leaves every harness with only the hook it already had$`,
		w.withdrawalLeavesOnlyTheForeignHook)
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

// Every harness in this scenario starts with a session hook another tool
// installed, because that is how the machines this ships to actually look. The
// contract is one added hook per harness, never a replaced one.
var distributionHookFixtures = map[string]struct{ path, body, marker, foreign string }{
	"claude": {
		path:    filepath.Join(".claude", "settings.json"),
		body:    `{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"pane-state session"}]}]}}`,
		marker:  "pane-state session",
		foreign: "pane-state session",
	},
	"codex": {
		path:    filepath.Join(".codex", "hooks.json"),
		body:    `{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"pane-state session","timeout":10}]}]}}`,
		marker:  "pane-state session",
		foreign: "pane-state session",
	},
	"cursor": {
		path:    filepath.Join(".cursor", "hooks.json"),
		body:    `{"version":1,"hooks":{"sessionStart":[{"command":"pane-state session"}]}}`,
		marker:  "pane-state session",
		foreign: "pane-state session",
	},
	"zcode": {
		path:    filepath.Join(".zcode", "cli", "config.json"),
		body:    `{"hooks":{"enabled":true,"events":{"SessionStart":[{"hooks":[{"type":"command","command":"pane-state","timeoutMs":5000}]}]}}}`,
		marker:  `"pane-state"`,
		foreign: "pane-state",
	},
	"pi": {
		path:   filepath.Join(".pi", "agent", "extensions", "pane-state.ts"),
		body:   "export default function () {}\n",
		marker: "export default function () {}",
	},
	"opencode": {
		path:   filepath.Join(".config", "opencode", "plugins", "pane-state.js"),
		body:   "export default { id: \"pane.state\" };\n",
		marker: `id: "pane.state"`,
	},
}

func distributionHookRuntimes() []string {
	names := make([]string, 0, len(distributionHookFixtures))
	for name := range distributionHookFixtures {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (w *distributionWorld) harnessesWithTheirOwnSessionHook() error {
	if err := w.ensurePrepared(); err != nil {
		return err
	}
	for _, runtime := range distributionHookRuntimes() {
		fixture := distributionHookFixtures[runtime]
		path := filepath.Join(w.home, fixture.path)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(fixture.body), 0o600); err != nil {
			return err
		}
	}
	return nil
}

func (w *distributionWorld) installSessionHooksEverywhere() error {
	for _, runtime := range distributionHookRuntimes() {
		if run := w.run("hooks", "install", runtime, "--pills", "--handoff"); run.code != 0 {
			return fmt.Errorf("hooks install %s failed: %s%s", runtime, run.stdout, run.stderr)
		}
	}
	return nil
}

func (w *distributionWorld) everyHarnessCarriesTheSessionHook() error {
	for _, runtime := range distributionHookRuntimes() {
		fixture := distributionHookFixtures[runtime]
		body, err := os.ReadFile(filepath.Join(w.home, fixture.path))
		if err != nil {
			return err
		}
		switch runtime {
		case "pi", "opencode":
			if !strings.Contains(string(body), fixture.marker) {
				return fmt.Errorf("%s lost the hook it already had: %s", runtime, body)
			}
			script, err := os.ReadFile(distributionSessionScript(runtime, w.home))
			if err != nil {
				return err
			}
			if !strings.Contains(string(script), `"--runtime", "`+runtime+`"`) {
				return fmt.Errorf("%s did not receive the La Roca session hook: %s", runtime, script)
			}
		default:
			commands, err := harnessHookCommands(runtime, body)
			if err != nil {
				return err
			}
			if countCommands(commands, fixture.foreign) != 1 {
				return fmt.Errorf("%s lost the hook it already had: %v", runtime, commands)
			}
			wanted := distributionSessionHookCommand(w.installed, runtime)
			if runtime == "zcode" {
				wanted = filepath.Join(w.home, ".zcode", "hooks", "roca-handoff.sh")
			}
			if countCommands(commands, wanted) != 1 {
				return fmt.Errorf("%s did not receive the La Roca session hook: %v", runtime, commands)
			}
		}
	}
	return nil
}

func (w *distributionWorld) withdrawalLeavesOnlyTheForeignHook() error {
	for _, runtime := range distributionHookRuntimes() {
		if run := w.run("hooks", "uninstall", runtime); run.code != 0 {
			return fmt.Errorf("hooks uninstall %s failed: %s%s", runtime, run.stdout, run.stderr)
		}
	}
	for _, runtime := range distributionHookRuntimes() {
		fixture := distributionHookFixtures[runtime]
		path := filepath.Join(w.home, fixture.path)
		body, err := os.ReadFile(path)
		if runtime == "pi" || runtime == "opencode" {
			if err != nil {
				return fmt.Errorf("%s lost the extension it already had: %v", runtime, err)
			}
			if _, err := os.Stat(distributionSessionScript(runtime, w.home)); !os.IsNotExist(err) {
				return fmt.Errorf("%s kept the La Roca script after a withdrawal", runtime)
			}
			if !strings.Contains(string(body), fixture.marker) {
				return fmt.Errorf("withdrawal removed %s's own hook: %s", runtime, body)
			}
			continue
		}
		if err != nil {
			return err
		}
		commands, err := harnessHookCommands(runtime, body)
		if err != nil {
			return err
		}
		if countCommands(commands, fixture.foreign) != 1 {
			return fmt.Errorf("withdrawal removed %s's own hook: %v", runtime, commands)
		}
		wanted := distributionSessionHookCommand(w.installed, runtime)
		if runtime == "zcode" {
			wanted = filepath.Join(w.home, ".zcode", "hooks", "roca-handoff.sh")
		}
		if countCommands(commands, wanted) != 0 {
			return fmt.Errorf("%s kept the La Roca session hook after a withdrawal: %v", runtime, commands)
		}
	}
	return nil
}

func distributionSessionScript(runtime, home string) string {
	if runtime == "pi" {
		return filepath.Join(home, ".pi", "agent", "extensions", "roca-session.ts")
	}
	return filepath.Join(home, ".config", "opencode", "plugins", "roca-session.js")
}

func distributionSessionHookCommand(binary, runtime string) string {
	return "'" + strings.ReplaceAll(binary, "'", `'"'"'`) + "' hooks run session --runtime " +
		runtime + " --pills --handoff"
}

func harnessHookCommands(runtime string, body []byte) ([]string, error) {
	var document map[string]any
	if err := json.Unmarshal(body, &document); err != nil {
		return nil, fmt.Errorf("%s hook document is not JSON: %w", runtime, err)
	}
	switch runtime {
	case "claude", "codex":
		return nestedHookCommands(document["hooks"], "SessionStart")
	case "cursor":
		return flatHookCommands(document["hooks"], "sessionStart")
	case "zcode":
		hooks, _ := document["hooks"].(map[string]any)
		if hooks == nil {
			return nil, fmt.Errorf("zcode hooks must be an object")
		}
		return nestedHookCommands(hooks["events"], "SessionStart")
	default:
		return nil, fmt.Errorf("%s has no JSON hook document", runtime)
	}
}

// hookEventEntries is the part both document shapes share: the container has
// to be an object, and the event it holds has to be a list.
func hookEventEntries(raw any, event string) ([]any, error) {
	root, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("hooks container is not an object")
	}
	entries, ok := root[event].([]any)
	if !ok {
		return nil, fmt.Errorf("hooks.%s is missing or not an array", event)
	}
	return entries, nil
}

func nestedHookCommands(raw any, event string) ([]string, error) {
	entries, err := hookEventEntries(raw, event)
	if err != nil {
		return nil, err
	}
	var commands []string
	for i, entry := range entries {
		group, ok := entry.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("hooks.%s[%d] is not an object", event, i)
		}
		hooks, ok := group["hooks"].([]any)
		if !ok {
			return nil, fmt.Errorf("hooks.%s[%d].hooks is not an array", event, i)
		}
		for j, hook := range hooks {
			object, ok := hook.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("hooks.%s[%d].hooks[%d] is not an object", event, i, j)
			}
			command, ok := object["command"].(string)
			if !ok {
				return nil, fmt.Errorf("hooks.%s[%d].hooks[%d].command is not a string", event, i, j)
			}
			commands = append(commands, command)
		}
	}
	return commands, nil
}

func flatHookCommands(raw any, event string) ([]string, error) {
	entries, err := hookEventEntries(raw, event)
	if err != nil {
		return nil, err
	}
	var commands []string
	for i, entry := range entries {
		object, ok := entry.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("hooks.%s[%d] is not an object", event, i)
		}
		command, ok := object["command"].(string)
		if !ok {
			return nil, fmt.Errorf("hooks.%s[%d].command is not a string", event, i)
		}
		commands = append(commands, command)
	}
	return commands, nil
}

func countCommands(commands []string, want string) int {
	seen := 0
	for _, command := range commands {
		if command == want {
			seen++
		}
	}
	return seen
}
