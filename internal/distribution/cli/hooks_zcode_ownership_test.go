package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Each case drives `roca hooks install|uninstall zcode` against a throwaway
// home: La Roca owns exactly the SessionStart entry and hooks.enabled value it
// wrote, and the containers it created around them.

func zcodeHookHome(t *testing.T, config string) (string, string, string) {
	t.Helper()
	home, path := zcodeHookTestPaths(t)
	writeZcodeHookExecutable(t, home, "#!/bin/sh\nexit 0\n")
	if config != "" {
		writeFile(t, path, config)
	}
	return home, path, filepath.Join(home, ".zcode", "hooks", "roca-handoff.sh")
}

func TestZcodeHookInstallNeverChangesOperatorEnabled(t *testing.T) {
	_, config, _ := zcodeHookHome(t, `{"hooks":{"enabled":false}}`)
	requireZcodeHooks(t, "install")
	if enabled := readZcodeHookDocument(t, config)["hooks"].(map[string]any)["enabled"]; enabled != false {
		t.Fatalf("install changed the operator's hooks.enabled to %#v", enabled)
	}
	requireZcodeHooks(t, "uninstall")
	requireJSONEqual(t, config, `{"hooks":{"enabled":false}}`)
}

func TestZcodeHookUninstallRemovesEnabledOnlyWhileItHoldsRocaValue(t *testing.T) {
	_, config, _ := zcodeHookHome(t, `{"hooks":{"events":{}}}`)
	requireZcodeHooks(t, "install")
	requireZcodeHooks(t, "uninstall")
	requireJSONEqual(t, config, `{"hooks":{"events":{}}}`)

	requireZcodeHooks(t, "install")
	document := readZcodeHookDocument(t, config)
	document["hooks"].(map[string]any)["enabled"] = false
	writeZcodeHookDocument(t, config, document)
	requireZcodeHooks(t, "uninstall")
	requireJSONEqual(t, config, `{"hooks":{"enabled":false,"events":{}}}`)
}

func TestZcodeHookInstallRefusesUnmarkedWrapperBeforeWriting(t *testing.T) {
	_, config, wrapper := zcodeHookHome(t, "")
	operator := "#!/bin/sh\nprintf '{}\\n'\n"
	writeFile(t, wrapper, operator)

	if err := executeZcodeHooks("install"); err == nil {
		t.Fatal("install overwrote an unmarked wrapper")
	}
	if _, err := os.Stat(config); !os.IsNotExist(err) {
		t.Fatalf("refused install created the config: %v", err)
	}
	_ = executeZcodeHooks("uninstall")
	if got := string(mustRead(t, wrapper)); got != operator {
		t.Fatalf("operator wrapper changed: %q", got)
	}
}

func TestZcodeHookEditedMarkedWrapperSurvivesInstallAndUninstall(t *testing.T) {
	_, config, wrapper := zcodeHookHome(t, "{}\n")
	requireZcodeHooks(t, "install")
	operator := append(mustRead(t, wrapper), []byte("# operator change\n")...)
	writeFile(t, wrapper, string(operator))
	before := string(mustRead(t, config))

	if err := executeZcodeHooks("install"); err == nil {
		t.Fatal("install overwrote an edited marked wrapper")
	}
	if got := string(mustRead(t, wrapper)); got != string(operator) {
		t.Fatalf("install changed the operator wrapper: %q", got)
	}
	if got := string(mustRead(t, config)); got != before {
		t.Fatalf("refused install changed config: %s", got)
	}
	requireZcodeHooks(t, "uninstall")
	if got := string(mustRead(t, wrapper)); got != string(operator) {
		t.Fatalf("uninstall changed the operator wrapper: %q", got)
	}
}

func TestZcodeHookUninstallKeepsWrapperReferencedByEditedEntry(t *testing.T) {
	_, config, wrapper := zcodeHookHome(t, "{}\n")
	requireZcodeHooks(t, "install")
	entry := editZcodeHookTimeout(t, config)

	requireZcodeHooks(t, "uninstall")
	if got := string(mustRead(t, config)); got != entry {
		t.Fatalf("uninstall changed the operator-edited entry: %s", got)
	}
	if _, err := os.Stat(wrapper); err != nil {
		t.Fatalf("uninstall removed the referenced wrapper: %v", err)
	}
}

