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

func zcodeHookTestPaths(t *testing.T) (string, string) {
	t.Helper()
	home := skillTestHome(t)
	return home, filepath.Join(home, ".zcode", "cli", "config.json")
}

func writeZcodeHookExecutable(t *testing.T, home, body string) {
	t.Helper()
	binary := filepath.Join(home, "bin", "roca")
	writeFile(t, binary, body)
	if err := os.Chmod(binary, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvExecutable, binary)
}

func executeZcodeHooks(action string) error {
	root := rootCommand(&cliEnv{out: io.Discard, build: Build{Version: "test"}})
	root.SetArgs([]string{"hooks", action, "zcode"})
	return root.Execute()
}

func requireZcodeHooks(t *testing.T, action string) {
	t.Helper()
	if err := executeZcodeHooks(action); err != nil {
		t.Fatalf("hooks %s zcode: %v", action, err)
	}
}

func readZcodeHookDocument(t *testing.T, path string) map[string]any {
	t.Helper()
	var document map[string]any
	if err := json.Unmarshal(mustRead(t, path), &document); err != nil {
		t.Fatal(err)
	}
	return document
}

func TestZcodeHookInstallerWritesNestedSessionStartAndJSONWrapper(t *testing.T) {
	home, config := zcodeHookTestPaths(t)
	fake := `#!/bin/sh
if [ "$1 $2 $3" = "hooks run zcode" ]; then
  printf '%s\n' '{"additionalContext":"synthetic handoff"}'
  exit 0
fi
exit 1
`
	writeZcodeHookExecutable(t, home, fake)
	initial := `{"theme":"dark","hooks":{"enabled":false,"events":{"SessionStart":[{"hooks":[{"type":"command","command":"operator-hook","timeoutMs":5000}]}]}}}`
	writeFile(t, config, initial)

	for attempt := 0; attempt < 2; attempt++ {
		if err := executeZcodeHooks("install"); err != nil {
			t.Fatalf("install attempt %d: %v", attempt+1, err)
		}
	}

	body := mustRead(t, config)
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
	script := mustRead(t, wrapper)
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
	writeZcodeHookExecutable(t, home, "#!/bin/sh\nexit 1\n")
	output, err = exec.Command(wrapper).Output()
	if err != nil {
		t.Fatalf("wrapper should degrade to empty JSON when La Roca is unavailable: %v", err)
	}
	context = nil
	if err := json.Unmarshal(output, &context); err != nil {
		t.Fatalf("degraded wrapper stdout is not JSON: %v\n%s", err, output)
	}

	requireZcodeHooks(t, "uninstall")
	if _, err := os.Stat(wrapper); !os.IsNotExist(err) {
		t.Fatal("zcode hook wrapper survived uninstall")
	}
	body = mustRead(t, config)
	if !strings.Contains(string(body), "operator-hook") || strings.Contains(string(body), "roca-handoff.sh") {
		t.Fatalf("uninstall did not preserve only the operator hook: %s", body)
	}
	if !strings.Contains(string(body), `"theme"`) {
		t.Fatalf("uninstall lost neighbouring theme: %s", body)
	}
}

func TestZcodeHookUninstallDoesNotRemoveOperatorOwnedEmptyHooks(t *testing.T) {
	home, config := zcodeHookTestPaths(t)
	writeZcodeHookExecutable(t, home, "#!/bin/sh\nexit 0\n")
	before := "{\n  \"hooks\": {\n    \"enabled\": false\n  }\n}\n"
	writeFile(t, config, before)
	requireZcodeHooks(t, "install")
	requireZcodeHooks(t, "uninstall")
	got := readSettings(t, config)
	if !strings.Contains(got, `"hooks"`) || strings.Contains(got, "roca-handoff.sh") {
		t.Fatalf("operator-owned hooks object was removed: %s", got)
	}
}

