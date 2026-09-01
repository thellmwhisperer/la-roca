package cli

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/artifact"
	"github.com/thellmwhisperer/la-roca/internal/distribution/agentcfg"
	"github.com/thellmwhisperer/la-roca/internal/distribution/lifecycle"
	"github.com/thellmwhisperer/la-roca/internal/distribution/rocaops"
	"github.com/thellmwhisperer/la-roca/internal/provider/service"
)

func executeZcodeTestCLI(args ...string) error {
	root := rootCommand(&cliEnv{out: io.Discard, errOut: io.Discard, build: Build{Version: "test"}})
	root.SetArgs(args)
	return root.Execute()
}

func runZcodeTestCLI(t *testing.T, args ...string) {
	t.Helper()
	if err := executeZcodeTestCLI(args...); err != nil {
		t.Fatalf("roca %v: %v", args, err)
	}
}

func installZcodeTestIntegration(t *testing.T, integration, home string) {
	t.Helper()
	runZcodeTestCLI(t, integration, "install", "zcode", "--executable", filepath.Join(home, "roca"))
}

func purgeZcodeTestIntegrations(purge bool) lifecycle.Report {
	report := lifecycle.Report{Purged: true, Deleted: []string{}}
	(&cliEnv{out: io.Discard, errOut: io.Discard}).withdrawTheIntegrations(&report, purge)
	return report
}

func readZcodeTestJSON(t *testing.T, path string) ([]byte, map[string]any) {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(body, &document); err != nil {
		t.Fatal(err)
	}
	return body, document
}

func writeZcodeTestJSON(t *testing.T, path string, document map[string]any) []byte {
	t.Helper()
	body, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	return body
}

func installZcodeTestHook(t *testing.T, config, wrapper, executable string) {
	t.Helper()
	if _, _, err := installZcodeHandoffHook(config, wrapper, executable); err != nil {
		t.Fatal(err)
	}
}

func uninstallZcodeTestHook(t *testing.T, config, wrapper, executable string) string {
	t.Helper()
	_, warning, err := uninstallZcodeHandoffHook(config, wrapper, []byte(zcodeWrapper(executable)))
	if err != nil {
		t.Fatal(err)
	}
	return warning
}

func zcodeOperatorHookConfig(command string) string {
	return fmt.Sprintf(`{"hooks":{"enabled":true,"events":{"SessionStart":[{"hooks":[{"type":"command","command":%q,"timeoutMs":4000}]}]}}}`, command)
}

func zcodeTestConfigAndWrapper(home string) (string, string) {
	return filepath.Join(home, ".zcode", "cli", "config.json"),
		filepath.Join(home, ".zcode", "hooks", "roca-handoff.sh")
}

func roundTripZcodeOperatorHook(t *testing.T, home, config, wrapper, command string) (string, string) {
	t.Helper()
	initial := zcodeOperatorHookConfig(command)
	writeFile(t, config, initial)
	executable := filepath.Join(home, "roca")
	installZcodeTestHook(t, config, wrapper, executable)
	return initial, uninstallZcodeTestHook(t, config, wrapper, executable)
}

func assertZcodeOwnershipExpiresWithoutContinuity(t *testing.T, integration string) {
	t.Helper()
	home := skillTestHome(t)
	rootPath := filepath.Join(home, ".zcode")
	config := filepath.Join(rootPath, "cli", "config.json")
	installZcodeTestIntegration(t, integration, home)
	if err := os.RemoveAll(rootPath); err != nil {
		t.Fatal(err)
	}
	writeFile(t, config, "{}")
	installZcodeTestIntegration(t, integration, home)
	report := purgeZcodeTestIntegrations(true)
	if !strings.Contains(strings.Join(report.Errors, "\n"), config) {
		t.Fatalf("operator-recreated config was not retained: %v", report.Errors)
	}
	if body, err := os.ReadFile(config); err != nil || strings.TrimSpace(string(body)) != "{}" {
		t.Fatalf("operator-recreated config changed: body=%q err=%v", body, err)
	}
}

func assertFreshZcodePurgeRemovesRuntimePaths(t *testing.T, integration string) {
	t.Helper()
	home := skillTestHome(t)
	installZcodeTestIntegration(t, integration, home)
	report := purgeZcodeTestIntegrations(true)
	if len(report.Errors) != 0 {
		t.Fatalf("purge errors = %v", report.Errors)
	}
	if _, err := os.Stat(filepath.Join(home, ".zcode")); !os.IsNotExist(err) {
		t.Fatalf("fresh %s runtime paths survived: %v", integration, err)
	}
}

func prepareZcodeTestWrapperDir(t *testing.T, home string) string {
	t.Helper()
	_, wrapper := zcodeTestConfigAndWrapper(home)
	if err := os.MkdirAll(filepath.Dir(wrapper), 0o700); err != nil {
		t.Fatal(err)
	}
	return wrapper
}

func TestZcodeHookPlatformSupportRequiresBash(t *testing.T) {
	for _, goos := range []string{"darwin", "linux"} {
		if err := zcodeHookPlatformError(goos, os.Stat); err != nil {
			t.Fatalf("%s support = %v", goos, err)
		}
	}
	if err := zcodeHookPlatformError("windows", nil); err == nil || !strings.Contains(err.Error(), "/bin/bash") {
		t.Fatalf("Windows support error = %v", err)
	}
	missing := func(string) (os.FileInfo, error) { return nil, os.ErrNotExist }
	if err := zcodeHookPlatformError("linux", missing); err == nil || !strings.Contains(err.Error(), "require executable") {
		t.Fatalf("Linux support without Bash = %v", err)
	}
}

