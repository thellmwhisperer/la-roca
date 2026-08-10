//go:build acceptance

package acceptance

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/cucumber/godog"
)

// The steps of job J3, the session lifecycle.
//
// The law every one of them measures: a hook reaches the kernel by running a
// command. Never the database directly, never the MCP. Everything here is
// therefore checked from the outside, over the settings file the runtime really
// reads and over the standard output the runtime really parses.

// theSettingsWithAHookOfItsOwn is a settings file that already has a hook in it
// before Roca arrives, which is the whole question F11-01 and F11-08 ask.
const theSettingsWithAHookOfItsOwn = `{
  "model": "opus",
  "hooks": {
    "SessionStart": [
      {
        "hooks": [
          { "type": "command", "command": "mi-propio-script.sh" }
        ]
      }
    ]
  },
  "theme": "dark"
}
`

// theLifecycleEvents are the ones v1 declares. `Stop` is deliberately not among
// them: it fires on every turn and in v1 it would have nothing to do, because
// the incremental engine is `roca ingest`.
var theLifecycleEvents = []string{"SessionStart", "PreCompact", "SessionEnd"}

func registerHookSteps(ctx *godog.ScenarioContext, m *world) {
	ctx.Given(`^the runtime "([^"]*)" has a settings file with a hook of its own in it$`,
		m.aSettingsFileWithAHookOfItsOwn)
	ctx.Given(`^a HOME with no trace of Roca in it$`, m.aHomeWithNoRoca)
	ctx.Given(`^there is a handoff longer than the whole budget$`, m.anOversizedHandoff)

	ctx.Then(`^the settings file declares one command for each lifecycle event$`,
		m.oneCommandPerLifecycleEvent)
	ctx.Then(`^the hook that was already there is still there$`, m.theUsersOwnHookSurvives)
	ctx.Then(`^a backup of the previous settings file exists$`, m.aBackupOfTheSettingsExists)
	ctx.Then(`^no Roca hook is declared any more$`, m.noRocaHookIsDeclared)
	ctx.Then(`^everything outside the hooks member is what it was$`,
		m.everythingOutsideTheHooksMemberSurvives)
	ctx.Then(`^the settings file has not been deleted$`, m.theSettingsFileIsStillThere)
	ctx.Then(`^the injected context contains the served pill$`, m.theContextCarriesThePill)
	ctx.Then(`^the injected context contains the most recent handoff$`,
		m.theContextCarriesTheNewestHandoff)
	ctx.Then(`^the injected context ends by pointing back at La Roca$`,
		m.theContextPointsBackAtLaRoca)
	ctx.Then(`^the injected context does not exceed (\d+) characters$`, m.theContextFitsIn)
	ctx.Then(`^the budget report declares the limit that was applied$`, m.theBudgetDeclaresItsLimit)
	ctx.Then(`^the budget report names every section that did not go in whole$`,
		m.theBudgetNamesWhatWasTrimmed)
	ctx.Then(`^the pill was served whole even so$`, m.thePillWasServedWhole)
	ctx.Then(`^the JSON output declares the lifecycle event it answers$`, m.itDeclaresItsEvent)
	ctx.Then(`^the JSON output carries the injected context$`, m.itCarriesTheContext)
	ctx.Then(`^the output names the session that ended$`, m.itNamesTheSession)
	ctx.Then(`^the output names the working directory it ended in$`, m.itNamesTheDirectory)
	ctx.Then(`^every command Roca declared is a roca command line$`, m.everyDeclaredCommandIsCLI)
	ctx.Then(`^no command Roca declared names the database file$`, m.noDeclaredCommandOpensTheDB)
	ctx.Then(`^no command Roca declared speaks the MCP$`, m.noDeclaredCommandSpeaksMCP)
	ctx.Then(`^standard output carries nothing$`, m.standardOutputCarriesNothing)
	ctx.Then(`^the output names the runtime that does have an adapter$`,
		m.itNamesTheRuntimeThatExists)
	ctx.Then(`^standard output is valid JSON and nothing else$`, m.theOutputIsValidJSON)
}

