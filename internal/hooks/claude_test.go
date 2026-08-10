package hooks_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/hooks"
)

// The settings file belongs to the user. Roca owns exactly the entries whose
// command runs `roca hook`, so a hook somebody wrote themselves survives both an
// install and an uninstall, and every byte outside the `hooks` member is left
// where it was found.
const settingsWithAHookOfItsOwn = `{
  "model": "opus",
  "hooks": {
    "SessionStart": [
      {
        "hooks": [
          { "type": "command", "command": "my-own-script.sh" }
        ]
      }
    ]
  },
  "theme": "dark"
}
`

func TestInstallingDeclaresOneCommandPerLifecycleEvent(t *testing.T) {
	path := settingsFile(t, settingsWithAHookOfItsOwn)

	outcome, err := hooks.Install(hooks.RuntimeClaude, path, "roca")
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if !outcome.Changed {
		t.Fatal("installing changed nothing")
	}

	declared := hooksMember(t, path)
	for _, event := range hooks.SupportedEvents {
		if _, ok := declared[event]; !ok {
			t.Errorf("the event %q was not declared", event)
		}
	}
	// The transport is the CLI and nothing else: no database, no MCP.
	rendered := read(t, path)
	if !strings.Contains(rendered, "roca hook context") {
		t.Error("the session start does not ask for the context over the CLI")
	}
	if !strings.Contains(rendered, "roca hook record") {
		t.Error("no event records a handoff")
	}
}

func TestAHookTheUserWroteThemselvesSurvivesBothWays(t *testing.T) {
	path := settingsFile(t, settingsWithAHookOfItsOwn)

	if _, err := hooks.Install(hooks.RuntimeClaude, path, "roca"); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if !strings.Contains(read(t, path), "my-own-script.sh") {
		t.Fatal("installing ate a hook the user wrote")
	}
	if _, err := hooks.Uninstall(hooks.RuntimeClaude, path); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if !strings.Contains(read(t, path), "my-own-script.sh") {
		t.Error("uninstalling ate a hook the user wrote")
	}
}

func TestEverythingOutsideTheHooksMemberComesBackUntouched(t *testing.T) {
	path := settingsFile(t, settingsWithAHookOfItsOwn)

	if _, err := hooks.Install(hooks.RuntimeClaude, path, "roca"); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if _, err := hooks.Uninstall(hooks.RuntimeClaude, path); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}

	var settings map[string]any
	if err := json.Unmarshal([]byte(read(t, path)), &settings); err != nil {
		t.Fatalf("the settings file no longer parses: %v", err)
	}
	if settings["model"] != "opus" || settings["theme"] != "dark" {
		t.Errorf("the neighbours changed: %v", settings)
	}
	if strings.Contains(read(t, path), "roca hook") {
		t.Error("a Roca hook survived the uninstall")
	}
}

func TestInstallingTwiceLeavesOneDeclarationPerEvent(t *testing.T) {
	path := settingsFile(t, settingsWithAHookOfItsOwn)

	if _, err := hooks.Install(hooks.RuntimeClaude, path, "roca"); err != nil {
		t.Fatalf("first Install: %v", err)
	}
	second, err := hooks.Install(hooks.RuntimeClaude, path, "roca")
	if err != nil {
		t.Fatalf("second Install: %v", err)
	}
	if second.Changed {
		t.Error("the second installation changed the file")
	}
	if got := strings.Count(read(t, path), "roca hook context"); got != 1 {
		t.Errorf("%d declarations of the same command, want 1", got)
	}
}

// An entry Roca owns is recognized whatever path, wrapper or extension the
// binary was installed under, so an install with an absolute path is still
// withdrawn by an uninstall that does not know about it.
func TestAnEntryIsRecognizedWhateverPathTheBinaryWasInstalledUnder(t *testing.T) {
	path := settingsFile(t, settingsWithAHookOfItsOwn)

	if _, err := hooks.Install(hooks.RuntimeClaude, path, "/opt/roca/bin/roca"); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if _, err := hooks.Uninstall(hooks.RuntimeClaude, path); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if strings.Contains(read(t, path), "roca hook") {
		t.Error("an entry installed under an absolute path survived the uninstall")
	}
}

func TestStatusReportsWhatIsDeclaredWithoutTouchingTheFile(t *testing.T) {
	path := settingsFile(t, settingsWithAHookOfItsOwn)

	before, err := hooks.Status(hooks.RuntimeClaude, path)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if before.State != hooks.StateNotConfigured {
		t.Errorf("state = %q, want %q", before.State, hooks.StateNotConfigured)
	}

	if _, err := hooks.Install(hooks.RuntimeClaude, path, "roca"); err != nil {
		t.Fatalf("Install: %v", err)
	}
	after, err := hooks.Status(hooks.RuntimeClaude, path)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if after.State != hooks.StateConfigured {
		t.Errorf("state = %q, want %q", after.State, hooks.StateConfigured)
	}
	if len(after.Events) != len(hooks.SupportedEvents) {
		t.Errorf("events = %v, want the %d declared ones",
			after.Events, len(hooks.SupportedEvents))
	}
}

// A settings file Roca cannot parse is a settings file Roca must not edit.
func TestABrokenSettingsFileIsNotEdited(t *testing.T) {
	path := settingsFile(t, "{ this is not JSON")

	if _, err := hooks.Install(hooks.RuntimeClaude, path, "roca"); err == nil {
		t.Fatal("a settings file that does not parse was edited anyway")
	}
	if read(t, path) != "{ this is not JSON" {
		t.Error("a settings file that does not parse was written to")
	}
}

func TestOnlyTheChosenRuntimeIsSupported(t *testing.T) {
	if got := hooks.Runtimes(); len(got) != 1 || got[0] != hooks.RuntimeClaude {
		t.Errorf("runtimes = %v, want only %q: the rest is not v1 scope",
			got, hooks.RuntimeClaude)
	}
	_, err := hooks.Install("codex", filepath.Join(t.TempDir(), "settings.json"), "roca")
	if err == nil {
		t.Fatal("a runtime with no adapter was accepted")
	}
	if !strings.Contains(err.Error(), hooks.RuntimeClaude) {
		t.Errorf("the error %q does not name the runtime that does exist", err)
	}
}

// --- the harness ---

func settingsFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write the fixture: %v", err)
	}
	return path
}

func read(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(content)
}

func hooksMember(t *testing.T, path string) map[string]any {
	t.Helper()
	var settings struct {
		Hooks map[string]any `json:"hooks"`
	}
	if err := json.Unmarshal([]byte(read(t, path)), &settings); err != nil {
		t.Fatalf("the settings file does not parse: %v", err)
	}
	return settings.Hooks
}