func TestZcodeOutputValidatorRequiresOneContextObject(t *testing.T) {
	for _, test := range []struct {
		name, input string
		valid       bool
	}{
		{name: "valid", input: `{"additionalContext":"handoff"}`, valid: true},
		{name: "empty"},
		{name: "plain", input: `handoff`},
		{name: "multiple", input: `{"additionalContext":"a"} {"additionalContext":"b"}`},
		{name: "extra member", input: `{"additionalContext":"a","other":true}`},
		{name: "duplicate member", input: `{"additionalContext":"a","additionalContext":"b"}`},
		{name: "wrong type", input: `{"additionalContext":3}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			var out strings.Builder
			root := rootCommand(&cliEnv{out: &out})
			root.SetIn(strings.NewReader(test.input))
			root.SetArgs([]string{"hooks", "validate-zcode-output"})
			err := root.Execute()
			if test.valid {
				if err != nil || strings.TrimSpace(out.String()) != zcodeOutputValidationToken {
					t.Fatalf("validation: output=%q err=%v", out.String(), err)
				}
			} else if err == nil {
				t.Fatalf("invalid payload accepted: %q", test.input)
			}
		})
	}
}

func TestZcodeHookInstallerWritesNestedSessionStartAndJSONWrapper(t *testing.T) {
	home := skillTestHome(t)
	binary := filepath.Join(home, "bin", "roca")
	if err := os.MkdirAll(filepath.Dir(binary), 0o700); err != nil {
		t.Fatal(err)
	}
	handoffJSON := `{"project":"demo","handoffs":[{"content":"synthetic handoff"}]}`
	fake := `#!/bin/sh
if [ "$1 $2 $3" = "handoff latest --json" ]; then
  printf '%s\n' '` + handoffJSON + `'
  exit 0
fi
if [ "$1 $2 $3" = "hooks run zcode" ]; then
  [ "$(cat)" = '` + handoffJSON + `' ] || exit 1
  printf '%s\n' '{"additionalContext":"{\"project\":\"demo\",\"handoffs\":[{\"content\":\"synthetic handoff\"}]}"}'
  exit 0
fi
if [ "$1 $2" = "hooks validate-zcode-output" ]; then
  [ "$(cat)" = '{"additionalContext":"{\"project\":\"demo\",\"handoffs\":[{\"content\":\"synthetic handoff\"}]}"}' ] || exit 1
  printf '%s\n' 'roca-zcode-output-valid-v1'
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
	initial := `{"numeric_spelling":9007199254740993,"theme":"dark","hooks":{"enabled":false,"events":{"SessionStart":[{"hooks":[{"type":"command","command":"operator-hook","timeoutMs":5000}]}]}}}`
	if err := os.WriteFile(config, []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}

	for attempt := 0; attempt < 2; attempt++ {
		root := rootCommand(&cliEnv{out: io.Discard, errOut: io.Discard, build: Build{Version: "test"}})
		root.SetArgs([]string{"hooks", "install", "zcode"})
		err := root.Execute()
		if err == nil || !strings.Contains(err.Error(), "installed but inactive") ||
			!strings.Contains(err.Error(), "set hooks.enabled to true") {
			t.Fatalf("inactive install attempt %d error = %v", attempt+1, err)
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
	hooks := document["hooks"].(map[string]any)
	if hooks["enabled"] != false {
		t.Fatalf("hooks.enabled = %#v, want the operator's false value", hooks["enabled"])
	}
	if !strings.Contains(string(body), `"numeric_spelling":9007199254740993`) {
		t.Fatalf("installer re-encoded neighboring numeric configuration: %s", body)
	}
	if _, flat := hooks["SessionStart"]; flat {
		t.Fatal("installer wrote the rejected flat SessionStart shape")
	}
	events := hooks["events"].(map[string]any)
	entries := events["SessionStart"].([]any)
	if len(entries) != 2 {
		t.Fatalf("SessionStart entries = %d, want operator hook plus one Roca hook", len(entries))
	}
	if marker, _ := entries[1].(map[string]any)["matcher"].(string); !strings.Contains(marker, zcodeSessionStartMarker) {
		t.Fatalf("managed SessionStart marker = %#v", marker)
	}
	wrapper := filepath.Join(home, ".zcode", "hooks", "roca-handoff.sh")
	info, err := os.Stat(wrapper)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o100 == 0 {
		t.Fatal("wrapper is not executable")
	}
	registry, err := artifact.LoadRegistry(filepath.Join(home, ".roca", "artifacts.json"))
	if err != nil {
		t.Fatal(err)
	}
	entry, found := registry.Find(artifactKindHook, "zcode", wrapper)
	if !found || entry.Executable != binary || entry.SystemSHA256 != artifact.Checksum(zcodeWrapper(binary)) {
		t.Fatalf("ZCode wrapper ownership state = %#v", entry)
	}
	output, err := exec.Command(wrapper).Output()
	if err != nil {
		t.Fatalf("run wrapper: %v", err)
	}
	var context map[string]string
	if err := json.Unmarshal(output, &context); err != nil {
		t.Fatalf("wrapper stdout is not JSON: %v\n%s", err, output)
	}
	if context["additionalContext"] != handoffJSON {
		t.Fatalf("wrapper context = %#v", context)
	}
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf 'partial output'\nexit 1\n"), 0o700); err != nil {
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
	if context["additionalContext"] != "" {
		t.Fatalf("degraded wrapper context = %#v", context)
	}
	invalid := `#!/bin/sh
if [ "$1 $2 $3" = "handoff latest --json" ]; then
  printf '{}'
  exit 0
fi
if [ "$1 $2 $3" = "hooks run zcode" ]; then
  cat >/dev/null
  printf 'not json'
  exit 0
fi
if [ "$1 $2" = "hooks validate-zcode-output" ]; then
  printf 'wrong-token\n'
  exit 0
fi
exit 1
`
	if err := os.WriteFile(binary, []byte(invalid), 0o700); err != nil {
		t.Fatal(err)
	}
	output, err = exec.Command(wrapper).Output()
	if err != nil {
		t.Fatalf("wrapper should reject successful invalid output: %v", err)
	}
	context = nil
	if err := json.Unmarshal(output, &context); err != nil || context["additionalContext"] != "" {
		t.Fatalf("invalid child output escaped wrapper validation: %q err=%v", output, err)
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
	if string(body) != initial {
		t.Fatalf("uninstall did not restore neighboring settings exactly:\nwant %s\n got %s", initial, body)
	}

	root = rootCommand(&cliEnv{out: io.Discard, build: Build{Version: "test"}})
	root.SetArgs([]string{"hooks", "install", "zcode"})
	if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "installed but inactive") {
		t.Fatalf("inactive reinstall error = %v", err)
	}
	report := lifecycle.Report{Purged: true, Deleted: []string{}}
	(&cliEnv{out: io.Discard, errOut: io.Discard}).withdrawTheIntegrations(&report, false)
	if _, err := os.Stat(wrapper); !os.IsNotExist(err) {
		t.Fatal("zcode hook wrapper survived full uninstall")
	}
	body, err = os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "roca-handoff.sh") {
		t.Fatalf("zcode hook survived full uninstall: %s", body)
	}
}

func TestZcodeFreshHookPurgeRemovesCreatedRuntimePaths(t *testing.T) {
	assertFreshZcodePurgeRemovesRuntimePaths(t, "hooks")
}

func TestZcodeHookPurgeRetainsReplacedLifecycleLock(t *testing.T) {
	for _, test := range []struct {
		name    string
		replace func(string) error
		verify  func(*testing.T, string)
	}{
		{
			name: "nonempty file",
			replace: func(path string) error {
				return os.WriteFile(path, []byte("operator data\n"), 0o600)
			},
			verify: func(t *testing.T, path string) {
				body, err := os.ReadFile(path)
				if err != nil || string(body) != "operator data\n" {
					t.Fatalf("replacement changed: body=%q err=%v", body, err)
				}
			},
		},
		{
			name: "symlink",
			replace: func(path string) error {
				target := path + ".operator"
				if err := os.WriteFile(target, []byte("operator target\n"), 0o600); err != nil {
					return err
				}
				if err := os.Remove(path); err != nil {
					return err
				}
				return os.Symlink(target, path)
			},
			verify: func(t *testing.T, path string) {
				info, err := os.Lstat(path)
				if err != nil || info.Mode()&os.ModeSymlink == 0 {
					t.Fatalf("replacement symlink changed: info=%v err=%v", info, err)
				}
				body, err := os.ReadFile(path + ".operator")
				if err != nil || string(body) != "operator target\n" {
					t.Fatalf("replacement target changed: body=%q err=%v", body, err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := skillTestHome(t)
			installZcodeTestIntegration(t, "hooks", home)
			lockPath := filepath.Join(home, ".zcode", ".roca-hooks.lock")
			if err := test.replace(lockPath); err != nil {
				t.Fatal(err)
			}
			report := purgeZcodeTestIntegrations(true)
			if !strings.Contains(strings.Join(report.Errors, "\n"), lockPath) {
				t.Fatalf("replaced lock was not reported: %v", report.Errors)
			}
			test.verify(t, lockPath)
		})
	}
}

func TestZcodeHookInstallPreservesExistingLifecycleLock(t *testing.T) {
	home := skillTestHome(t)
	root := filepath.Join(home, ".zcode")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(root, ".roca-hooks.lock")
	if err := os.WriteFile(lockPath, []byte("operator lock data\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(lockPath, 0o640); err != nil {
		t.Fatal(err)
	}
	before, err := os.Lstat(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	installZcodeTestIntegration(t, "hooks", home)
	after, err := os.Lstat(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) || after.Mode().Perm() != 0o640 || string(body) != "operator lock data\n" {
		t.Fatalf("existing lifecycle lock changed: same=%v mode=%o body=%q",
			os.SameFile(before, after), after.Mode().Perm(), body)
	}
}

func TestZcodeHookInstallRejectsLifecycleLockSymlink(t *testing.T) {
	home := skillTestHome(t)
	root := filepath.Join(home, ".zcode")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(home, "operator-data")
	if err := os.WriteFile(target, []byte("operator data\n"), 0o744); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(target, 0o744); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, ".roca-hooks.lock")); err != nil {
		t.Fatal(err)
	}
	err := executeZcodeTestCLI("hooks", "install", "zcode", "--executable", filepath.Join(home, "roca"))
	if err == nil || !strings.Contains(err.Error(), "non-regular ZCode lifecycle lock") {
		t.Fatalf("symlink lifecycle lock error = %v", err)
	}
	info, statErr := os.Stat(target)
	body, readErr := os.ReadFile(target)
	if statErr != nil || readErr != nil || info.Mode().Perm() != 0o744 || string(body) != "operator data\n" {
		t.Fatalf("symlink target changed: mode=%v body=%q statErr=%v readErr=%v",
			info, body, statErr, readErr)
	}
}

func TestZcodeFailedLifecycleLockAcquisitionCleansOnlyCreatedLock(t *testing.T) {
	for _, test := range []struct {
		name       string
		replace    bool
		wantExists bool
	}{
		{name: "created lock", wantExists: false},
		{name: "operator replacement", replace: true, wantExists: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := skillTestHome(t)
			config, wrapper := zcodeTestConfigAndWrapper(home)
			injected := errors.New("synthetic acquisition failure")
			_, created, err := lockZcodeHookLifecycleWith(config, wrapper, true,
				func(path string, _ os.FileInfo) (func() error, error) {
					if test.replace {
						if err := os.Remove(path); err != nil {
							t.Fatal(err)
						}
						if err := os.WriteFile(path, []byte("operator lock\n"), 0o640); err != nil {
							t.Fatal(err)
						}
					}
					return nil, injected
				})
			if !errors.Is(err, injected) || created {
				t.Fatalf("lock result: created=%v err=%v", created, err)
			}
			lockPath := filepath.Join(home, ".zcode", ".roca-hooks.lock")
			body, readErr := os.ReadFile(lockPath)
			if test.wantExists {
				if readErr != nil || string(body) != "operator lock\n" {
					t.Fatalf("operator replacement changed: body=%q err=%v", body, readErr)
				}
			} else if !os.IsNotExist(readErr) {
				t.Fatalf("created lock survived failed acquisition: body=%q err=%v", body, readErr)
			}
		})
	}
}

func TestZcodeFreshHookInstallEnablesHooksAndReportsIt(t *testing.T) {
	home := skillTestHome(t)
	var notices strings.Builder
	root := rootCommand(&cliEnv{out: io.Discard, errOut: &notices, build: Build{Version: "test"}})
	root.SetArgs([]string{"hooks", "install", "zcode", "--executable", filepath.Join(home, "roca")})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(home, ".zcode", "cli", "config.json")
	body, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	declared, enabled, err := agentcfg.ZcodeHooksEnabled(string(body))
	if err != nil || !declared || !enabled {
		t.Fatalf("hooks.enabled: declared=%v enabled=%v err=%v", declared, enabled, err)
	}
	if !strings.Contains(notices.String(), "enabled ZCode hooks by adding hooks.enabled: true") {
		t.Fatalf("enablement notice = %q", notices.String())
	}
	runZcodeTestCLI(t, "hooks", "uninstall", "zcode")
	body, err = os.ReadFile(config)
	if err != nil || strings.TrimSpace(string(body)) != "{}" {
		t.Fatalf("created enablement survived uninstall: body=%q err=%v", body, err)
	}
}

func TestZcodePurgeExpiresOwnershipAfterRuntimeReplacement(t *testing.T) {
	for _, integration := range []string{"mcp", "hooks"} {
		t.Run(integration, func(t *testing.T) {
			home := skillTestHome(t)
			installZcodeTestIntegration(t, integration, home)
			root := filepath.Join(home, ".zcode")
			config := filepath.Join(root, "cli", "config.json")
			if err := os.RemoveAll(root); err != nil {
				t.Fatal(err)
			}
			writeFile(t, config, `{}`)
			report := purgeZcodeTestIntegrations(true)
			if !strings.Contains(strings.Join(report.Errors, "\n"), config) {
				t.Fatalf("retained replacement was not reported: %v", report.Errors)
			}
			if body, err := os.ReadFile(config); err != nil || string(body) != `{}` {
				t.Fatalf("operator replacement changed: body=%q err=%v", body, err)
			}
			registry, err := artifact.LoadRegistry(filepath.Join(home, ".roca", "artifacts.json"))
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range registry.Entries {
				if entry.Runtime == agentcfg.RuntimeZcode &&
					(entry.Kind == artifactKindMCP || entry.Kind == artifactKindHook) {
					t.Fatalf("stale ownership survived: %#v", entry)
				}
			}
		})
	}
}

func TestZcodePurgeReconcilesArtifactsWhenConfigIsMissing(t *testing.T) {
	for _, integration := range []string{"mcp", "hooks"} {
		t.Run(integration, func(t *testing.T) {
			home := skillTestHome(t)
			installZcodeTestIntegration(t, integration, home)
			config := filepath.Join(home, ".zcode", "cli", "config.json")
			if err := os.Remove(config); err != nil {
				t.Fatal(err)
			}
			report := purgeZcodeTestIntegrations(true)
			if len(report.Errors) != 0 {
				t.Fatalf("missing config purge errors = %v", report.Errors)
			}
			if _, err := os.Stat(filepath.Join(home, ".zcode")); !os.IsNotExist(err) {
				t.Fatalf("registered %s artifacts survived missing config purge: %v", integration, err)
			}
			registry, err := artifact.LoadRegistry(filepath.Join(home, ".roca", "artifacts.json"))
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range registry.Entries {
				if entry.Runtime == agentcfg.RuntimeZcode &&
					(entry.Kind == artifactKindMCP || entry.Kind == artifactKindHook) {
					t.Fatalf("reconciled ownership survived: %#v", entry)
				}
			}
		})
	}
}

func TestZcodeCombinedMCPAndHookPurgeIsAggregated(t *testing.T) {
	home := skillTestHome(t)
	for _, integration := range []string{"mcp", "hooks"} {
		installZcodeTestIntegration(t, integration, home)
	}
	report := purgeZcodeTestIntegrations(true)
	if len(report.Errors) != 0 {
		t.Fatalf("aggregated purge errors = %v", report.Errors)
	}
	if _, err := os.Stat(filepath.Join(home, ".zcode")); !os.IsNotExist(err) {
		t.Fatalf("combined ZCode artifacts survived purge: %v", err)
	}
	registry, err := artifact.LoadRegistry(filepath.Join(home, ".roca", "artifacts.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range registry.Entries {
		if entry.Runtime == agentcfg.RuntimeZcode && (entry.Kind == artifactKindMCP || entry.Kind == artifactKindHook) {
			t.Fatalf("stale ZCode ownership survived: %#v", entry)
		}
	}
}

func TestZcodeHookUninstallKeepsActivationForNeighboringHooks(t *testing.T) {
	home := skillTestHome(t)
	installZcodeTestIntegration(t, "hooks", home)
	config := filepath.Join(home, ".zcode", "cli", "config.json")
	body, document := readZcodeTestJSON(t, config)
	hooks := document["hooks"].(map[string]any)
	events := hooks["events"].(map[string]any)
	events["Other"] = []any{map[string]any{"hooks": []any{map[string]any{
		"type": "command", "command": "operator-hook", "timeoutMs": float64(5000),
	}}}}
	writeZcodeTestJSON(t, config, document)
	runZcodeTestCLI(t, "hooks", "uninstall", "zcode")
	body, document = readZcodeTestJSON(t, config)
	hooks = document["hooks"].(map[string]any)
	if hooks["enabled"] != true {
		t.Fatalf("neighboring hook was disabled: %s", body)
	}
	commands, err := agentcfg.ZcodeHookCommands(string(body))
	if err != nil || !slices.Contains(commands, "operator-hook") {
		t.Fatalf("operator hook changed: commands=%v err=%v", commands, err)
	}
}

func TestZcodeHookPurgeReportsRetainedCreatedConfig(t *testing.T) {
	home := skillTestHome(t)
	installZcodeTestIntegration(t, "hooks", home)
	config := filepath.Join(home, ".zcode", "cli", "config.json")
	_, document := readZcodeTestJSON(t, config)
	document["operator"] = true
	writeZcodeTestJSON(t, config, document)
	report := purgeZcodeTestIntegrations(true)
	if !strings.Contains(strings.Join(report.Errors, "\n"), config) {
		t.Fatalf("retained config was not reported: %v", report.Errors)
	}
	if body, err := os.ReadFile(config); err != nil || !strings.Contains(string(body), `"operator":true`) {
		t.Fatalf("operator config changed: body=%q err=%v", body, err)
	}
}

func TestZcodePurgeReportsUnprovenPathsAfterOrdinaryUninstall(t *testing.T) {
	home := skillTestHome(t)
	installZcodeTestIntegration(t, "hooks", home)
	runZcodeTestCLI(t, "hooks", "uninstall", "zcode")
	installZcodeTestIntegration(t, "hooks", home)
	report := purgeZcodeTestIntegrations(true)
	lockPath := filepath.Join(home, ".zcode", ".roca-hooks.lock")
	if !slices.ContainsFunc(report.Errors, func(message string) bool { return strings.Contains(message, lockPath) }) {
		t.Fatalf("unproven ZCode path was not reported: %v", report.Errors)
	}
	if _, err := os.Stat(lockPath); err != nil {
		t.Fatalf("unproven lock was not preserved: %v", err)
	}
}

func TestZcodeHookPurgePreservesPreexistingConfig(t *testing.T) {
	home := skillTestHome(t)
	config := filepath.Join(home, ".zcode", "cli", "config.json")
	initial := `{"operator":true,"hooks":{"enabled":true}}`
	writeFile(t, config, initial)
	installZcodeTestIntegration(t, "hooks", home)
	report := purgeZcodeTestIntegrations(true)
	if len(report.Errors) != 0 {
		t.Fatalf("purge errors = %v", report.Errors)
	}
	body, err := os.ReadFile(config)
	if err != nil || string(body) != initial {
		t.Fatalf("preexisting config changed: body=%q err=%v", body, err)
	}
	for _, path := range []string{
		filepath.Join(home, ".zcode", ".roca-hooks.lock"),
		filepath.Join(home, ".zcode", "hooks"),
	} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("created ZCode path survived purge: %s", path)
		}
	}
}

func TestZcodeHookUninstallWithoutInstallCreatesNoRuntimeDirectories(t *testing.T) {
	home := skillTestHome(t)
	runZcodeTestCLI(t, "hooks", "uninstall", "zcode")
	if _, err := os.Stat(filepath.Join(home, ".zcode")); !os.IsNotExist(err) {
		t.Fatalf("empty uninstall created ZCode directories: %v", err)
	}
}

func TestZcodeHookReinstallRepointsManagedWrapper(t *testing.T) {
	home := skillTestHome(t)
	config := filepath.Join(home, ".zcode", "cli", "config.json")
	writeFile(t, config, `{"hooks":{"enabled":true}}`)
	first := filepath.Join(home, "bin", "roca-v1")
	second := filepath.Join(home, "bin", "roca-v2")
	for _, executable := range []string{first, second} {
		runZcodeTestCLI(t, "hooks", "install", "zcode", "--executable", executable)
	}
	wrapper := filepath.Join(home, ".zcode", "hooks", "roca-handoff.sh")
	if body, err := os.ReadFile(wrapper); err != nil || string(body) != zcodeWrapper(second) {
		t.Fatalf("repointed wrapper: body=%q err=%v", body, err)
	}
}

func TestZcodeHookReinstallMergesNewEnablementOwnership(t *testing.T) {
	home := skillTestHome(t)
	config := filepath.Join(home, ".zcode", "cli", "config.json")
	writeFile(t, config, `{"hooks":{"enabled":true}}`)
	installZcodeTestIntegration(t, "hooks", home)
	_, document := readZcodeTestJSON(t, config)
	delete(document["hooks"].(map[string]any), "enabled")
	writeZcodeTestJSON(t, config, document)
	installZcodeTestIntegration(t, "hooks", home)
	runZcodeTestCLI(t, "hooks", "uninstall", "zcode")
	body, document := readZcodeTestJSON(t, config)
	hooks := document["hooks"].(map[string]any)
	if _, found := hooks["enabled"]; found {
		t.Fatalf("reinstall-owned hooks.enabled survived uninstall: %s", body)
	}
	if _, found := hooks["events"]; found {
		t.Fatalf("managed hook survived uninstall: %s", body)
	}
}

func TestZcodeHookReinstallPreservesLiveOwnership(t *testing.T) {
	home := skillTestHome(t)
	for range 2 {
		installZcodeTestIntegration(t, "hooks", home)
	}
	config, wrapper := zcodeTestConfigAndWrapper(home)
	registry, err := artifact.LoadRegistry(filepath.Join(home, ".roca", "artifacts.json"))
	if err != nil {
		t.Fatal(err)
	}
	entry, found := registry.Find(artifactKindHook, agentcfg.RuntimeZcode, wrapper)
	if !found || !entry.CreatedRoot || !entry.CreatedConfigDir || !entry.CreatedHooksDir ||
		!entry.CreatedConfig || !entry.CreatedLock || !entry.CreatedHooksEnabled || entry.RootIdentity == "" {
		t.Fatalf("continuous hook provenance = %#v, found=%v", entry, found)
	}
	runZcodeTestCLI(t, "hooks", "uninstall", "zcode")
	_, document := readZcodeTestJSON(t, config)
	if hooks, found := document["hooks"].(map[string]any); found {
		if _, found := hooks["enabled"]; found {
			t.Fatalf("continuously owned hooks.enabled survived uninstall: %#v", document)
		}
	}
}

func TestZcodeHookReinstallExpiresOwnershipWithoutManagedContinuity(t *testing.T) {
	assertZcodeOwnershipExpiresWithoutContinuity(t, "hooks")
}

func TestZcodeHookReinstallDropsOwnershipOnRecreatedManagedTree(t *testing.T) {
	home := skillTestHome(t)
	rootPath := filepath.Join(home, ".zcode")
	config, wrapper := zcodeTestConfigAndWrapper(home)
	lockPath := filepath.Join(rootPath, ".roca-hooks.lock")
	installZcodeTestIntegration(t, "hooks", home)
	managedConfig, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	managedWrapper, err := os.ReadFile(wrapper)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(rootPath); err != nil {
		t.Fatal(err)
	}
	writeFile(t, config, string(managedConfig))
	writeFile(t, wrapper, string(managedWrapper))
	writeFile(t, lockPath, "")
	if err := os.Chmod(wrapper, 0o700); err != nil {
		t.Fatal(err)
	}
	installZcodeTestIntegration(t, "hooks", home)
	registry, err := artifact.LoadRegistry(filepath.Join(home, ".roca", "artifacts.json"))
	if err != nil {
		t.Fatal(err)
	}
	entry, found := registry.Find(artifactKindHook, agentcfg.RuntimeZcode, wrapper)
	if !found || entry.CreatedRoot || entry.CreatedConfigDir || entry.CreatedHooksDir || entry.CreatedConfig ||
		entry.CreatedLock || entry.CreatedHooksEnabled {
		t.Fatalf("recreated hook path ownership = %#v, found=%v", entry, found)
	}
	report := purgeZcodeTestIntegrations(true)
	if !strings.Contains(strings.Join(report.Errors, "\n"), lockPath) {
		t.Fatalf("operator-recreated lock was not reported: %v", report.Errors)
	}
	for _, path := range []string{config, lockPath, rootPath} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("operator-recreated ZCode path was removed: %s: %v", path, err)
		}
	}
}

func TestZcodePurgeDropsClaimsForReplacementTree(t *testing.T) {
	home := skillTestHome(t)
	rootPath := filepath.Join(home, ".zcode")
	config, wrapper := zcodeTestConfigAndWrapper(home)
	lockPath := filepath.Join(rootPath, ".roca-hooks.lock")
	installZcodeTestIntegration(t, "mcp", home)
	installZcodeTestIntegration(t, "hooks", home)
	managedConfig, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	managedWrapper, err := os.ReadFile(wrapper)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(rootPath); err != nil {
		t.Fatal(err)
	}
	writeFile(t, config, string(managedConfig))
	writeFile(t, wrapper, string(managedWrapper))
	writeFile(t, lockPath, "")
	if err := os.Chmod(wrapper, 0o700); err != nil {
		t.Fatal(err)
	}
	report := purgeZcodeTestIntegrations(true)
	if len(report.Errors) != 0 {
		t.Fatalf("replacement tree produced purge errors: %v", report.Errors)
	}
	matched, err := agentcfg.ZcodeMCPMatches(config, filepath.Join(home, "roca"))
	if err != nil || !matched || !zcodeManagedHookPresent(config) {
		t.Fatalf("replacement declaration state: mcp=%v hook=%v err=%v", matched, zcodeManagedHookPresent(config), err)
	}
	registry, err := artifact.LoadRegistry(filepath.Join(home, ".roca", "artifacts.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, found := registry.Find(artifactKindMCP, agentcfg.RuntimeZcode, config); found {
		t.Fatal("replacement MCP claim survived purge")
	}
	if _, found := registry.Find(artifactKindHook, agentcfg.RuntimeZcode, wrapper); found {
		t.Fatal("replacement hook claim survived purge")
	}
	for _, path := range []string{rootPath, config, wrapper, lockPath} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("replacement ZCode artifact was removed: %s: %v", path, err)
		}
	}
}