// --- the worlds ---

func (m *world) aSettingsFileWithAHookOfItsOwn(runtime string) error {
	if runtime != "claude" {
		return fmt.Errorf("there is no lifecycle adapter for %q", runtime)
	}
	path := filepath.Join(m.home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(theSettingsWithAHookOfItsOwn), 0o600); err != nil {
		return err
	}
	m.settings = path
	m.settingsBefore = theSettingsWithAHookOfItsOwn
	return nil
}

// aHomeWithNoRoca is the machine of somebody who has not installed the product:
// the HOME of the scenario is already fresh, and this step is what says out
// loud that nothing is initialized in it.
func (m *world) aHomeWithNoRoca() error {
	if _, err := os.Stat(filepath.Join(m.home, ".roca")); err == nil {
		return fmt.Errorf("this HOME already has a Roca in it")
	}
	return nil
}

func (m *world) anOversizedHandoff() error {
	db, err := m.openDB()
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.Exec(
		`INSERT INTO memories (layer, content, origin, metadata, created_at)
		 VALUES ('handoff', ?, 'agent', '{}', '2026-08-05 23:59:59')`,
		"un handoff desmesurado: "+strings.Repeat("relleno ", 4000))
	return err
}

// --- the assertions ---

func (m *world) oneCommandPerLifecycleEvent() error {
	declared, err := m.declaredHooks()
	if err != nil {
		return err
	}
	for _, event := range theLifecycleEvents {
		commands := commandsOf(declared[event])
		found := 0
		for _, command := range commands {
			if strings.Contains(command, "roca hook") {
				found++
			}
		}
		if found != 1 {
			return fmt.Errorf("the event %s declares %d Roca commands, want exactly 1: %v",
				event, found, commands)
		}
	}
	return nil
}

func (m *world) theUsersOwnHookSurvives() error {
	current, err := os.ReadFile(m.settings)
	if err != nil {
		return err
	}
	if !strings.Contains(string(current), "mi-propio-script.sh") {
		return fmt.Errorf("the hook the user wrote was lost:\n%s", current)
	}
	return nil
}

func (m *world) aBackupOfTheSettingsExists() error {
	if _, err := os.Stat(m.settings + ".bak"); err != nil {
		return fmt.Errorf("there is no backup of %s: %w", m.settings, err)
	}
	return nil
}

func (m *world) noRocaHookIsDeclared() error {
	commands, err := m.rocaCommands()
	if err != nil {
		return err
	}
	if len(commands) > 0 {
		return fmt.Errorf("Roca hooks survived the uninstall: %v", commands)
	}
	return nil
}

// Everything outside the `hooks` member: the same members, with the same
// values, and the lines that carry them still spelled exactly as they were.
// Inside the member Roca re-serializes, which is the declared trade; outside it
// nothing at all may move.
func (m *world) everythingOutsideTheHooksMemberSurvives() error {
	current, err := os.ReadFile(m.settings)
	if err != nil {
		return err
	}
	before, err := withoutTheHooks(m.settingsBefore)
	if err != nil {
		return err
	}
	after, err := withoutTheHooks(string(current))
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(before, after) {
		return fmt.Errorf("the members outside the hooks changed: %v against %v",
			before, after)
	}
	for _, line := range linesOutsideTheHooks(m.settingsBefore) {
		if !strings.Contains(string(current), line) {
			return fmt.Errorf("the line %q was respelled or lost", line)
		}
	}
	return nil
}

// linesOutsideTheHooks are the lines of a settings file that do not belong to
// the `hooks` member. They are the ones that may not move by a single byte:
// inside the member Roca re-serializes, and that is the declared trade.
func linesOutsideTheHooks(text string) []string {
	lines := strings.Split(text, "\n")
	start := -1
	for i, line := range lines {
		if strings.Contains(line, `"hooks"`) {
			start = i
			break
		}
	}
	if start < 0 {
		return nonBlank(lines)
	}
	depth, end := 0, len(lines)-1
	for i := start; i < len(lines); i++ {
		depth += strings.Count(lines[i], "{") + strings.Count(lines[i], "[")
		depth -= strings.Count(lines[i], "}") + strings.Count(lines[i], "]")
		if i > start && depth <= 0 {
			end = i
			break
		}
	}
	return nonBlank(append(append([]string{}, lines[:start]...), lines[end+1:]...))
}

