package cli

import (
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestZcodeHookJSONWrapsAdditionalContext(t *testing.T) {
	if got := string(zcodeHookJSON("")); got != "{}\n" {
		t.Fatalf("empty context = %q", got)
	}
	var payload map[string]string
	if err := json.Unmarshal(zcodeHookJSON("latest handoff"), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["additionalContext"] != "latest handoff" {
		t.Fatalf("payload = %#v", payload)
	}
}

func TestZcodeHookInstallerWritesNestedSessionStartAndJSONWrapper(t *testing.T) {
	home := skillTestHome(t)
	binary := filepath.Join(home, "bin", "roca")
	if err := os.MkdirAll(filepath.Dir(binary), 0o700); err != nil {
		t.Fatal(err)
	}
	fake := `#!/bin/sh
if [ "$1 $2 $3" = "hooks run zcode" ]; then
  printf '%s\n' '{"additionalContext":"synthetic handoff"}'
  exit 0
fi
exit 1
`
	if err := os.WriteFile(binary, []byte(fake), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvExecutable, binary)
	config := filepath.Join(home, ".zcode", "cli", "config.json")
	if err := os.MkdirAll(filepath.Dir(config), 0o700); err != nil {
		t.Fatal(err)
	}
	initial := `{"theme":"dark","hooks":{"enabled":false,"events":{"SessionStart":[{"hooks":[{"type":"command","command":"operator-hook","timeoutMs":5000}]}]}}}`
	if err := os.WriteFile(config, []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}

	for attempt := 0; attempt < 2; attempt++ {
		root := rootCommand(&cliEnv{out: io.Discard, build: Build{Version: "test"}})
		root.SetArgs([]string{"hooks", "install", "zcode"})
		if err := root.Execute(); err != nil {
			t.Fatalf("install attempt %d: %v", attempt+1, err)
		}
	}

	body, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(body, &document); err != nil {
		t.Fatal(err)
	}
	if document["theme"] != "dark" {
		t.Fatalf("installer lost neighbouring theme: %s", body)
	}
	hooks := document["hooks"].(map[string]any)
	if hooks["enabled"] != true {
		t.Fatalf("hooks.enabled = %#v", hooks["enabled"])
	}
	if _, flat := hooks["SessionStart"]; flat {
		t.Fatal("installer wrote the rejected flat SessionStart shape")
	}
	events := hooks["events"].(map[string]any)
	entries := events["SessionStart"].([]any)
	if len(entries) != 2 {
		t.Fatalf("SessionStart entries = %d, want operator hook plus one Roca hook", len(entries))
	}
	wrapper := filepath.Join(home, ".zcode", "hooks", "roca-handoff.sh")
	info, err := os.Stat(wrapper)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o100 == 0 {
		t.Fatal("wrapper is not executable")
	}
	script, err := os.ReadFile(wrapper)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(script), zcodeHookWrapperMarker) {
		t.Fatalf("wrapper missing La Roca marker: %s", script)
	}
	output, err := exec.Command(wrapper).Output()
	if err != nil {
		t.Fatalf("run wrapper: %v\n%s", err, output)
	}
	var context map[string]string
	if err := json.Unmarshal(output, &context); err != nil {
		t.Fatalf("wrapper stdout is not JSON: %v\n%s", err, output)
	}
	if context["additionalContext"] != "synthetic handoff" {
		t.Fatalf("wrapper context = %#v", context)
	}
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	output, err = exec.Command(wrapper).Output()
	if err != nil {
		t.Fatalf("wrapper should degrade to empty JSON when La Roca is unavailable: %v", err)
	}
	context = nil
	if err := json.Unmarshal(output, &context); err != nil {
		t.Fatalf("degraded wrapper stdout is not JSON: %v\n%s", err, output)
	}

	root := rootCommand(&cliEnv{out: io.Discard, build: Build{Version: "test"}})
	root.SetArgs([]string{"hooks", "uninstall", "zcode"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(wrapper); !os.IsNotExist(err) {
		t.Fatal("zcode hook wrapper survived uninstall")
	}
	body, err = os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "operator-hook") || strings.Contains(string(body), "roca-handoff.sh") {
		t.Fatalf("uninstall did not preserve only the operator hook: %s", body)
	}
	if !strings.Contains(string(body), `"theme"`) {
		t.Fatalf("uninstall lost neighbouring theme: %s", body)
	}
}

func TestZcodeHookUninstallDoesNotRemoveOperatorOwnedEmptyHooks(t *testing.T) {
	home := skillTestHome(t)
	binary := filepath.Join(home, "bin", "roca")
	if err := os.MkdirAll(filepath.Dir(binary), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvExecutable, binary)
	config := filepath.Join(home, ".zcode", "cli", "config.json")
	if err := os.MkdirAll(filepath.Dir(config), 0o700); err != nil {
		t.Fatal(err)
	}
	before := "{\n  \"hooks\": {\n    \"enabled\": false\n  }\n}\n"
	if err := os.WriteFile(config, []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}
	root := rootCommand(&cliEnv{out: io.Discard, build: Build{Version: "test"}})
	root.SetArgs([]string{"hooks", "install", "zcode"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	root = rootCommand(&cliEnv{out: io.Discard, build: Build{Version: "test"}})
	root.SetArgs([]string{"hooks", "uninstall", "zcode"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	got := readSettings(t, config)
	if !strings.Contains(got, `"hooks"`) || strings.Contains(got, "roca-handoff.sh") {
		t.Fatalf("operator-owned hooks object was removed: %s", got)
	}
}

func TestZcodeHookUninstallRejectsUnreadableOwnershipBeforeMutation(t *testing.T) {
	home := skillTestHome(t)
	binary := filepath.Join(home, "bin", "roca")
	if err := os.MkdirAll(filepath.Dir(binary), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvExecutable, binary)
	config := filepath.Join(home, ".zcode", "cli", "config.json")
	if err := os.MkdirAll(filepath.Dir(config), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	root := rootCommand(&cliEnv{out: io.Discard, build: Build{Version: "test"}})
	root.SetArgs([]string{"hooks", "install", "zcode"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	configBefore, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	wrapper := filepath.Join(home, ".zcode", "hooks", "roca-handoff.sh")
	wrapperBefore, err := os.ReadFile(wrapper)
	if err != nil {
		t.Fatal(err)
	}
	owned := config + ".roca-owned"
	if err := os.WriteFile(owned, []byte("{not-json\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	root = rootCommand(&cliEnv{out: io.Discard, build: Build{Version: "test"}})
	root.SetArgs([]string{"hooks", "uninstall", "zcode"})
	if err := root.Execute(); err == nil {
		t.Fatal("uninstall accepted an unrecognized ownership sidecar")
	}
	configAfter, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	if string(configAfter) != string(configBefore) {
		t.Fatalf("uninstall edited config before rejecting ownership:\n--- before ---\n%s\n--- after ---\n%s", configBefore, configAfter)
	}
	wrapperAfter, err := os.ReadFile(wrapper)
	if err != nil {
		t.Fatal(err)
	}
	if string(wrapperAfter) != string(wrapperBefore) {
		t.Fatal("uninstall edited wrapper before rejecting ownership")
	}
	ownedAfter, err := os.ReadFile(owned)
	if err != nil {
		t.Fatal(err)
	}
	if string(ownedAfter) != "{not-json\n" {
		t.Fatalf("uninstall edited unrecognized ownership: %s", ownedAfter)
	}
}

func TestZcodeHookInstallRejectsUnreadableOwnershipBeforeMutation(t *testing.T) {
	home := skillTestHome(t)
	config := filepath.Join(home, ".zcode", "cli", "config.json")
	if err := os.MkdirAll(filepath.Dir(config), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	owned := config + ".roca-owned"
	if err := os.WriteFile(owned, []byte("{not-json\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	root := rootCommand(&cliEnv{out: io.Discard, build: Build{Version: "test"}})
	root.SetArgs([]string{"hooks", "install", "zcode"})
	if err := root.Execute(); err == nil {
		t.Fatal("install accepted an unrecognized ownership sidecar")
	}
	body, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "{}\n" {
		t.Fatalf("install edited config before rejecting ownership: %s", body)
	}
	ownedAfter, err := os.ReadFile(owned)
	if err != nil {
		t.Fatal(err)
	}
	if string(ownedAfter) != "{not-json\n" {
		t.Fatalf("install edited unrecognized ownership: %s", ownedAfter)
	}
	wrapper := filepath.Join(home, ".zcode", "hooks", "roca-handoff.sh")
	if _, err := os.Stat(wrapper); !os.IsNotExist(err) {
		t.Fatalf("install wrote wrapper before rejecting ownership: %v", err)
	}
}

func TestZcodeHookReinstallPreservesContainerOwnership(t *testing.T) {
	home := skillTestHome(t)
	config := filepath.Join(home, ".zcode", "cli", "config.json")
	if err := os.MkdirAll(filepath.Dir(config), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	root := rootCommand(&cliEnv{out: io.Discard, build: Build{Version: "test"}})
	root.SetArgs([]string{"hooks", "install", "zcode"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config, []byte("{\"hooks\":{\"enabled\":true}}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	root = rootCommand(&cliEnv{out: io.Discard, build: Build{Version: "test"}})
	root.SetArgs([]string{"hooks", "install", "zcode"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	root = rootCommand(&cliEnv{out: io.Discard, build: Build{Version: "test"}})
	root.SetArgs([]string{"hooks", "uninstall", "zcode"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	body, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(body, &document); err != nil {
		t.Fatal(err)
	}
	if _, ok := document["hooks"]; ok {
		t.Fatalf("reinstall lost ownership of the product-created hooks container: %#v", document)
	}
}

func TestZcodeHookUninstallPreservesGroupMetadata(t *testing.T) {
	home := skillTestHome(t)
	config := filepath.Join(home, ".zcode", "cli", "config.json")
	if err := os.MkdirAll(filepath.Dir(config), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	root := rootCommand(&cliEnv{out: io.Discard, build: Build{Version: "test"}})
	root.SetArgs([]string{"hooks", "install", "zcode"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(body, &document); err != nil {
		t.Fatal(err)
	}
	hooks := document["hooks"].(map[string]any)
	events := hooks["events"].(map[string]any)
	groups := events["SessionStart"].([]any)
	group := groups[0].(map[string]any)
	group["matcher"] = "operator-owned"
	body, err = json.MarshalIndent(document, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config, append(body, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}

	root = rootCommand(&cliEnv{out: io.Discard, build: Build{Version: "test"}})
	root.SetArgs([]string{"hooks", "uninstall", "zcode"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	body, err = os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	document = nil
	if err := json.Unmarshal(body, &document); err != nil {
		t.Fatal(err)
	}
	hooks = document["hooks"].(map[string]any)
	events = hooks["events"].(map[string]any)
	groups = events["SessionStart"].([]any)
	if len(groups) != 1 {
		t.Fatalf("SessionStart groups = %d, want metadata-bearing group", len(groups))
	}
	group = groups[0].(map[string]any)
	if group["matcher"] != "operator-owned" {
		t.Fatalf("uninstall lost group metadata: %#v", group)
	}
	groupHooks, ok := group["hooks"].([]any)
	if !ok || len(groupHooks) != 0 {
		t.Fatalf("uninstall did not remove only the Roca command: %#v", group)
	}
}