func TestZcodeDirectUninstallRetainsReplacementArtifacts(t *testing.T) {
	home := skillTestHome(t)
	rootPath := filepath.Join(home, ".zcode")
	config, wrapper := zcodeTestConfigAndWrapper(home)
	installZcodeTestIntegration(t, "mcp", home)
	installZcodeTestIntegration(t, "hooks", home)
	managedConfig, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	managedWrapper, err := os.ReadFile(wrapper)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(rootPath); err != nil {
		t.Fatal(err)
	}
	writeFile(t, config, string(managedConfig))
	writeFile(t, wrapper, string(managedWrapper))
	if err := os.Chmod(wrapper, 0o700); err != nil {
		t.Fatal(err)
	}
	runZcodeTestCLI(t, "hooks", "uninstall", "zcode")
	runZcodeTestCLI(t, "mcp", "uninstall", "zcode")
	matched, err := agentcfg.ZcodeMCPMatches(config, filepath.Join(home, "roca"))
	if err != nil || !matched || !zcodeManagedHookPresent(config) {
		t.Fatalf("replacement declaration state: mcp=%v hook=%v err=%v", matched, zcodeManagedHookPresent(config), err)
	}
	_, document := readZcodeTestJSON(t, config)
	hooks := document["hooks"].(map[string]any)
	if enabled, found := hooks["enabled"].(bool); !found || !enabled {
		t.Fatalf("replacement hooks.enabled changed: %#v", document)
	}
	mcp := document["mcp"].(map[string]any)
	if _, found := mcp["servers"]; !found {
		t.Fatalf("replacement MCP containers changed: %#v", document)
	}
	if body, err := os.ReadFile(wrapper); err != nil || string(body) != string(managedWrapper) {
		t.Fatalf("replacement wrapper changed: body=%q err=%v", body, err)
	}
	localLock := filepath.Join(rootPath, ".roca-hooks.lock")
	if _, err := os.Lstat(localLock); !os.IsNotExist(err) {
		t.Fatalf("replacement tree gained a local lock: %v", err)
	}
	configBeforeRetry, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	wrapperBeforeRetry, err := os.ReadFile(wrapper)
	if err != nil {
		t.Fatal(err)
	}
	var warnings strings.Builder
	env := &cliEnv{out: io.Discard, errOut: &warnings}
	for _, args := range [][]string{{"hooks", "uninstall", "zcode"}, {"mcp", "uninstall", "zcode"}} {
		root := rootCommand(env)
		root.SetArgs(args)
		if err := root.Execute(); err != nil {
			t.Fatalf("second roca %v: %v", args, err)
		}
	}
	if !strings.Contains(warnings.String(), "no managed ZCode hook ownership") ||
		!strings.Contains(warnings.String(), "no managed ZCode MCP ownership") {
		t.Fatalf("missing unowned retry warnings: %q", warnings.String())
	}
	configAfterRetry, configErr := os.ReadFile(config)
	wrapperAfterRetry, wrapperErr := os.ReadFile(wrapper)
	if configErr != nil || wrapperErr != nil || !bytes.Equal(configAfterRetry, configBeforeRetry) ||
		!bytes.Equal(wrapperAfterRetry, wrapperBeforeRetry) {
		t.Fatalf("retry changed replacement tree: config=%q wrapper=%q configErr=%v wrapperErr=%v",
			configAfterRetry, wrapperAfterRetry, configErr, wrapperErr)
	}
	if _, err := os.Lstat(localLock); !os.IsNotExist(err) {
		t.Fatalf("retry created a local lock: %v", err)
	}
}