func TestZcodeHookUninstallRejectsUnreadableOwnershipBeforeMutation(t *testing.T) {
	home, config := zcodeHookTestPaths(t)
	writeZcodeHookExecutable(t, home, "#!/bin/sh\nexit 0\n")
	writeFile(t, config, "{}\n")
	requireZcodeHooks(t, "install")
	configBefore := mustRead(t, config)
	wrapper := filepath.Join(home, ".zcode", "hooks", "roca-handoff.sh")
	wrapperBefore := mustRead(t, wrapper)
	owned := config + ".roca-owned"
	writeFile(t, owned, "{not-json\n")

	if err := executeZcodeHooks("uninstall"); err == nil {
		t.Fatal("uninstall accepted an unrecognized ownership sidecar")
	}
	configAfter := mustRead(t, config)
	if string(configAfter) != string(configBefore) {
		t.Fatalf("uninstall edited config before rejecting ownership:\n--- before ---\n%s\n--- after ---\n%s", configBefore, configAfter)
	}
	wrapperAfter := mustRead(t, wrapper)
	if string(wrapperAfter) != string(wrapperBefore) {
		t.Fatal("uninstall edited wrapper before rejecting ownership")
	}
	ownedAfter := mustRead(t, owned)
	if string(ownedAfter) != "{not-json\n" {
		t.Fatalf("uninstall edited unrecognized ownership: %s", ownedAfter)
	}
}

func TestZcodeHookInstallRejectsUnreadableOwnershipBeforeMutation(t *testing.T) {
	home, config := zcodeHookTestPaths(t)
	writeFile(t, config, "{}\n")
	owned := config + ".roca-owned"
	writeFile(t, owned, "{not-json\n")

	if err := executeZcodeHooks("install"); err == nil {
		t.Fatal("install accepted an unrecognized ownership sidecar")
	}
	body := mustRead(t, config)
	if string(body) != "{}\n" {
		t.Fatalf("install edited config before rejecting ownership: %s", body)
	}
	ownedAfter := mustRead(t, owned)
	if string(ownedAfter) != "{not-json\n" {
		t.Fatalf("install edited unrecognized ownership: %s", ownedAfter)
	}
	wrapper := filepath.Join(home, ".zcode", "hooks", "roca-handoff.sh")
	if _, err := os.Stat(wrapper); !os.IsNotExist(err) {
		t.Fatalf("install wrote wrapper before rejecting ownership: %v", err)
	}
}

func TestZcodeHookInstallRejectsInvalidContainersBeforeWritingWrapper(t *testing.T) {
	for name, initial := range map[string]string{
		"hooks null":           `{"hooks":null}`,
		"hooks list":           `{"hooks":[]}`,
		"events null":          `{"hooks":{"events":null}}`,
		"session start null":   `{"hooks":{"events":{"SessionStart":null}}}`,
		"group not object":     `{"hooks":{"events":{"SessionStart":[null]}}}`,
		"group hooks missing":  `{"hooks":{"events":{"SessionStart":[{}]}}}`,
		"group hooks null":     `{"hooks":{"events":{"SessionStart":[{"hooks":null}]}}}`,
		"hook not object":      `{"hooks":{"events":{"SessionStart":[{"hooks":[null]}]}}}`,
		"hook type missing":    `{"hooks":{"events":{"SessionStart":[{"hooks":[{}]}]}}}`,
		"command null":         `{"hooks":{"events":{"SessionStart":[{"hooks":[{"type":"command","command":null,"timeoutMs":5000}]}]}}}`,
		"process command null": `{"hooks":{"events":{"SessionStart":[{"hooks":[{"type":"process","command":null}]}]}}}`,
		"timeout string":       `{"hooks":{"events":{"SessionStart":[{"hooks":[{"type":"command","command":"operator-hook","timeoutMs":"bad"}]}]}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			home, config := zcodeHookTestPaths(t)
			writeZcodeHookExecutable(t, home, "#!/bin/sh\nexit 0\n")
			writeFile(t, config, initial)

			if err := executeZcodeHooks("install"); err == nil {
				t.Fatal("install accepted an invalid hook container")
			}
			if got := string(mustRead(t, config)); got != initial {
				t.Fatalf("install edited invalid config: %s", got)
			}
			wrapper := filepath.Join(home, ".zcode", "hooks", "roca-handoff.sh")
			if _, err := os.Stat(wrapper); !os.IsNotExist(err) {
				t.Fatalf("install wrote wrapper before rejecting config: %v", err)
			}
		})
	}
}

func TestZcodeHookInstallAcceptsSupportedHookUnion(t *testing.T) {
	for name, operatorHook := range map[string]string{
		"command defaults": `{"type":"command","command":"operator-hook"}`,
		"command timeout":  `{"type":"command","command":"operator-hook","timeout":1.5}`,
		"process":          `{"type":"process","command":"operator-hook","args":["one"],"timeoutMs":5000}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, config := zcodeHookTestPaths(t)
			writeFile(t, config, `{"hooks":{"events":{"SessionStart":[{"hooks":[`+operatorHook+`]}]}}}`)
			requireZcodeHooks(t, "install")
		})
	}
}