func nonBlank(lines []string) []string {
	var kept []string
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			kept = append(kept, line)
		}
	}
	return kept
}

func withoutTheHooks(text string) (map[string]any, error) {
	var settings map[string]any
	if err := json.Unmarshal([]byte(text), &settings); err != nil {
		return nil, fmt.Errorf("the settings file does not parse: %w", err)
	}
	delete(settings, "hooks")
	return settings, nil
}

func (m *world) theSettingsFileIsStillThere() error {
	if _, err := os.Stat(m.settings); err != nil {
		return fmt.Errorf("the settings file was deleted: %w", err)
	}
	return nil
}

func (m *world) theContextCarriesThePill() error {
	return m.injectedContains(theSeededPill)
}

func (m *world) theContextCarriesTheNewestHandoff() error {
	return m.injectedContains(theNewestHandoff)
}

func (m *world) theContextPointsBackAtLaRoca() error {
	return m.injectedContains("The rest stays in La Roca")
}

func (m *world) theContextFitsIn(limit int) error {
	context, err := m.injected()
	if err != nil {
		return err
	}
	if len(context) > limit {
		return fmt.Errorf("%d characters over a limit of %d", len(context), limit)
	}
	return nil
}

func (m *world) theBudgetDeclaresItsLimit() error {
	budget, err := m.budget()
	if err != nil {
		return err
	}
	if fmt.Sprint(budget["limit"]) != "900" {
		return fmt.Errorf("limit = %v, want the 900 that was asked for", budget["limit"])
	}
	return nil
}

// What was cut is declared cut. A block that silently loses half a handoff is
// worse than one that says it did.
func (m *world) theBudgetNamesWhatWasTrimmed() error {
	sections, err := m.sections()
	if err != nil {
		return err
	}
	context, err := m.injected()
	if err != nil {
		return err
	}
	for name, state := range sections {
		if state == "full" {
			continue
		}
		if !strings.Contains(context, name) {
			return fmt.Errorf("the section %q came out %s and the block does not say so",
				name, state)
		}
	}
	return nil
}

func (m *world) thePillWasServedWhole() error {
	sections, err := m.sections()
	if err != nil {
		return err
	}
	if state := sections["pills"]; state != "full" {
		return fmt.Errorf("pills = %q: the long handoff starved the pill", state)
	}
	return m.injectedContains(theSeededPill)
}

func (m *world) itDeclaresItsEvent() error {
	specific, err := m.hookSpecificOutput()
	if err != nil {
		return err
	}
	if specific["hookEventName"] != "SessionStart" {
		return fmt.Errorf("hookEventName = %v, want SessionStart", specific["hookEventName"])
	}
	return nil
}

func (m *world) itCarriesTheContext() error {
	specific, err := m.hookSpecificOutput()
	if err != nil {
		return err
	}
	context := fmt.Sprint(specific["additionalContext"])
	if !strings.Contains(context, theSeededPill) {
		return fmt.Errorf("the injected context does not carry the pill: %s", context)
	}
	return nil
}

func (m *world) itNamesTheSession() error   { return m.outputContains("abc-123") }
func (m *world) itNamesTheDirectory() error { return m.outputContains("/w") }

func (m *world) everyDeclaredCommandIsCLI() error {
	commands, err := m.rocaCommands()
	if err != nil {
		return err
	}
	if len(commands) == 0 {
		return fmt.Errorf("Roca declared no command")
	}
	expected := m.binaryPath() + " "
	for _, command := range commands {
		if !strings.HasPrefix(command, expected) {
			return fmt.Errorf("the declared command %q does not launch %s", command, m.binaryPath())
		}
	}
	return nil
}