func TestZcodeHookUninstallKeepsWrapperReferencedByOtherEvent(t *testing.T) {
	_, config, wrapper := zcodeHookHome(t, "{}\n")
	requireZcodeHooks(t, "install")
	document := readZcodeHookDocument(t, config)
	events := document["hooks"].(map[string]any)["events"].(map[string]any)
	events["Stop"] = []any{map[string]any{"hooks": []any{
		map[string]any{"type": "command", "command": wrapper},
	}}}
	writeZcodeHookDocument(t, config, document)

	requireZcodeHooks(t, "uninstall")
	if _, err := os.Stat(wrapper); err != nil {
		t.Fatalf("uninstall removed the wrapper referenced by another event: %v", err)
	}
}

func TestZcodeHookInstallUninstallRestoresConfigWithoutHooks(t *testing.T) {
	before := "{\n  \"theme\": \"dark\"\n}\n"
	_, config, _ := zcodeHookHome(t, before)
	requireZcodeHooks(t, "install")
	requireZcodeHooks(t, "uninstall")
	if got := string(mustRead(t, config)); got != before {
		t.Fatalf("config = %q, want %q", got, before)
	}
}

func TestZcodeHookEditedTimeoutSurvivesReinstallAndUninstall(t *testing.T) {
	_, config, _ := zcodeHookHome(t, "{}\n")
	requireZcodeHooks(t, "install")
	edited := editZcodeHookTimeout(t, config)

	for _, action := range []string{"install", "uninstall"} {
		requireZcodeHooks(t, action)
		if got := string(mustRead(t, config)); got != edited {
			t.Fatalf("%s touched the operator-edited hook: %s", action, got)
		}
	}
}

func editZcodeHookTimeout(t *testing.T, config string) string {
	t.Helper()
	document := readZcodeHookDocument(t, config)
	groups := document["hooks"].(map[string]any)["events"].(map[string]any)["SessionStart"].([]any)
	groups[0].(map[string]any)["hooks"].([]any)[0].(map[string]any)["timeoutMs"] = 9000
	writeZcodeHookDocument(t, config, document)
	return string(mustRead(t, config))
}

func TestZcodeHookUninstallKeepsOperatorEmptyGroup(t *testing.T) {
	before := `{"hooks":{"events":{"SessionStart":[{"hooks":[]}]}}}`
	_, config, _ := zcodeHookHome(t, before)
	requireZcodeHooks(t, "install")
	requireZcodeHooks(t, "uninstall")
	requireJSONEqual(t, config, before)
}

func TestZcodeHookLegacyClaimOnExactHookIsRocas(t *testing.T) {
	home, config, wrapper := zcodeHookHome(t, "")
	executable := filepath.Join(home, "bin", "roca")
	writeFile(t, config, `{"theme":"dark","hooks":{"enabled":true,"events":{"SessionStart":[{"hooks":[`+
		`{"type":"command","command":"`+wrapper+`","timeoutMs":15000}]}]}}}`)
	writeFile(t, config+".roca-owned",
		`{"roca":"owned-containers-v1","hooks":["hooks","hooks.events","hooks.events.SessionStart"]}`+"\n")
	writeFile(t, wrapper, zcodeWrapper(executable, sessionRequest{}))

	requireZcodeHooks(t, "install")
	if got := strings.Count(string(mustRead(t, config)), wrapper); got != 1 {
		t.Fatalf("La Roca hooks after install = %d, want 1", got)
	}
	requireZcodeHooks(t, "uninstall")
	requireJSONEqual(t, config, `{"theme":"dark"}`)
	if _, err := os.Stat(wrapper); !os.IsNotExist(err) {
		t.Fatalf("wrapper survived uninstall: %v", err)
	}
}

func writeZcodeHookDocument(t *testing.T, path string, document map[string]any) {
	t.Helper()
	body, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, string(body)+"\n")
}