func TestZcodeHooksRejectClaudeOnlyFlags(t *testing.T) {
	for _, operation := range []string{"install", "uninstall"} {
		for _, flag := range []string{"--pills", "--handoff"} {
			t.Run(operation+" "+flag, func(t *testing.T) {
				home := skillTestHome(t)
				err := executeZcodeTestCLI("hooks", operation, "zcode", flag)
				if err == nil || !strings.Contains(err.Error(), "not-supported-on-zcode") ||
					!strings.Contains(err.Error(), "issue #274") {
					t.Fatalf("unsupported flag error = %v", err)
				}
				if _, err := os.Stat(filepath.Join(home, ".zcode")); !os.IsNotExist(err) {
					t.Fatalf("rejected flag changed ZCode state: %v", err)
				}
			})
		}
	}
}

func TestZcodeHooksRejectForce(t *testing.T) {
	home := skillTestHome(t)
	if err := executeZcodeTestCLI("hooks", "install", "zcode", "--force", "--executable", filepath.Join(home, "roca")); err == nil || !strings.Contains(err.Error(), "--force is not supported") {
		t.Fatalf("ZCode --force error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".zcode")); !os.IsNotExist(err) {
		t.Fatalf("rejected --force changed ZCode state: %v", err)
	}
}

func TestFullUninstallWithdrawsRegisteredZcodeHome(t *testing.T) {
	home := skillTestHome(t)
	t.Chdir(home)
	custom := filepath.Join(home, "custom-zcode")
	t.Setenv("ZCODE_HOME", "custom-zcode")
	config := filepath.Join(custom, "cli", "config.json")
	writeFile(t, config, `{"hooks":{"enabled":true}}`)
	installZcodeTestIntegration(t, "hooks", home)
	var output strings.Builder
	runSkill(t, &output, "skill", "install", "zcode")
	wrapper := filepath.Join(custom, "hooks", "roca-handoff.sh")
	skillPath := filepath.Join(custom, "skills", "roca", "SKILL.md")
	t.Setenv("ZCODE_HOME", "")
	report := purgeZcodeTestIntegrations(false)
	if len(report.Errors) != 0 {
		t.Fatalf("full uninstall errors = %v", report.Errors)
	}
	for _, path := range []string{wrapper, skillPath} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("registered ZCode artifact survived: %s", path)
		}
	}
	body, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "roca-handoff.sh") {
		t.Fatalf("registered ZCode hook survived: %s", body)
	}
}

func TestZcodeCustomHookPurgeWithdrawsDeclarationWithModifiedWrapper(t *testing.T) {
	home := skillTestHome(t)
	custom := filepath.Join(home, "custom-zcode")
	t.Setenv("ZCODE_HOME", custom)
	config := filepath.Join(custom, "cli", "config.json")
	writeFile(t, config, `{"hooks":{"enabled":true}}`)
	installZcodeTestIntegration(t, "hooks", home)
	wrapper := filepath.Join(custom, "hooks", "roca-handoff.sh")
	if err := os.WriteFile(wrapper, []byte("operator wrapper\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ZCODE_HOME", "")
	report := purgeZcodeTestIntegrations(true)
	if !strings.Contains(strings.Join(report.Errors, "\n"), config) {
		t.Fatalf("retained wrapper was not reported: %v", report.Errors)
	}
	body, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "roca-handoff.sh") {
		t.Fatalf("verified managed declaration survived purge: %s", body)
	}
	if body, err := os.ReadFile(wrapper); err != nil || string(body) != "operator wrapper\n" {
		t.Fatalf("modified wrapper changed: body=%q err=%v", body, err)
	}
	registry, err := artifact.LoadRegistry(filepath.Join(home, ".roca", "artifacts.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, found := registry.Find(artifactKindHook, agentcfg.RuntimeZcode, wrapper); found {
		t.Fatal("withdrawn custom hook ownership survived")
	}
}

func TestZcodeCustomHookPurgeWithdrawsEditedMarkers(t *testing.T) {
	for _, test := range []struct {
		name      string
		duplicate bool
	}{
		{name: "edited timeout"},
		{name: "duplicate marker", duplicate: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := skillTestHome(t)
			custom := filepath.Join(home, "custom-zcode")
			t.Setenv("ZCODE_HOME", custom)
			config := filepath.Join(custom, "cli", "config.json")
			writeFile(t, config, `{"hooks":{"enabled":true}}`)
			installZcodeTestIntegration(t, "hooks", home)
			_, document := readZcodeTestJSON(t, config)
			events := document["hooks"].(map[string]any)["events"].(map[string]any)
			groups := events["SessionStart"].([]any)
			if test.duplicate {
				events["SessionStart"] = append(groups, groups[0])
			} else {
				hooks := groups[0].(map[string]any)["hooks"].([]any)
				hooks[0].(map[string]any)["timeoutMs"] = float64(999)
			}
			writeZcodeTestJSON(t, config, document)
			wrapper := filepath.Join(custom, "hooks", "roca-handoff.sh")
			t.Setenv("ZCODE_HOME", "")
			report := purgeZcodeTestIntegrations(true)
			if !strings.Contains(strings.Join(report.Errors, "\n"), config) {
				t.Fatalf("retained artifacts were not reported: %v", report.Errors)
			}
			if zcodeManagedHookPresent(config) {
				t.Fatal("edited marker-owned declaration survived purge")
			}
			if _, err := os.Stat(wrapper); err != nil {
				t.Fatalf("wrapper without canonical declaration continuity was removed: %v", err)
			}
			registry, err := artifact.LoadRegistry(filepath.Join(home, ".roca", "artifacts.json"))
			if err != nil {
				t.Fatal(err)
			}
			if _, found := registry.Find(artifactKindHook, agentcfg.RuntimeZcode, wrapper); found {
				t.Fatal("withdrawn edited hook ownership survived")
			}
		})
	}
}

func TestZcodeFailedInstallRollsBackCreationProvenance(t *testing.T) {
	for _, integration := range []string{"mcp", "hooks"} {
		t.Run(integration, func(t *testing.T) {
			home := skillTestHome(t)
			configDir := filepath.Join(home, ".zcode", "cli")
			config := filepath.Join(configDir, "config.json")
			if err := os.MkdirAll(config, 0o700); err != nil {
				t.Fatal(err)
			}
			err := executeZcodeTestCLI(integration, "install", "zcode", "--executable", filepath.Join(home, "roca"))
			if err == nil {
				t.Fatal("install unexpectedly published into a non-file config path")
			}
			registry, loadErr := artifact.LoadRegistry(filepath.Join(home, ".roca", "artifacts.json"))
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			kind := artifactKindMCP
			path := config
			if integration == "hooks" {
				kind = artifactKindHook
				path = filepath.Join(home, ".zcode", "hooks", "roca-handoff.sh")
			}
			if _, found := registry.Find(kind, agentcfg.RuntimeZcode, path); found {
				t.Fatal("failed install retained creation provenance")
			}
		})
	}
}

func TestZcodeFailedConfigPublicationRestoresFreshWrapper(t *testing.T) {
	home := skillTestHome(t)
	config, wrapper := zcodeTestConfigAndWrapper(home)
	if err := os.MkdirAll(filepath.Dir(config), 0o700); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(home, "roca")
	command := zcodeOwnedHookCommand(wrapper)
	_, _, err := installZcodeHandoffHookWithPrevious(config, wrapper, executable, nil,
		func(_ string, _, _ bool) error {
			operator, err := agentcfg.DeclareZcodeSessionStartHook(
				`{}`, zcodeSessionStartMarker, command, 15000)
			if err != nil {
				return err
			}
			operator, _, err = agentcfg.EnsureZcodeHooksEnabled(operator)
			if err != nil {
				return err
			}
			return os.WriteFile(config, []byte(operator), 0o600)
		})
	if err == nil {
		t.Fatal("hook install accepted a lost config publication race")
	}
	if _, err := os.Stat(wrapper); !os.IsNotExist(err) {
		t.Fatalf("failed publication left a wrapper: %v", err)
	}
	if !zcodeManagedHookPresent(config) {
		t.Fatal("operator config used to win the race was changed")
	}
}