func (m *world) noDeclaredCommandOpensTheDB() error {
	commands, err := m.rocaCommands()
	if err != nil {
		return err
	}
	for _, command := range commands {
		for _, forbidden := range []string{".db", "sqlite", "roca.db"} {
			if strings.Contains(command, forbidden) {
				return fmt.Errorf("the declared command %q reaches the database directly",
					command)
			}
		}
	}
	return nil
}

func (m *world) noDeclaredCommandSpeaksMCP() error {
	commands, err := m.rocaCommands()
	if err != nil {
		return err
	}
	for _, command := range commands {
		for _, forbidden := range []string{"serve", "mcp", "http"} {
			if strings.Contains(command, forbidden) {
				return fmt.Errorf("the declared command %q reaches the kernel over the MCP",
					command)
			}
		}
	}
	return nil
}

// A hook on a machine with no Roca stays silent instead of printing noise into
// every session.
func (m *world) standardOutputCarriesNothing() error {
	if strings.TrimSpace(m.last.stdout) != "" {
		return fmt.Errorf("standard output carries %q", m.last.stdout)
	}
	return nil
}

func (m *world) itNamesTheRuntimeThatExists() error {
	return m.outputContains("claude")
}

// --- reading the answer ---

func (m *world) injected() (string, error) {
	document, err := m.json()
	if err != nil {
		return "", err
	}
	if context, ok := document["context"]; ok {
		return fmt.Sprint(context), nil
	}
	specific, err := m.hookSpecificOutput()
	if err != nil {
		return "", err
	}
	return fmt.Sprint(specific["additionalContext"]), nil
}

func (m *world) injectedContains(text string) error {
	context, err := m.injected()
	if err != nil {
		return err
	}
	if !strings.Contains(context, text) {
		return fmt.Errorf("the injected context does not contain %q:\n%s", text, context)
	}
	return nil
}

func (m *world) budget() (map[string]any, error) {
	document, err := m.json()
	if err != nil {
		return nil, err
	}
	budget, ok := document["budget"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("the answer carries no budget report: %v", keys(document))
	}
	return budget, nil
}

func (m *world) sections() (map[string]string, error) {
	budget, err := m.budget()
	if err != nil {
		return nil, err
	}
	declared, _ := budget["sections"].([]any)
	states := map[string]string{}
	for _, item := range declared {
		section, ok := item.(map[string]any)
		if !ok {
			continue
		}
		states[fmt.Sprint(section["name"])] = fmt.Sprint(section["state"])
	}
	return states, nil
}

func (m *world) hookSpecificOutput() (map[string]any, error) {
	document, err := m.json()
	if err != nil {
		return nil, err
	}
	specific, ok := document["hookSpecificOutput"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("the answer does not speak the runtime's protocol: %v",
			keys(document))
	}
	return specific, nil
}

func (m *world) declaredHooks() (map[string]any, error) {
	content, err := os.ReadFile(m.settings)
	if err != nil {
		return nil, err
	}
	var settings struct {
		Hooks map[string]any `json:"hooks"`
	}
	if err := json.Unmarshal(content, &settings); err != nil {
		return nil, fmt.Errorf("the settings file no longer parses: %w", err)
	}
	return settings.Hooks, nil
}

// rocaCommands are every command line Roca declared in the settings file.
func (m *world) rocaCommands() ([]string, error) {
	declared, err := m.declaredHooks()
	if err != nil {
		return nil, err
	}
	var commands []string
	for _, groups := range declared {
		for _, command := range commandsOf(groups) {
			if strings.Contains(command, "roca ") {
				commands = append(commands, command)
			}
		}
	}
	return commands, nil
}

func commandsOf(groups any) []string {
	list, ok := groups.([]any)
	if !ok {
		return nil
	}
	var commands []string
	for _, item := range list {
		group, ok := item.(map[string]any)
		if !ok {
			continue
		}
		entries, ok := group["hooks"].([]any)
		if !ok {
			continue
		}
		for _, entry := range entries {
			declared, ok := entry.(map[string]any)
			if !ok {
				continue
			}
			if command, ok := declared["command"].(string); ok {
				commands = append(commands, command)
			}
		}
	}
	return commands
}