func TestZcodeHookQuotesWrapperCommandAndRecognizesLegacyRawPath(t *testing.T) {
	home, _ := zcodeHookTestPaths(t)
	zcodeHome := filepath.Join(home, "zcode home")
	t.Setenv("ZCODE_HOME", zcodeHome)
	config := filepath.Join(zcodeHome, "cli", "config.json")
	writeZcodeHookExecutable(t, home, "#!/bin/sh\nexit 0\n")
	writeFile(t, config, "{}\n")
	requireZcodeHooks(t, "install")

	wrapper := filepath.Join(zcodeHome, "hooks", "roca-handoff.sh")
	document := readZcodeHookDocument(t, config)
	hooks := document["hooks"].(map[string]any)
	events := hooks["events"].(map[string]any)
	group := events["SessionStart"].([]any)[0].(map[string]any)
	hook := group["hooks"].([]any)[0].(map[string]any)
	if hook["command"] != shellQuote(wrapper) {
		t.Fatalf("wrapper command = %#v", hook["command"])
	}

	hook["command"] = wrapper
	body, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, config, string(body))
	requireZcodeHooks(t, "install")
	requireZcodeHooks(t, "uninstall")
	if strings.Contains(string(mustRead(t, config)), wrapper) {
		t.Fatal("legacy raw wrapper registration survived uninstall")
	}
}

func TestZcodeHookOwnershipFailureRetainsWorkingHookWithoutFollowingSymlink(t *testing.T) {
	home, config := zcodeHookTestPaths(t)
	writeZcodeHookExecutable(t, home, "#!/bin/sh\nexit 0\n")
	writeFile(t, config, "{}\n")
	target := filepath.Join(home, "redirected-owned.json")
	sidecar := config + ".roca-owned"
	if err := os.Symlink(target, sidecar); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	if err := executeZcodeHooks("install"); err == nil {
		t.Fatal("install followed a dangling ownership symlink")
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("ownership JSON was written through symlink: %v", err)
	}
	if info, err := os.Lstat(sidecar); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("ownership symlink was replaced: info=%v err=%v", info, err)
	}
	wrapper := filepath.Join(home, ".zcode", "hooks", "roca-handoff.sh")
	if _, err := os.Stat(wrapper); err != nil {
		t.Fatalf("working wrapper was rolled back after config mutation: %v", err)
	}
	if body := string(mustRead(t, config)); !strings.Contains(body, wrapper) {
		t.Fatalf("failed install left no matching hook registration: %s", body)
	}
}

func TestZcodeHookInstallRollsBackWrapperAfterConfigEditFailure(t *testing.T) {
	for _, existing := range []bool{false, true} {
		name := "new wrapper"
		if existing {
			name = "existing wrapper"
		}
		t.Run(name, func(t *testing.T) {
			home, config := zcodeHookTestPaths(t)
			writeZcodeHookExecutable(t, home, "#!/bin/sh\nexit 0\n")
			writeFile(t, config, "{}\n")
			wrapper := filepath.Join(home, ".zcode", "hooks", "roca-handoff.sh")
			before := "#!/bin/sh\nprintf 'operator wrapper\\n'\n"
			if existing {
				writeFile(t, wrapper, before)
				if err := os.Chmod(wrapper, 0o600); err != nil {
					t.Fatal(err)
				}
			}

			configDir := filepath.Dir(config)
			if err := os.Chmod(configDir, 0o500); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(configDir, 0o700) })
			probe := filepath.Join(configDir, "write-probe")
			if err := os.WriteFile(probe, []byte("probe"), 0o600); err == nil {
				_ = os.Remove(probe)
				t.Skip("directory permissions are not enforced")
			}

			if err := executeZcodeHooks("install"); err == nil {
				t.Fatal("install succeeded despite an unwritable config directory")
			}
			if got := string(mustRead(t, config)); got != "{}\n" {
				t.Fatalf("failed install edited config: %s", got)
			}
			if existing {
				if got := string(mustRead(t, wrapper)); got != before {
					t.Fatalf("failed install did not restore wrapper: %s", got)
				}
				info, err := os.Stat(wrapper)
				if err != nil {
					t.Fatal(err)
				}
				if info.Mode().Perm() != 0o600 {
					t.Fatalf("wrapper mode = %o, want 600", info.Mode().Perm())
				}
			} else if _, err := os.Stat(wrapper); !os.IsNotExist(err) {
				t.Fatalf("failed install left wrapper behind: %v", err)
			}
			if _, err := os.Stat(wrapper + ".roca.bak"); !os.IsNotExist(err) {
				t.Fatalf("failed install left wrapper backup behind: %v", err)
			}
		})
	}
}