func TestFullUninstallRetainsBinaryForUnverifiedZcodeHook(t *testing.T) {
	home := skillTestHome(t)
	installZcodeTestIntegration(t, "hooks", home)
	config, wrapper := zcodeTestConfigAndWrapper(home)
	if err := os.WriteFile(config, []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	env := &cliEnv{out: &output, errOut: &output}
	if err := env.uninstall(uninstallCommand(env), strings.NewReader(""), false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "integration withdrawal failed") ||
		!strings.Contains(output.String(), "could not be verified") {
		t.Fatalf("unverified hook did not retain the binary:\n%s", output.String())
	}
	if _, err := os.Stat(wrapper); err != nil {
		t.Fatalf("unverified hook wrapper was removed: %v", err)
	}
}

func TestZcodePurgeKeepsOwnershipWhenConfigInspectionFails(t *testing.T) {
	for _, integration := range []string{"mcp", "hooks"} {
		t.Run(integration, func(t *testing.T) {
			home := skillTestHome(t)
			installZcodeTestIntegration(t, integration, home)
			config := filepath.Join(home, ".zcode", "cli", "config.json")
			if err := os.WriteFile(config, []byte(`{broken`), 0o600); err != nil {
				t.Fatal(err)
			}
			report := purgeZcodeTestIntegrations(true)
			if len(report.Errors) == 0 {
				t.Fatal("purge accepted an unreadable registered config")
			}
			registry, err := artifact.LoadRegistry(filepath.Join(home, ".roca", "artifacts.json"))
			if err != nil {
				t.Fatal(err)
			}
			kind := artifactKindMCP
			path := config
			if integration == "hooks" {
				kind = artifactKindHook
				path = filepath.Join(home, ".zcode", "hooks", "roca-handoff.sh")
			}
			if _, found := registry.Find(kind, agentcfg.RuntimeZcode, path); !found {
				t.Fatal("inspection failure discarded retry ownership")
			}
		})
	}
}

func TestZcodePurgeReconcilesExactWrapperAfterDeclarationRemoval(t *testing.T) {
	home := skillTestHome(t)
	rootPath := filepath.Join(home, ".zcode")
	config := filepath.Join(rootPath, "cli", "config.json")
	installZcodeTestIntegration(t, "hooks", home)
	body, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	withoutManaged, err := agentcfg.RemoveZcodeSessionStartHook(string(body), zcodeSessionStartMarker)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config, []byte(withoutManaged), 0o600); err != nil {
		t.Fatal(err)
	}
	wrapper := filepath.Join(home, ".zcode", "hooks", "roca-handoff.sh")
	report := purgeZcodeTestIntegrations(true)
	if len(report.Errors) != 0 {
		t.Fatalf("wrapper reconciliation errors = %v", report.Errors)
	}
	if _, err := os.Stat(rootPath); !os.IsNotExist(err) {
		t.Fatalf("installer-created ZCode state survived reconciliation: %v", err)
	}
	registry, err := artifact.LoadRegistry(filepath.Join(home, ".roca", "artifacts.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, found := registry.Find(artifactKindHook, agentcfg.RuntimeZcode, wrapper); found {
		t.Fatal("reconciled wrapper ownership survived")
	}
}

func TestZcodeHookUninstallTreatsMissingWrapperAsAbsent(t *testing.T) {
	home := skillTestHome(t)
	installZcodeTestIntegration(t, "hooks", home)
	config, wrapper := zcodeTestConfigAndWrapper(home)
	if err := os.Remove(wrapper); err != nil {
		t.Fatal(err)
	}
	var errOut strings.Builder
	env := &cliEnv{out: io.Discard, errOut: &errOut}
	root := rootCommand(env)
	root.SetArgs([]string{"hooks", "uninstall", "zcode"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if errOut.Len() != 0 {
		t.Fatalf("missing wrapper produced a warning: %s", errOut.String())
	}
	if zcodeManagedHookPresent(config) {
		t.Fatal("managed declaration survived missing-wrapper uninstall")
	}
	registry, err := artifact.LoadRegistry(filepath.Join(home, ".roca", "artifacts.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, found := registry.Find(artifactKindHook, agentcfg.RuntimeZcode, wrapper); found {
		t.Fatal("missing wrapper retained stale ownership")
	}
}

func TestZcodeHookUninstallIgnoresMissingForeignHookPath(t *testing.T) {
	home := skillTestHome(t)
	config, wrapper := zcodeTestConfigAndWrapper(home)
	missing := filepath.Join(home, "operator", "deleted-hook.sh")
	writeFile(t, config, zcodeOperatorHookConfig(missing))
	installZcodeTestIntegration(t, "hooks", home)
	var errOut strings.Builder
	env := &cliEnv{out: io.Discard, errOut: &errOut}
	root := rootCommand(env)
	root.SetArgs([]string{"hooks", "uninstall", "zcode"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if errOut.Len() != 0 {
		t.Fatalf("missing foreign hook produced a warning: %s", errOut.String())
	}
	if _, err := os.Stat(wrapper); !os.IsNotExist(err) {
		t.Fatalf("managed wrapper survived: %v", err)
	}
	body, err := os.ReadFile(config)
	if err != nil || !strings.Contains(string(body), missing) {
		t.Fatalf("foreign hook changed: body=%q err=%v", body, err)
	}
}

func TestZcodeUnreadableHookKeepsOwnershipForRetry(t *testing.T) {
	home := skillTestHome(t)
	config := filepath.Join(home, ".zcode", "cli", "config.json")
	writeFile(t, config, `{"hooks":{"enabled":true}}`)
	installZcodeTestIntegration(t, "hooks", home)
	if err := os.WriteFile(config, []byte(`{broken`), 0o600); err != nil {
		t.Fatal(err)
	}
	runZcodeTestCLI(t, "hooks", "uninstall", "zcode")
	wrapper := filepath.Join(home, ".zcode", "hooks", "roca-handoff.sh")
	registry, err := artifact.LoadRegistry(filepath.Join(home, ".roca", "artifacts.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, found := registry.Find(artifactKindHook, agentcfg.RuntimeZcode, wrapper); !found {
		t.Fatal("unreadable hook configuration lost retry ownership")
	}
	if _, err := os.Stat(wrapper); err != nil {
		t.Fatalf("managed wrapper was not retained: %v", err)
	}
}

func TestFullUninstallDoesNotLockForeignZcodeWrapper(t *testing.T) {
	home := skillTestHome(t)
	wrapper := filepath.Join(home, ".zcode", "hooks", "roca-handoff.sh")
	operator := "#!/bin/sh\nprintf 'operator\\n'\n"
	writeFile(t, wrapper, operator)
	if err := os.Chmod(wrapper, 0o700); err != nil {
		t.Fatal(err)
	}
	runZcodeTestCLI(t, "hooks", "uninstall", "zcode")
	purgeZcodeTestIntegrations(false)
	if body, err := os.ReadFile(wrapper); err != nil || string(body) != string(operator) {
		t.Fatalf("foreign wrapper changed: body=%q err=%v", body, err)
	}
	if _, err := os.Stat(filepath.Join(home, ".zcode", ".roca-hooks.lock")); !os.IsNotExist(err) {
		t.Fatalf("foreign wrapper created a runtime lock: %v", err)
	}
}

func TestFullUninstallDoesNotCreateUnselectedZcodeLock(t *testing.T) {
	home := skillTestHome(t)
	config := filepath.Join(home, ".zcode", "cli", "config.json")
	writeFile(t, config, `{"theme":"operator"}`)
	purgeZcodeTestIntegrations(false)
	if _, err := os.Stat(filepath.Join(home, ".zcode", ".roca-hooks.lock")); !os.IsNotExist(err) {
		t.Fatalf("unselected ZCode created a runtime lock: %v", err)
	}
}

func TestZcodeHookCommandExecutesWhenWrapperPathContainsSpaces(t *testing.T) {
	home := t.TempDir()
	config := filepath.Join(home, "config.json")
	wrapper := filepath.Join(home, "ZCode Home", "hooks", "roca-handoff.sh")
	binary := filepath.Join(home, "fake-roca")
	fake := `#!/bin/sh
if [ "$1 $2 $3" = "handoff latest --json" ]; then
  printf '%s\n' '{"project":"space-safe","handoffs":[]}'
  exit 0
fi
if [ "$1 $2 $3" = "hooks run zcode" ]; then
  [ "$(cat)" = '{"project":"space-safe","handoffs":[]}' ] || exit 1
  printf '%s\n' '{"additionalContext":"space-safe"}'
  exit 0
fi
if [ "$1 $2" = "hooks validate-zcode-output" ]; then
  [ "$(cat)" = '{"additionalContext":"space-safe"}' ] || exit 1
  printf '%s\n' 'roca-zcode-output-valid-v1'
  exit 0
fi
exit 1
`
	if err := os.WriteFile(binary, []byte(fake), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, warning, err := installZcodeHandoffHook(config, wrapper, binary); err != nil {
		t.Fatal(err)
	} else if warning != "" {
		t.Fatalf("fresh ZCode install warning = %q", warning)
	}
	body, document := readZcodeTestJSON(t, config)
	hooks := document["hooks"].(map[string]any)["events"].(map[string]any)["SessionStart"].([]any)
	command := hooks[0].(map[string]any)["hooks"].([]any)[0].(map[string]any)["command"].(string)
	output, err := exec.Command("sh", "-c", command).Output()
	if err != nil {
		t.Fatalf("execute quoted ZCode hook command: %v", err)
	}
	var context map[string]string
	if err := json.Unmarshal(output, &context); err != nil || context["additionalContext"] != "space-safe" {
		t.Fatalf("hook command output = %q, err = %v", output, err)
	}
	if _, _, err := uninstallZcodeHandoffHook(config, wrapper, []byte(zcodeWrapper(binary))); err != nil {
		t.Fatal(err)
	}
	body, err = os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), command) {
		t.Fatalf("uninstall left quoted hook command in config: %s", body)
	}
}

func TestZcodeInstallPreservesOperatorHookUsingSameWrapper(t *testing.T) {
	home := t.TempDir()
	config := filepath.Join(home, "config.json")
	wrapper := filepath.Join(home, "hooks", "roca-handoff.sh")
	operatorCommand := zcodeOwnedHookCommand(wrapper)
	initial := zcodeOperatorHookConfig(operatorCommand)
	writeFile(t, config, initial)
	executable := filepath.Join(home, "roca")
	installZcodeTestHook(t, config, wrapper, executable)
	body, document := readZcodeTestJSON(t, config)
	entries := document["hooks"].(map[string]any)["events"].(map[string]any)["SessionStart"].([]any)
	if len(entries) != 2 {
		t.Fatalf("SessionStart entries = %d, want operator and managed hooks", len(entries))
	}
	_, warning, err := uninstallZcodeHandoffHook(config, wrapper,
		[]byte(zcodeWrapper(filepath.Join(home, "roca"))))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(warning, "remaining operator-owned hook references it") {
		t.Fatalf("wrapper retention warning = %q", warning)
	}
	wrapperBody, err := os.ReadFile(wrapper)
	if err != nil {
		t.Fatalf("operator-referenced wrapper was removed: %v", err)
	}
	if string(wrapperBody) != zcodeWrapper(executable) {
		t.Fatalf("retained wrapper changed: %q", wrapperBody)
	}
	body, err = os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != initial {
		t.Fatalf("operator hook changed:\nwant %s\n got %s", initial, body)
	}
}

func TestZcodeUninstallKeepsWrapperForEquivalentOperatorPaths(t *testing.T) {
	for _, test := range []struct {
		name    string
		command func(string, string) (string, error)
	}{
		{name: "home alias", command: func(_, _ string) (string, error) {
			return "~/.zcode/hooks/roca-handoff.sh", nil
		}},
		{name: "symlink alias", command: func(home, wrapper string) (string, error) {
			alias := filepath.Join(home, "operator-wrapper")
			return shellQuote(alias), os.Symlink(wrapper, alias)
		}},
		{name: "ambiguous shell", command: func(_, _ string) (string, error) {
			return `test -x "$HOME/.zcode/hooks/roca-handoff.sh" && "$HOME/.zcode/hooks/roca-handoff.sh"`, nil
		}},
		{name: "nested command fragment", command: func(_, wrapper string) (string, error) {
			return "sh -c " + shellQuote(wrapper+" --flag"), nil
		}},
		{name: "multiline after comment", command: func(_, wrapper string) (string, error) {
			return "echo ok # note\n" + shellQuote(wrapper), nil
		}},
		{name: "ambiguous glob", command: func(_, _ string) (string, error) {
			return "roca-*.sh", nil
		}},
		{name: "attached option path", command: func(_, wrapper string) (string, error) {
			return "/bin/bash --rcfile=" + wrapper + " -i -c exit", nil
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			config, wrapper := zcodeTestConfigAndWrapper(home)
			operatorCommand, err := test.command(home, wrapper)
			if err != nil {
				t.Fatal(err)
			}
			initial, warning := roundTripZcodeOperatorHook(t, home, config, wrapper, operatorCommand)
			if !strings.Contains(warning, "remaining operator-owned hook references it") {
				t.Fatalf("wrapper retention warning = %q", warning)
			}
			if _, err := os.Stat(wrapper); err != nil {
				t.Fatalf("operator-referenced wrapper was removed: %v", err)
			}
			if body, err := os.ReadFile(config); err != nil || string(body) != initial {
				t.Fatalf("operator config changed: body=%q err=%v", body, err)
			}
		})
	}
}

func TestZcodeUninstallKeepsWrapperForAmbiguousDoubleQuotedEscape(t *testing.T) {
	home := filepath.Join(t.TempDir(), `home\q`)
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	config, wrapper := zcodeTestConfigAndWrapper(home)
	operatorCommand := `"` + wrapper + `"`
	initial, warning := roundTripZcodeOperatorHook(t, home, config, wrapper, operatorCommand)
	if !strings.Contains(warning, "could not compare remaining ZCode hook paths") {
		t.Fatalf("ambiguous escape warning = %q", warning)
	}
	if _, err := os.Stat(wrapper); err != nil {
		t.Fatalf("ambiguously referenced wrapper was removed: %v", err)
	}
	if body, err := os.ReadFile(config); err != nil || string(body) != initial {
		t.Fatalf("operator config changed: body=%q err=%v", body, err)
	}
}

func TestZcodeManagedHookFailureRemovesCreatedRuntimeLock(t *testing.T) {
	home := skillTestHome(t)
	wrapper := prepareZcodeTestWrapperDir(t, home)
	if err := os.WriteFile(wrapper, []byte("operator wrapper\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := executeZcodeTestCLI("hooks", "install", "zcode", "--executable", filepath.Join(home, "roca")); err == nil {
		t.Fatal("foreign wrapper was accepted")
	}
	if _, err := os.Stat(filepath.Join(home, ".zcode", ".roca-hooks.lock")); !os.IsNotExist(err) {
		t.Fatalf("failed install left runtime lock: %v", err)
	}
}

func TestZcodeHookRefusesSymlinkWrapper(t *testing.T) {
	home := skillTestHome(t)
	executable := filepath.Join(home, "roca")
	target := filepath.Join(home, "operator-wrapper")
	wrapper := prepareZcodeTestWrapperDir(t, home)
	if err := os.WriteFile(target, []byte(zcodeWrapper(executable)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, wrapper); err != nil {
		t.Fatal(err)
	}
	if err := executeZcodeTestCLI("hooks", "install", "zcode", "--executable", executable); err == nil || !strings.Contains(err.Error(), "non-regular") {
		t.Fatalf("symlink install error = %v", err)
	}
	if info, err := os.Lstat(wrapper); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("wrapper symlink changed: info=%v err=%v", info, err)
	}
	if info, err := os.Stat(target); err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("symlink target mode changed: info=%v err=%v", info, err)
	}
	if _, err := os.Stat(filepath.Join(home, ".zcode", ".roca-hooks.lock")); !os.IsNotExist(err) {
		t.Fatalf("refused symlink left runtime lock: %v", err)
	}
}

func TestZcodeHookRefusesPreexistingOperatorWrapper(t *testing.T) {
	home := t.TempDir()
	config := filepath.Join(home, "config.json")
	wrapper := filepath.Join(home, "hooks", "roca-handoff.sh")
	original := []byte("#!/bin/sh\nprintf 'operator wrapper\\n'\n")
	if err := os.MkdirAll(filepath.Dir(wrapper), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(wrapper, original, 0o640); err != nil {
		t.Fatal(err)
	}
	if _, _, err := installZcodeHandoffHook(config, wrapper, filepath.Join(home, "roca")); err == nil ||
		!strings.Contains(err.Error(), "refuse to replace operator-modified") {
		t.Fatalf("install collision error = %v", err)
	}
	body, err := os.ReadFile(wrapper)
	if err != nil || string(body) != string(original) {
		t.Fatalf("operator wrapper changed: body=%q err=%v", body, err)
	}
	if _, err := os.Stat(config); !os.IsNotExist(err) {
		t.Fatalf("refused install created config: %v", err)
	}
}

func TestZcodeUninstallRetainsWrapperWithEditedExecutable(t *testing.T) {
	home := t.TempDir()
	config := filepath.Join(home, "config.json")
	wrapper := filepath.Join(home, "hooks", "roca-handoff.sh")
	executable := filepath.Join(home, "roca")
	if _, _, err := installZcodeHandoffHook(config, wrapper, executable); err != nil {
		t.Fatal(err)
	}
	edited := []byte(zcodeWrapper(filepath.Join(home, "operator-roca")))
	if err := os.WriteFile(wrapper, edited, 0o700); err != nil {
		t.Fatal(err)
	}
	_, warning, err := uninstallZcodeHandoffHook(config, wrapper, []byte(zcodeWrapper(executable)))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(warning, "kept operator-modified") {
		t.Fatalf("edited wrapper warning = %q", warning)
	}
	if body, err := os.ReadFile(wrapper); err != nil || string(body) != string(edited) {
		t.Fatalf("edited wrapper changed: body=%q err=%v", body, err)
	}
}

func TestZcodeWrapperRemovalDoesNotCreateMissingDirectories(t *testing.T) {
	home := t.TempDir()
	wrapper := filepath.Join(home, "missing", "hooks", "roca-handoff.sh")
	retained, err := removeZcodeWrapper(wrapper, []byte(zcodeWrapper(filepath.Join(home, "roca"))))
	if err != nil || retained {
		t.Fatalf("absent wrapper removal: retained=%v err=%v", retained, err)
	}
	if _, err := os.Stat(filepath.Join(home, "missing")); !os.IsNotExist(err) {
		t.Fatalf("absent wrapper removal created directories: %v", err)
	}
}

func TestZcodeWrapperRemovalRestoresQuarantineOnFailure(t *testing.T) {
	home := t.TempDir()
	wrapper := filepath.Join(home, "roca-handoff.sh")
	expected := []byte(zcodeWrapper(filepath.Join(home, "roca")))
	if err := os.WriteFile(wrapper, expected, 0o700); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("synthetic removal failure")
	retained, err := removeZcodeWrapperQuarantine(wrapper, expected, nil,
		func(string) error { return failure })
	if !retained || !errors.Is(err, failure) {
		t.Fatalf("failed removal: retained=%v err=%v", retained, err)
	}
	body, readErr := os.ReadFile(wrapper)
	if readErr != nil || string(body) != string(expected) {
		t.Fatalf("wrapper was not restored: body=%q err=%v", body, readErr)
	}
}

func TestZcodeWrapperRemovalPreservesConcurrentReplacement(t *testing.T) {
	home := t.TempDir()
	wrapper := filepath.Join(home, "roca-handoff.sh")
	expected := []byte(zcodeWrapper(filepath.Join(home, "roca")))
	operator := []byte("#!/bin/sh\nprintf 'operator replacement\\n'\n")
	if err := os.WriteFile(wrapper, expected, 0o700); err != nil {
		t.Fatal(err)
	}
	retained, err := removeZcodeWrapperAfterQuarantine(wrapper, expected, func() {
		if writeErr := os.WriteFile(wrapper, operator, 0o700); writeErr != nil {
			t.Error(writeErr)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if !retained {
		t.Fatal("concurrent operator replacement was not reported as retained")
	}
	if body, err := os.ReadFile(wrapper); err != nil || string(body) != string(operator) {
		t.Fatalf("concurrent operator replacement changed: body=%q err=%v", body, err)
	}
}

func TestZcodeConfigRemovalPreservesConcurrentReplacement(t *testing.T) {
	home := t.TempDir()
	config := filepath.Join(home, "config.json")
	if err := os.WriteFile(config, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	operator := []byte(`{"operator":true}`)
	retained, err := removeEmptyZcodeConfigAfterQuarantine(config, func() {
		if writeErr := os.WriteFile(config, operator, 0o600); writeErr != nil {
			t.Error(writeErr)
		}
	}, os.Remove)
	if err != nil || !retained {
		t.Fatalf("concurrent replacement: retained=%v err=%v", retained, err)
	}
	if body, err := os.ReadFile(config); err != nil || string(body) != string(operator) {
		t.Fatalf("operator replacement changed: body=%q err=%v", body, err)
	}
}

func TestZcodeCleanupPrechecksDivergedArtifactBeforeQuarantine(t *testing.T) {
	config := filepath.Join(t.TempDir(), "config.json")
	operator := []byte(`{"operator":true}`)
	if err := os.WriteFile(config, operator, 0o600); err != nil {
		t.Fatal(err)
	}
	retained, err := removeOwnedZcodeArtifactWithRename(config,
		zcodeRegularFileVerifier(func(body []byte) bool { return strings.TrimSpace(string(body)) == "{}" }),
		func(string, string) error {
			t.Fatal("diverged config reached quarantine")
			return nil
		}, nil, os.Remove)
	if err != nil || !retained {
		t.Fatalf("diverged cleanup: retained=%v err=%v", retained, err)
	}
	if body, readErr := os.ReadFile(config); readErr != nil || string(body) != string(operator) {
		t.Fatalf("diverged config changed: body=%q err=%v", body, readErr)
	}
}

func TestZcodeCleanupFailsBeforeMovingWhenQuarantineUnsupported(t *testing.T) {
	config := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(config, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	unsupported := errors.New("unsupported quarantine")
	retained, err := removeOwnedZcodeArtifactWithRename(config,
		zcodeRegularFileVerifier(func(body []byte) bool { return strings.TrimSpace(string(body)) == "{}" }),
		func(string, string) error { return unsupported }, nil, os.Remove)
	if !retained || !errors.Is(err, unsupported) {
		t.Fatalf("unsupported cleanup: retained=%v err=%v", retained, err)
	}
	if body, readErr := os.ReadFile(config); readErr != nil || strings.TrimSpace(string(body)) != "{}" {
		t.Fatalf("managed config moved: body=%q err=%v", body, readErr)
	}
}

func TestZcodeWrapperInstallUsesCapturedPreimage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "roca-handoff.sh")
	if err := os.WriteFile(path, []byte("captured\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	state, err := readZcodeWrapperState(path)
	if err != nil {
		t.Fatal(err)
	}
	concurrent := []byte("operator changed this\n")
	if err := os.WriteFile(path, concurrent, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeZcodeWrapper(path, zcodeWrapper("/bin/roca"), state); err == nil {
		t.Fatal("wrapper install accepted a stale preimage")
	}
	body, err := os.ReadFile(path)
	if err != nil || string(body) != string(concurrent) {
		t.Fatalf("wrapper install changed concurrent bytes: body=%q err=%v", body, err)
	}
}

func TestZcodeWrapperInstallNormalizesManagedMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "roca-handoff.sh")
	content := zcodeWrapper("/bin/roca")
	if err := os.WriteFile(path, []byte(content), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o777); err != nil {
		t.Fatal(err)
	}
	state, err := readZcodeWrapperState(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeZcodeWrapper(path, content, state); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("managed wrapper mode = %o, want 700", info.Mode().Perm())
	}
}

func TestZcodeWrapperFreshInstallDoesNotReplaceConcurrentFile(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "hooks")
	path := filepath.Join(directory, "roca-handoff.sh")
	state, err := readZcodeWrapperState(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(directory, 0o750); err != nil {
		t.Fatal(err)
	}
	operator := []byte("#!/bin/sh\nprintf 'operator wrapper\\n'\n")
	if err := os.WriteFile(path, operator, 0o640); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(directory)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeZcodeWrapper(path, zcodeWrapper("/bin/roca"), state); err == nil {
		t.Fatal("fresh wrapper install replaced a concurrently created file")
	}
	body, err := os.ReadFile(path)
	if err != nil || string(body) != string(operator) {
		t.Fatalf("concurrent operator wrapper changed: body=%q err=%v", body, err)
	}
	after, err := os.Stat(directory)
	if err != nil {
		t.Fatal(err)
	}
	if after.Mode().Perm() != before.Mode().Perm() {
		t.Fatalf("hooks directory mode changed from %o to %o", before.Mode().Perm(), after.Mode().Perm())
	}
}

func TestFullUninstallDoesNotCreateUnselectedZcodeState(t *testing.T) {
	home := skillTestHome(t)
	report := lifecycle.Report{Purged: true, Deleted: []string{}}
	(&cliEnv{out: io.Discard, errOut: io.Discard}).withdrawTheIntegrations(&report, false)
	if _, err := os.Stat(filepath.Join(home, ".zcode")); !os.IsNotExist(err) {
		t.Fatalf("uninstall created unselected ZCode state: %v", err)
	}
}

func TestZcodeHookRunnerWrapsLatestHandoffJSON(t *testing.T) {
	skillTestHome(t)
	input := `{"project":"demo","handoffs":[]}`
	var output strings.Builder
	root := rootCommand(&cliEnv{out: &output, build: Build{Version: "test"}})
	root.SetIn(strings.NewReader(input))
	root.SetArgs([]string{"hooks", "run", "zcode"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	var context map[string]string
	if err := json.Unmarshal([]byte(output.String()), &context); err != nil {
		t.Fatalf("zcode hook runner output is not JSON: %v", err)
	}
	if context["additionalContext"] != input {
		t.Fatalf("wrapped handoff = %#v", context)
	}
}

func TestZcodeHandoffUsesProjectScopedLatestCommand(t *testing.T) {
	fixture := fixtureInstallation(t)
	projectDir := filepath.Join(fixture.home, "project-b")
	if err := os.Mkdir(projectDir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(projectDir)
	database, err := sql.Open("sqlite", filepath.Join(fixture.home, ".roca", "plugins",
		rocaops.Name, rocaops.DatabaseFilename))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO memories (layer, content, origin, project, status, created_at)
		VALUES ('handoff', 'project-a-private', 'agent', 'project-a', 'active', '9999-01-03 00:00:00')`); err != nil {
		database.Close()
		t.Fatal(err)
	}
	result, err := database.Exec(`INSERT INTO memories (layer, content, origin, project, status, created_at)
		VALUES ('handoff', 'obsolete-project-b', 'agent', 'project-b', 'active', '9999-01-01 00:00:00')`)
	if err != nil {
		database.Close()
		t.Fatal(err)
	}
	obsolete, err := result.LastInsertId()
	if err != nil {
		database.Close()
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO memories (layer, content, origin, project, status, created_at, supersedes)
		VALUES ('handoff', 'current-project-b', 'agent', 'project-b', 'active', '9999-01-02 00:00:00', ?)`, obsolete); err != nil {
		database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	env := &cliEnv{out: &output, build: Build{Version: "test"}}
	root := rootCommand(env)
	root.SetArgs([]string{"handoff", "latest", "--json"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	var handoffs service.HandoffList
	if err := json.Unmarshal([]byte(output.String()), &handoffs); err != nil {
		t.Fatal(err)
	}
	if handoffs.Project != "project-b" || len(handoffs.Handoffs) != 1 ||
		handoffs.Handoffs[0].Content != "current-project-b" {
		t.Fatalf("project handoffs = %+v", handoffs)
	}
}

func TestZcodeHookInstallRollsBackWrapperWhenConfigEditFails(t *testing.T) {
	for _, test := range []struct {
		name, previous string
	}{
		{name: "new wrapper"},
		{name: "existing wrapper", previous: "operator wrapper\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			config := filepath.Join(home, "config.json")
			wrapper := filepath.Join(home, "hooks", "roca-handoff.sh")
			if err := os.WriteFile(config, []byte(`{"hooks":[]}`), 0o600); err != nil {
				t.Fatal(err)
			}
			if test.previous != "" {
				if err := os.MkdirAll(filepath.Dir(wrapper), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(wrapper, []byte(test.previous), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if _, _, err := installZcodeHandoffHook(config, wrapper, filepath.Join(home, "roca")); err == nil {
				t.Fatal("install accepted a non-object hooks setting")
			}
			body, err := os.ReadFile(wrapper)
			if test.previous == "" {
				if !os.IsNotExist(err) {
					t.Fatalf("failed install left a wrapper: %v", err)
				}
				return
			}
			if err != nil || string(body) != test.previous {
				t.Fatalf("failed install did not restore wrapper: body=%q err=%v", body, err)
			}
		})
	}
}

func TestZcodeWrapperRollbackPreservesConcurrentChanges(t *testing.T) {
	installed := []byte("installed wrapper\n")
	for _, test := range []struct {
		name  string
		state zcodeWrapperState
	}{
		{name: "new wrapper"},
		{name: "replaced wrapper", state: zcodeWrapperState{
			body: []byte("previous wrapper\n"), mode: 0o600, exists: true,
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "roca-handoff.sh")
			if err := os.WriteFile(path, installed, 0o700); err != nil {
				t.Fatal(err)
			}
			concurrent := []byte("operator changed this\n")
			if err := os.WriteFile(path, concurrent, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := restoreZcodeWrapper(path, test.state, installed); err == nil {
				t.Fatal("rollback accepted concurrently changed wrapper bytes")
			}
			body, err := os.ReadFile(path)
			if err != nil || string(body) != string(concurrent) {
				t.Fatalf("rollback clobbered concurrent bytes: body=%q err=%v", body, err)
			}
		})
	}
}

func TestClaudeHookSignsRocaStoreFromTheTranscriptIdentity(t *testing.T) {
	transcript := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(transcript, []byte(
		`{"message":{"role":"assistant","model":"claude-sonnet-4-6"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name       string
		command    string
		transcript string
		want       []string
	}{
		{"detected model", "roca store --layer handoff --content note", transcript, []string{"--agent claude", "--model 'claude-sonnet-4-6'"}},
		{"unknown model", "roca store --layer handoff --content note", filepath.Join(t.TempDir(), "missing"), []string{"--agent claude", "--model 'unknown'"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input, _ := json.Marshal(map[string]any{
				"hook_event_name": "PreToolUse", "tool_name": "Bash",
				"transcript_path": test.transcript,
				"tool_input":      map[string]any{"command": test.command, "timeout": 5000},
			})
			output, err := runClaudeAuthorshipHook(input)
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range test.want {
				if !strings.Contains(string(output), want) {
					t.Errorf("hook output does not contain %q: %s", want, output)
				}
			}
			if strings.Count(string(output), "--agent") != 1 || strings.Count(string(output), "--model") != 1 {
				t.Errorf("hook duplicated identity flags: %s", output)
			}
		})
	}
	untouched := []string{
		"roca store --agent claude --model opus --layer handoff --content note",
		// A separator inside a quoted value is text: cutting the segment there
		// hid the explicit flags that follow it and duplicated them.
		`roca store --content "a || b" --agent codex --model gpt-5`,
		`echo 'roca store --layer handoff'`,
	}
	for _, command := range untouched {
		if signed := signRocaStoreCommand(command, "sonnet"); signed != command {
			t.Errorf("hook rewrote a command it should have left alone: %s", signed)
		}
	}
	command := `roca store --layer handoff --content '--agent is documentation'`
	if signed := signRocaStoreCommand(command, "sonnet"); !strings.Contains(signed, "--agent claude") {
		t.Errorf("quoted content hid the missing agent flag: %s", signed)
	}
	command = `roca store --content "a || b" --layer handoff && echo done`
	signed := signRocaStoreCommand(command, "sonnet")
	if strings.Count(signed, "--agent") != 1 || strings.Count(signed, "--model") != 1 {
		t.Errorf("hook duplicated identity flags across a quoted separator: %s", signed)
	}
}

func TestClaudeHookInstallerPreservesSettingsAndIsIdempotent(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "settings.json")
	binary := filepath.Join(home, "bin", "roca")
	initial := `{"permissions":{"allow":["Read"]},"hooks":{"PreToolUse":[{"matcher":"Write","hooks":[]}]}}`
	if err := os.WriteFile(path, []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}
	for attempt, wantChanged := range []bool{true, false} {
		outcome, err := installClaudeAuthorshipHook(path, binary)
		if err != nil {
			t.Fatal(err)
		}
		if outcome.Changed != wantChanged {
			t.Errorf("attempt %d changed = %v, want %v", attempt+1, outcome.Changed, wantChanged)
		}
		if attempt == 0 && outcome.Backup == "" {
			t.Error("installer replaced settings without a recovery backup")
		}
	}
	body := readSettings(t, path)
	// The hook runs in Claude's non-interactive shell, where a bare `roca` is
	// whatever PATH happens to hold, so the entry names this binary in full.
	if !strings.Contains(body, `"permissions"`) ||
		strings.Count(body, shellQuote(binary)+" hooks run claude") != 1 {
		t.Errorf("installer lost existing settings or did not name the binary once: %s", body)
	}

	moved := filepath.Join(home, "opt", "roca")
	if _, err := installClaudeAuthorshipHook(path, moved); err != nil {
		t.Fatal(err)
	}
	body = readSettings(t, path)
	if strings.Contains(body, binary) ||
		strings.Count(body, shellQuote(moved)+" hooks run claude") != 1 {
		t.Errorf("reinstall left the hook pointing at a binary that moved: %s", body)
	}

	outcome, warning, err := uninstallClaudeAuthorshipHook(path)
	if err != nil {
		t.Fatal(err)
	}
	if !outcome.Changed || warning != "" {
		t.Errorf("uninstall left the signing hook behind or warned about its own file: %q", warning)
	}
	body = readSettings(t, path)
	if strings.Contains(body, "hooks run claude") || !strings.Contains(body, `"permissions"`) ||
		!strings.Contains(body, `"Write"`) {
		t.Errorf("uninstall did not withdraw exactly its own hook: %s", body)
	}
	if again, _, err := uninstallClaudeAuthorshipHook(path); err != nil || again.Changed {
		t.Errorf("uninstall is not idempotent: changed=%v err=%v", again.Changed, err)
	}

	root := rootCommand(&cliEnv{})
	for _, verb := range []string{"install", "uninstall"} {
		command, _, err := root.Find([]string{"hooks", verb, "claude"})
		if err != nil || command == nil {
			t.Fatalf("roca hooks %s claude is unavailable: %v", verb, err)
		}
	}
}

func TestHookCommandRegistersTheOwnedClaudeFragment(t *testing.T) {
	home := skillTestHome(t)
	binary := filepath.Join(home, "bin", "roca")
	t.Setenv(EnvExecutable, binary)
	var output strings.Builder
	root := rootCommand(&cliEnv{out: &output, build: Build{Version: "v1.2.3"}})
	root.SetArgs([]string{"hooks", "install", "claude"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	settings := filepath.Join(home, ".claude", "settings.json")
	registry, err := artifact.LoadRegistry(filepath.Join(home, ".roca", "artifacts.json"))
	if err != nil {
		t.Fatal(err)
	}
	entry, ok := registry.Find("hook", "claude", settings)
	if !ok || entry.InstalledVersion != "v1.2.3" || entry.SystemSHA256 == "" {
		t.Fatalf("registered hook = %+v, found %v", entry, ok)
	}

	body, err := os.ReadFile(settings)
	if err != nil {
		t.Fatal(err)
	}
	operatorBinary := filepath.Join(home, "operator", "roca")
	edited := strings.Replace(string(body), claudeHookCommand(binary),
		claudeHookCommand(operatorBinary), 1)
	if err := os.WriteFile(settings, []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}
	var warning strings.Builder
	root = rootCommand(&cliEnv{out: &output, errOut: &warning, build: Build{Version: "v1.2.3"}})
	root.SetArgs([]string{"hooks", "install", "claude"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if got := readSettings(t, settings); !strings.Contains(got, operatorBinary) ||
		!strings.Contains(warning.String(), "hooks install claude --force") {
		t.Fatalf("diverged hook was overwritten or not warned: body=%s warning=%q", got, warning.String())
	}

	root = rootCommand(&cliEnv{out: &output, build: Build{Version: "v1.2.3"}})
	root.SetArgs([]string{"hooks", "install", "claude", "--force"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if got := readSettings(t, settings); strings.Contains(got, operatorBinary) || !strings.Contains(got, binary) {
		t.Fatalf("forced hook refresh did not restore SYSTEM: %s", got)
	}

	root = rootCommand(&cliEnv{out: &output, build: Build{Version: "v1.2.3"}})
	root.SetArgs([]string{"hooks", "uninstall", "claude"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	registry, err = artifact.LoadRegistry(filepath.Join(home, ".roca", "artifacts.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := registry.Find("hook", "claude", settings); ok {
		t.Fatal("explicit hook uninstall left the artifact registered")
	}
}

// The refresh rewrites the bytes of the command it registered and no others:
// the operator's numeric spelling, spacing and trailing members survive. The
// reader only ever looks inside PreToolUse, so the edit looks there too, and an
// operator who declared the identical command under another event owns those
// bytes: they are neither rewritten nor a reason to refuse the refresh.
func TestHookRefreshChangesOnlyItsRegisteredCommandBytes(t *testing.T) {
	home := t.TempDir()
	oldBinary := filepath.Join(home, "old", "roca")
	newBinary := filepath.Join(home, "new", "roca")
	oldCommand := encodedJSONString(t, claudeHookCommand(oldBinary))
	entry := `[{"matcher":"Bash","hooks":[{"type":"command","command":` + oldCommand + `}]}]`
	for _, test := range []struct{ name, previous string }{
		{"operator bytes around the entry",
			`{"numeric_spelling":1e3,"hooks":{"PreToolUse":` + entry + `},"tail":"  keep  "}`},
		{"the same command under another event",
			`{"hooks":{"PreToolUse":` + entry + `,"PostToolUse":` + entry + `}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "settings.json")
			if err := os.WriteFile(path, []byte(test.previous), 0o640); err != nil {
				t.Fatal(err)
			}
			system, found, err := claudeHookSystem(path)
			if err != nil || !found {
				t.Fatalf("read installed hook: found=%v err=%v", found, err)
			}
			outcome, err := refreshClaudeHook(path, newBinary, artifact.Checksum(system), true, false)
			if err != nil {
				t.Fatalf("an operator's own bytes blocked the refresh: %v", err)
			}
			want := strings.Replace(test.previous, oldCommand,
				encodedJSONString(t, claudeHookCommand(newBinary)), 1)
			if got := readSettings(t, path); !outcome.Changed || got != want {
				t.Fatalf("refresh did not edit exactly its own registered command:\nwant %s\n got %s", want, got)
			}
		})
	}
}

// With automatic refresh off, update records and reports outdated installs and
// mutates nothing — including under --force-artifacts. Clearing the divergence
// on that path made an edited hook fragment read as merely outdated and left it
// unnamed, while a zoned file under the same two flags still named itself.
func TestADisabledHookRefreshStillReportsDivergence(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "settings.json")
	previous := `{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":` +
		encodedJSONString(t, claudeHookCommand(filepath.Join(home, "operator", "roca"))) + `}]}]}}`
	if err := os.WriteFile(path, []byte(previous), 0o600); err != nil {
		t.Fatal(err)
	}
	registered := artifact.Checksum(`{"command":"what we installed","type":"command"}`)
	out, err := refreshClaudeHook(path, filepath.Join(home, "bin", "roca"), registered, false, true)
	if err != nil {
		t.Fatal(err)
	}
	if !out.Diverged || out.Changed {
		t.Fatalf("a forced refresh behind the off gate = %+v", out)
	}
	if got := readSettings(t, path); got != previous {
		t.Fatalf("a disabled refresh edited the settings: %s", got)
	}
}

func encodedJSONString(t *testing.T, value string) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

// Settings La Roca did not write are two different questions. Installing into
// them is refused, because an installer that cannot read a file cannot edit it
// safely. Withdrawing from them is not: an operator removing this product must
// never be held hostage by a file the product never owned, so the withdrawal
// changes nothing, succeeds, and names what to take out by hand.
func TestMalformedClaudeSettingsRefuseInstallAndNeverBlockWithdrawal(t *testing.T) {
	for _, test := range []struct {
		name, body        string
		sessionUnreadable bool
	}{
		{"settings are not JSON", "{not json", true},
		{"hooks is not an object", `{"hooks":"none"}`, true},
		{"PreToolUse is not an array", `{"hooks":{"PreToolUse":"none"}}`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := skillTestHome(t)
			path := filepath.Join(home, ".claude", "settings.json")
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(test.body), 0o600); err != nil {
				t.Fatal(err)
			}

			if _, err := installClaudeAuthorshipHook(path, filepath.Join(home, "bin", "roca")); err == nil {
				t.Fatal("install edited settings it could not read")
			}

			var out, errOut strings.Builder
			env := &cliEnv{out: &out, errOut: &errOut}
			root := rootCommand(env)
			root.SetArgs([]string{"hooks", "uninstall", "claude"})
			if err := root.Execute(); err != nil {
				t.Fatalf("withdrawal refused to run over foreign settings: %v", err)
			}
			assertClaudeWithdrawalWarning(t, errOut.String(), path)

			errOut.Reset()
			report := lifecycle.Report{Purged: true, Deleted: []string{}}
			env.withdrawTheIntegrations(&report, false)
			assertClaudeProductWithdrawalWarnings(t, errOut.String(), path, test.sessionUnreadable)
			for _, failure := range report.Errors {
				if strings.Contains(failure, "signing hook") {
					t.Errorf("foreign settings blocked the uninstall: %s", failure)
				}
			}

			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(body) != test.body {
				t.Errorf("withdrawal rewrote settings it could not read: %s", body)
			}
		})
	}
}

func assertClaudeWithdrawalWarning(t *testing.T, warned, path string) {
	t.Helper()
	if count := strings.Count(warned, "warning:"); count != 1 {
		t.Fatalf("want exactly one warning line, got %d: %q", count, warned)
	}
	if !strings.Contains(warned, path) || !strings.Contains(warned, "hooks run claude") ||
		!strings.Contains(warned, "PreToolUse") {
		t.Fatalf("the warning does not name the file and the entry to remove: %q", warned)
	}
}

func assertClaudeProductWithdrawalWarnings(t *testing.T, warned, path string, sessionUnreadable bool) {
	t.Helper()
	if !strings.Contains(warned, path) || !strings.Contains(warned, "PreToolUse") {
		t.Fatalf("product withdrawal did not name the unreadable signing hook: %q", warned)
	}
	if sessionUnreadable {
		for _, marker := range []string{"hooks.SessionStart", "hooks run claude-pills", "hooks run claude-handoff"} {
			if !strings.Contains(warned, marker) {
				t.Fatalf("product withdrawal warning does not name %q: %q", marker, warned)
			}
		}
	}
}

func TestProductUninstallWithdrawsSessionHooksWhenPreToolUseIsUnreadable(t *testing.T) {
	home := skillTestHome(t)
	path := filepath.Join(home, ".claude", "settings.json")
	binary := filepath.Join(home, "O'Brien Tools", "roca")
	settings := map[string]any{"hooks": map[string]any{
		"PreToolUse": "unreadable",
		"SessionStart": []any{
			map[string]any{"hooks": []any{map[string]any{"type": "command", "command": claudePillsHookCommand(binary)}}},
			map[string]any{"hooks": []any{map[string]any{"type": "command", "command": claudeHandoffHookCommand(binary)}}},
		},
	}}
	encoded, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, string(encoded))

	var out, errOut strings.Builder
	env := &cliEnv{out: &out, errOut: &errOut}
	report := lifecycle.Report{Purged: true, Deleted: []string{}}
	env.withdrawTheIntegrations(&report, false)
	groups := readClaudeSessionStartHooks(t, path)
	assertHookCommand(t, groups, "", claudePillsHookCommand(binary), 0)
	assertHookCommand(t, groups, "", claudeHandoffHookCommand(binary), 0)
	if !strings.Contains(errOut.String(), "PreToolUse") {
		t.Fatalf("product uninstall did not warn about the unreadable signing hook: %q", errOut.String())
	}
}

func TestSessionStartHooksInstallAndUninstallAreIdempotent(t *testing.T) {
	home := skillTestHome(t)
	binary := filepath.Join(home, "O'Brien Tools", "roca")
	t.Setenv(EnvExecutable, binary)
	path := filepath.Join(home, ".claude", "settings.json")
	foreign := "/opt/acme pill"
	writeFile(t, path,
		`{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"/opt/acme pill"}]}]}}`)

	pillsCommand := claudePillsHookCommand(binary)
	handoffCommand := claudeHandoffHookCommand(binary)
	var output strings.Builder
	for range 2 {
		runHookCLI(t, &output, nil, "install", "claude", "--pills", "--handoff")
		settings := readClaudeHookSettings(t, path)
		assertHookCommand(t, settings.Hooks["SessionStart"], "", foreign, 1)
		assertHookCommand(t, settings.Hooks["SessionStart"], "", pillsCommand, 1)
		assertHookCommand(t, settings.Hooks["SessionStart"], "", handoffCommand, 1)
		assertHookCommand(t, settings.Hooks["PreToolUse"], "Bash", claudeHookCommand(binary), 0)
	}
	registry, err := artifact.LoadRegistry(filepath.Join(home, ".roca", "artifacts.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, found := registry.Find("hook", "claude", path); found {
		t.Fatal("session-hook install registered the signing hook")
	}

	runHookCLI(t, &output, nil, "uninstall", "claude", "--pills", "--handoff")
	settings := readClaudeHookSettings(t, path)
	assertHookCommand(t, settings.Hooks["SessionStart"], "", foreign, 1)
	assertHookCommand(t, settings.Hooks["SessionStart"], "", pillsCommand, 0)
	assertHookCommand(t, settings.Hooks["SessionStart"], "", handoffCommand, 0)
	assertHookCommand(t, settings.Hooks["PreToolUse"], "Bash", claudeHookCommand(binary), 0)

	runHookCLI(t, &output, nil, "uninstall", "claude", "--pills", "--handoff")
	runHookCLI(t, &output, nil, "install", "claude")
	settings = readClaudeHookSettings(t, path)
	assertHookCommand(t, settings.Hooks["SessionStart"], "", pillsCommand, 0)
	assertHookCommand(t, settings.Hooks["SessionStart"], "", handoffCommand, 0)
	assertHookCommand(t, settings.Hooks["PreToolUse"], "Bash", claudeHookCommand(binary), 1)
}

func TestSessionHookInstallDoesNotInspectDivergedSigningHook(t *testing.T) {
	home := skillTestHome(t)
	binary := filepath.Join(home, "bin", "roca")
	t.Setenv(EnvExecutable, binary)
	path := filepath.Join(home, ".claude", "settings.json")
	var output strings.Builder
	runHookCLI(t, &output, nil, "install", "claude")
	operatorCommand := claudeHookCommand(filepath.Join(home, "operator", "roca"))
	body := readSettings(t, path)
	body = strings.Replace(body, claudeHookCommand(binary), operatorCommand, 1)
	writeFile(t, path, body)

	var warning strings.Builder
	runHookCLI(t, &output, &warning, "install", "claude", "--pills", "--handoff")
	settings := readClaudeHookSettings(t, path)
	assertHookCommand(t, settings.Hooks["PreToolUse"], "Bash", operatorCommand, 1)
	assertHookCommand(t, settings.Hooks["SessionStart"], "", claudePillsHookCommand(binary), 1)
	assertHookCommand(t, settings.Hooks["SessionStart"], "", claudeHandoffHookCommand(binary), 1)
	if warning.String() != "" {
		t.Fatalf("session-hook install inspected the signing hook: %q", warning.String())
	}
}

func TestSessionHookInstallIgnoresMalformedPreToolUse(t *testing.T) {
	home := skillTestHome(t)
	binary := filepath.Join(home, "bin", "roca")
	t.Setenv(EnvExecutable, binary)
	path := filepath.Join(home, ".claude", "settings.json")
	writeFile(t, path, `{"hooks":{"PreToolUse":"operator-owned"}}`)

	var output strings.Builder
	runHookCLI(t, &output, nil, "install", "claude", "--pills", "--handoff")
	groups := readClaudeSessionStartHooks(t, path)
	assertHookCommand(t, groups, "", claudePillsHookCommand(binary), 1)
	assertHookCommand(t, groups, "", claudeHandoffHookCommand(binary), 1)
	if got := readClaudeHookValue(t, path, "PreToolUse"); got != "operator-owned" {
		t.Fatalf("session-hook install changed PreToolUse: %#v", got)
	}
}

func TestCombinedSessionUninstallReportsBothOwnedMarkers(t *testing.T) {
	home := skillTestHome(t)
	path := filepath.Join(home, ".claude", "settings.json")
	writeFile(t, path, `{"hooks":{"SessionStart":"operator-owned"}}`)

	var output, warning strings.Builder
	runHookCLI(t, &output, &warning, "uninstall", "claude", "--pills", "--handoff")
	for _, marker := range []string{"hooks run claude-pills", "hooks run claude-handoff"} {
		if !strings.Contains(warning.String(), marker) {
			t.Fatalf("combined uninstall warning omitted %q: %q", marker, warning.String())
		}
	}
	if got := readClaudeHookValue(t, path, "SessionStart"); got != "operator-owned" {
		t.Fatalf("uninstall changed unreadable SessionStart settings: %#v", got)
	}
}

func runHookCLI(t *testing.T, output, warnings *strings.Builder, args ...string) {
	t.Helper()
	root := rootCommand(&cliEnv{
		out: output, errOut: warnings, build: Build{Version: "v1.2.3"},
	})
	root.SetArgs(append([]string{"hooks"}, args...))
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
}

type claudeHookSettingsModel struct {
	Hooks map[string][]claudeHookGroup `json:"hooks"`
}

type claudeHookGroup struct {
	Matcher string              `json:"matcher"`
	Hooks   []claudeCommandHook `json:"hooks"`
}

type claudeCommandHook struct {
	Type    string `json:"type"`
	Command string `json:"command"`
}

func readClaudeSessionStartHooks(t *testing.T, path string) []claudeHookGroup {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var settings struct {
		Hooks struct {
			SessionStart []claudeHookGroup `json:"SessionStart"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(body, &settings); err != nil {
		t.Fatalf("settings are no longer valid Claude SessionStart settings: %v", err)
	}
	return settings.Hooks.SessionStart
}

func readClaudeHookValue(t *testing.T, path, event string) any {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var settings struct {
		Hooks map[string]any `json:"hooks"`
	}
	if err := json.Unmarshal(body, &settings); err != nil {
		t.Fatalf("settings are no longer valid Claude hook settings: %v", err)
	}
	return settings.Hooks[event]
}

func readClaudeHookSettings(t *testing.T, path string) claudeHookSettingsModel {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var settings claudeHookSettingsModel
	if err := json.Unmarshal(body, &settings); err != nil {
		t.Fatalf("settings are no longer valid Claude hook settings: %v", err)
	}
	return settings
}

func assertHookCommand(t *testing.T, groups []claudeHookGroup, matcher, command string, want int) {
	t.Helper()
	got := 0
	for _, group := range groups {
		if group.Matcher != matcher {
			continue
		}
		for _, hook := range group.Hooks {
			if hook.Type == "command" && hook.Command == command {
				got++
			}
		}
	}
	if got != want {
		t.Fatalf("hook command %q with matcher %q occurs %d times, want %d: %+v", command, matcher, got, want, groups)
	}
}

func readSettings(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var settings map[string]any
	if err := json.Unmarshal(body, &settings); err != nil {
		t.Fatalf("settings are no longer JSON: %v", err)
	}
	return string(body)
}
