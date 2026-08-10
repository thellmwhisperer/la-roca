package hooks

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The seam a sandbox HOME never crosses and a real one does: the operator's
// home directory has a space in it.
//
// A hook entry is a COMMAND LINE, not an argv array. The runtime hands it to a
// shell, and a shell splits on whitespace, so an installation under
// `/Users/Ana Maria/.local/bin` declares a hook that runs `/Users/Ana` with
// `Maria/.local/bin/roca` as its argument. Nothing fails loudly: the hooks are
// declared, `roca hook status` says configured, and no context is ever injected
// into any session.

// The command a shell runs has to be the binary that was installed, whatever
// the path is spelled like.
func TestAHookCommandSurvivesAPathWithSpacesInIt(t *testing.T) {
	home := filepath.Join(t.TempDir(), "Ana Maria")
	binary := filepath.Join(home, ".local", "bin", "roca")
	if err := os.MkdirAll(filepath.Dir(binary), 0o755); err != nil {
		t.Fatal(err)
	}
	// A stand-in that prints its own arguments, so what the shell resolved is
	// read off the run and not guessed from the string.
	script := "#!/bin/sh\necho \"ran $0 with $*\"\n"
	if err := os.WriteFile(binary, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	line := Command(EventSessionStart, binary)
	output, err := exec.Command("sh", "-c", line).CombinedOutput()
	if err != nil {
		t.Fatalf("a shell cannot run the declared hook %q: %v\n%s", line, err, output)
	}
	if !strings.Contains(string(output), "ran "+binary) {
		t.Fatalf("the hook ran something else: %s\ndeclared: %s", output, line)
	}
	if !strings.Contains(string(output), "hook context") {
		t.Errorf("the hook lost its own arguments: %s", output)
	}
}

// And the withdrawal has to recognize what the installation wrote. A quoted
// path that `roca hook uninstall` no longer matches is worse than the bug it
// fixes: the entry stays in the operator's settings for ever, pointing at a
// binary the purge deleted.
func TestAQuotedCommandIsStillRecognizedAsRocasOwn(t *testing.T) {
	home := filepath.Join(t.TempDir(), "Ana Maria")
	settings := filepath.Join(home, ".claude", "settings.json")
	binary := filepath.Join(home, ".local", "bin", "roca")
	if err := os.MkdirAll(filepath.Dir(settings), 0o755); err != nil {
		t.Fatal(err)
	}

	if _, err := Install(RuntimeClaude, settings, binary); err != nil {
		t.Fatalf("Install: %v", err)
	}
	report, err := Status(RuntimeClaude, settings)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if report.State != StateConfigured {
		t.Fatalf("state = %q after installing under a path with a space", report.State)
	}

	if _, err := Uninstall(RuntimeClaude, settings); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	body, err := os.ReadFile(settings)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "hook context") {
		t.Fatalf("the withdrawal did not recognize its own entry:\n%s", body)
	}
}

// The settings file is JSON and stays JSON: whatever quoting the command needs
// travels inside a string value, so the file a runtime reads is still a file it
// can parse.
func TestTheSettingsFileIsStillJSONAfterAPathWithSpaces(t *testing.T) {
	settings := filepath.Join(t.TempDir(), "settings.json")
	if _, err := Install(RuntimeClaude, settings, "/Users/Ana Maria/.local/bin/roca"); err != nil {
		t.Fatalf("Install: %v", err)
	}
	body, err := os.ReadFile(settings)
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("the settings file no longer parses: %v\n%s", err, body)
	}
}