func TestZcodeHookReinstallPreservesContainerOwnership(t *testing.T) {
	_, config := zcodeHookTestPaths(t)
	writeFile(t, config, "{}\n")
	requireZcodeHooks(t, "install")
	writeFile(t, config, "{\"hooks\":{\"enabled\":true}}\n")
	requireZcodeHooks(t, "install")
	requireZcodeHooks(t, "uninstall")
	document := readZcodeHookDocument(t, config)
	if _, ok := document["hooks"]; ok {
		t.Fatalf("reinstall lost ownership of the product-created hooks container: %#v", document)
	}
}

func TestZcodeHookUninstallPreservesGroupMetadata(t *testing.T) {
	_, config := zcodeHookTestPaths(t)
	writeFile(t, config, "{}\n")
	requireZcodeHooks(t, "install")
	document := readZcodeHookDocument(t, config)
	hooks := document["hooks"].(map[string]any)
	events := hooks["events"].(map[string]any)
	groups := events["SessionStart"].([]any)
	group := groups[0].(map[string]any)
	group["matcher"] = "operator-owned"
	body, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, config, string(append(body, '\n')))

	requireZcodeHooks(t, "uninstall")
	document = readZcodeHookDocument(t, config)
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

func TestZcodeHookInstallRejectsOperatorOwnedEmptyGroup(t *testing.T) {
	home, config := zcodeHookTestPaths(t)
	initial := `{"hooks":{"enabled":true,"events":{"SessionStart":[{"hooks":[]}]}}}`
	writeFile(t, config, initial)

	if err := executeZcodeHooks("install"); err == nil {
		t.Fatal("install accepted an empty operator-owned hook group")
	}
	if got := string(mustRead(t, config)); got != initial {
		t.Fatalf("install edited invalid config: %s", got)
	}
	wrapper := filepath.Join(home, ".zcode", "hooks", "roca-handoff.sh")
	if _, err := os.Stat(wrapper); !os.IsNotExist(err) {
		t.Fatalf("install wrote wrapper before rejecting config: %v", err)
	}
}

func TestZcodeHookLifecyclePreservesLargeOperatorNumbers(t *testing.T) {
	_, config := zcodeHookTestPaths(t)
	initial := `{"hooks":{"enabled":true,"events":{"SessionStart":[{"operatorSequence":9007199254740993,"hooks":[{"type":"command","command":"operator-hook","timeoutMs":5000}]}]}}}`
	writeFile(t, config, initial)
	for _, action := range []string{"install", "uninstall"} {
		requireZcodeHooks(t, action)
	}
	body := mustRead(t, config)
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.UseNumber()
	var document map[string]any
	if err := decoder.Decode(&document); err != nil {
		t.Fatal(err)
	}
	hooks := document["hooks"].(map[string]any)
	events := hooks["events"].(map[string]any)
	groups := events["SessionStart"].([]any)
	if len(groups) != 1 {
		t.Fatalf("SessionStart groups = %d, want operator group", len(groups))
	}
	sequence, ok := groups[0].(map[string]any)["operatorSequence"].(json.Number)
	if !ok || sequence.String() != "9007199254740993" {
		t.Fatalf("operatorSequence = %#v", sequence)
	}
}
