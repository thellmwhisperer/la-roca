package cli

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thellmwhisperer/la-roca/internal/artifact"
	"github.com/thellmwhisperer/la-roca/internal/distribution/agentcfg"
	"github.com/thellmwhisperer/la-roca/internal/distribution/lifecycle"
)

// `--config` names ONE runtime's configuration file. Applied to every runtime it
// edited a single file once per runtime, each pass with a different agent's
// rules: one agent's configuration rewritten by another agent's editor.
func TestAConfigPathIsRefusedForMoreThanOneRuntime(t *testing.T) {
	for _, args := range [][]string{
		{"mcp", "uninstall", "--all", "--config", "/tmp/not-read.json"},
		{"mcp", "status", "--config", "/tmp/not-read.json"},
	} {
		err := failingRoot(t, args...)
		if !strings.Contains(err.Error(), "one runtime") {
			t.Errorf("roca %v: the refusal does not say what to do instead: %v", args, err)
		}
	}
}

// Naming the one runtime it belongs to still works.
func TestZcodeMCPStateRestoresContainerPreimage(t *testing.T) {
	home := skillTestHome(t)
	path := filepath.Join(home, "operator", "config.json")
	before := `{"mcp":{}}`
	writeFile(t, path, before)
	runZcodeTestCLI(t, "mcp", "install", "zcode", "--config", path, "--executable", filepath.Join(home, "roca"))
	var document map[string]any
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(body, &document); err != nil {
		t.Fatal(err)
	}
	entry := document["mcp"].(map[string]any)["servers"].(map[string]any)["roca"].(map[string]any)
	if len(entry) != 3 || entry["type"] != "stdio" {
		t.Fatalf("ZCode MCP entry = %#v", entry)
	}
	registry, err := artifact.LoadRegistry(filepath.Join(home, ".roca", "artifacts.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, found := registry.Find(artifactKindMCP, "zcode", path); !found {
		t.Fatal("ZCode MCP preimage was not recorded in La Roca state")
	}
	runZcodeTestCLI(t, "mcp", "uninstall", "zcode", "--config", path)
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != before {
		t.Fatalf("ZCode MCP preimage changed: want %s, got %s", before, after)
	}
}

func TestZcodeMCPReinstallMergesNewPathOwnership(t *testing.T) {
	home := skillTestHome(t)
	rootPath := filepath.Join(home, ".zcode")
	config := filepath.Join(rootPath, "cli", "config.json")
	writeFile(t, config, `{"operator":true}`)
	installZcodeTestIntegration(t, "mcp", home)
	if err := os.RemoveAll(rootPath); err != nil {
		t.Fatal(err)
	}
	installZcodeTestIntegration(t, "mcp", home)
	report := purgeZcodeTestIntegrations(true)
	if len(report.Errors) != 0 {
		t.Fatalf("purge errors = %v", report.Errors)
	}
	if _, err := os.Stat(rootPath); !os.IsNotExist(err) {
		t.Fatalf("reinstall-created MCP paths survived purge: %v", err)
	}
}

func TestZcodeMCPReinstallExpiresOwnershipWithoutManagedContinuity(t *testing.T) {
	assertZcodeOwnershipExpiresWithoutContinuity(t, "mcp")
}

func TestZcodeMCPReinstallPreservesOwnershipAfterDeclarationRemoval(t *testing.T) {
	home := skillTestHome(t)
	rootPath := filepath.Join(home, ".zcode")
	config := filepath.Join(rootPath, "cli", "config.json")
	installZcodeTestIntegration(t, "mcp", home)
	writeFile(t, config, "{}")
	installZcodeTestIntegration(t, "mcp", home)
	registry, err := artifact.LoadRegistry(filepath.Join(home, ".roca", "artifacts.json"))
	if err != nil {
		t.Fatal(err)
	}
	entry, found := registry.Find(artifactKindMCP, agentcfg.RuntimeZcode, config)
	if !found || !entry.CreatedRoot || !entry.CreatedConfigDir || !entry.CreatedConfig {
		t.Fatalf("continuous MCP ownership = %#v, found=%v", entry, found)
	}
	report := purgeZcodeTestIntegrations(true)
	if len(report.Errors) != 0 {
		t.Fatalf("purge errors = %v", report.Errors)
	}
	if _, err := os.Stat(rootPath); !os.IsNotExist(err) {
		t.Fatalf("continuously owned MCP tree survived purge: %v", err)
	}
}

func TestZcodeMCPUninstallReconcilesBrokenRootIdentity(t *testing.T) {
	for _, test := range []struct {
		name          string
		recreateRoot  bool
		expectWarning bool
	}{
		{name: "absent", expectWarning: true},
		{name: "recreated", recreateRoot: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := skillTestHome(t)
			rootPath := filepath.Join(home, ".zcode")
			config := filepath.Join(rootPath, "cli", "config.json")
			installZcodeTestIntegration(t, "mcp", home)
			if err := os.RemoveAll(rootPath); err != nil {
				t.Fatal(err)
			}
			if test.recreateRoot {
				if err := os.Mkdir(rootPath, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			var errOut strings.Builder
			env := &cliEnv{out: io.Discard, errOut: &errOut}
			root := rootCommand(env)
			root.SetArgs([]string{"mcp", "uninstall", "zcode"})
			if err := root.Execute(); err != nil {
				t.Fatal(err)
			}
			warned := strings.Contains(errOut.String(), "root is absent")
			if warned != test.expectWarning {
				t.Fatalf("root warning = %q", errOut.String())
			}
			registry, err := artifact.LoadRegistry(filepath.Join(home, ".roca", "artifacts.json"))
			if err != nil {
				t.Fatal(err)
			}
			if _, found := registry.Find(artifactKindMCP, agentcfg.RuntimeZcode, config); found {
				t.Fatal("broken root identity retained stale MCP ownership")
			}
		})
	}
}

func TestZcodeMCPPurgeDropsOwnershipOnRecreatedRoot(t *testing.T) {
	home := skillTestHome(t)
	rootPath := filepath.Join(home, ".zcode")
	config := filepath.Join(rootPath, "cli", "config.json")
	installZcodeTestIntegration(t, "mcp", home)
	if err := os.RemoveAll(rootPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(rootPath, 0o700); err != nil {
		t.Fatal(err)
	}
	report := purgeZcodeTestIntegrations(true)
	if len(report.Errors) != 0 {
		t.Fatalf("recreated root produced purge errors: %v", report.Errors)
	}
	registry, err := artifact.LoadRegistry(filepath.Join(home, ".roca", "artifacts.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, found := registry.Find(artifactKindMCP, agentcfg.RuntimeZcode, config); found {
		t.Fatal("recreated root retained stale MCP ownership")
	}
	if info, err := os.Stat(rootPath); err != nil || !info.IsDir() {
		t.Fatalf("replacement root was not preserved: info=%v err=%v", info, err)
	}
}

func TestZcodeMCPReinstallDropsOwnershipOnRecreatedManagedTree(t *testing.T) {
	home := skillTestHome(t)
	rootPath := filepath.Join(home, ".zcode")
	config := filepath.Join(rootPath, "cli", "config.json")
	installZcodeTestIntegration(t, "mcp", home)
	managed, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(rootPath); err != nil {
		t.Fatal(err)
	}
	writeFile(t, config, string(managed))
	installZcodeTestIntegration(t, "mcp", home)
	registry, err := artifact.LoadRegistry(filepath.Join(home, ".roca", "artifacts.json"))
	if err != nil {
		t.Fatal(err)
	}
	entry, found := registry.Find(artifactKindMCP, agentcfg.RuntimeZcode, config)
	if !found || entry.CreatedRoot || entry.CreatedConfigDir || entry.CreatedConfig {
		t.Fatalf("recreated MCP path ownership = %#v, found=%v", entry, found)
	}
	purgeZcodeTestIntegrations(true)
	matched, err := agentcfg.ZcodeMCPMatches(config, filepath.Join(home, "roca"))
	if err != nil || matched {
		t.Fatalf("operator-recreated MCP declaration survived: matched=%v err=%v", matched, err)
	}
	if _, err := os.Stat(config); err != nil {
		t.Fatalf("operator-recreated MCP config was removed: %v", err)
	}
}

func TestZcodeMCPPersistsAbsoluteConfigPath(t *testing.T) {
	home := skillTestHome(t)
	first := t.TempDir()
	t.Chdir(first)
	before := `{"mcp":{}}`
	if err := os.WriteFile("zcode.json", []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}
	runZcodeTestCLI(t, "mcp", "install", "zcode", "--config", "zcode.json", "--executable", filepath.Join(home, "roca"))
	absolute := filepath.Join(first, "zcode.json")
	registry, err := artifact.LoadRegistry(filepath.Join(home, ".roca", "artifacts.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, found := registry.Find(artifactKindMCP, agentcfg.RuntimeZcode, absolute); !found {
		t.Fatalf("absolute MCP ownership path not recorded: %s", absolute)
	}
	second := t.TempDir()
	t.Chdir(second)
	report := lifecycle.Report{Purged: true}
	(&cliEnv{out: io.Discard, errOut: io.Discard}).withdrawTheIntegrations(&report, false)
	if len(report.Errors) != 0 {
		t.Fatalf("full uninstall errors = %v", report.Errors)
	}
	body, err := os.ReadFile(absolute)
	if err != nil || string(body) != before {
		t.Fatalf("original relative config was not restored: body=%q err=%v", body, err)
	}
	if _, err := os.Stat(filepath.Join(second, "zcode.json")); !os.IsNotExist(err) {
		t.Fatalf("uninstall targeted the new working directory: %v", err)
	}
}

func TestZcodeMCPStateRollbackPreservesConcurrentRegistryEntries(t *testing.T) {
	home := skillTestHome(t)
	env := &cliEnv{build: Build{Version: "test"}}
	path := filepath.Join(home, "zcode.json")
	rollback, err := env.recordZcodeMCPPreimage(path, "/bin/roca", agentcfg.ZcodeMCPPreimageMCPServers, false)
	if err != nil {
		t.Fatal(err)
	}
	registryPath := filepath.Join(home, ".roca", "artifacts.json")
	registry, err := artifact.LoadRegistry(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	skillEntry := artifact.Entry{Kind: artifactKindSkill, Runtime: "claude", Path: filepath.Join(home, "skill.md")}
	registry.Upsert(skillEntry)
	if err := artifact.SaveRegistry(registryPath, registry); err != nil {
		t.Fatal(err)
	}
	if err := rollback(); err != nil {
		t.Fatal(err)
	}
	registry, err = artifact.LoadRegistry(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, found := registry.Find(skillEntry.Kind, skillEntry.Runtime, skillEntry.Path); !found {
		t.Fatal("MCP rollback removed a concurrent skill ownership entry")
	}
	if _, found := registry.Find(artifactKindMCP, "zcode", path); found {
		t.Fatal("MCP rollback retained its own ownership entry")
	}
}

func TestFullUninstallWithdrawsRegisteredCustomZcodeMCPPath(t *testing.T) {
	home := skillTestHome(t)
	path := filepath.Join(home, "custom", "zcode.json")
	before := `{"mcp":{}}`
	writeFile(t, path, before)
	runZcodeTestCLI(t, "mcp", "install", "zcode", "--config", path, "--executable", filepath.Join(home, "roca"))
	report := lifecycle.Report{Purged: true}
	(&cliEnv{out: io.Discard, errOut: io.Discard, build: Build{Version: "test"}}).withdrawTheIntegrations(&report, false)
	if len(report.Errors) != 0 {
		t.Fatalf("full uninstall errors = %v", report.Errors)
	}
	if body, err := os.ReadFile(path); err != nil || string(body) != before {
		t.Fatalf("custom ZCode MCP config changed: body=%q err=%v", body, err)
	}
	registry, err := artifact.LoadRegistry(filepath.Join(home, ".roca", "artifacts.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, found := registry.Find(artifactKindMCP, "zcode", path); found {
		t.Fatal("full uninstall retained custom ZCode MCP ownership state")
	}
}

func TestFullUninstallSkipsZcodeMCPWhenOwnershipStateIsUnreadable(t *testing.T) {
	home := skillTestHome(t)
	path := filepath.Join(home, ".zcode", "cli", "config.json")
	if _, err := agentcfg.Install(agentcfg.RuntimeZcode, path, filepath.Join(home, "roca")); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	registryPath := filepath.Join(home, ".roca", "artifacts.json")
	if err := os.MkdirAll(filepath.Dir(registryPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(registryPath, []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	report := lifecycle.Report{Purged: true}
	(&cliEnv{out: io.Discard, errOut: io.Discard}).withdrawTheIntegrations(&report, false)
	if len(report.Errors) == 0 {
		t.Fatal("full uninstall accepted unreadable ownership state")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("ZCode MCP changed without ownership state:\nwant %s\n got %s", before, after)
	}
}

func TestFullUninstallRetainsBinaryWhenZcodeWithdrawalFails(t *testing.T) {
	home := skillTestHome(t)
	config := filepath.Join(home, ".zcode", "cli", "config.json")
	installZcodeTestIntegration(t, "mcp", home)
	registryPath := filepath.Join(home, ".roca", "artifacts.json")
	if err := os.WriteFile(registryPath, []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	env := &cliEnv{out: &output, errOut: &output}
	if err := env.uninstall(uninstallCommand(env), strings.NewReader(""), false); err != nil {
		t.Fatal(err)
	}
	running, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), running) ||
		!strings.Contains(output.String(), "integration withdrawal failed") {
		t.Fatalf("retained binary was not reported:\n%s", output.String())
	}
	matched, err := agentcfg.ZcodeMCPMatches(config, filepath.Join(home, "roca"))
	if err != nil || !matched {
		t.Fatalf("failed withdrawal changed active MCP: matched=%v err=%v", matched, err)
	}
}

func TestZcodeMCPAndHooksShareLifecycleLock(t *testing.T) {
	home := skillTestHome(t)
	env := &cliEnv{out: io.Discard, errOut: io.Discard, build: Build{Version: "test"}}
	config, wrapper := zcodeTestConfigAndWrapper(home)
	writeFile(t, config, "{}")
	release, err := env.lockManagedZcodeLifecycle()
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	go func() {
		_, installErr := env.installZcodeMCP(config, filepath.Join(home, "roca"))
		results <- installErr
	}()
	go func() {
		_, _, installErr := env.installManagedZcodeHandoffHook(config, wrapper, filepath.Join(home, "roca"))
		results <- installErr
	}()
	select {
	case err := <-results:
		t.Fatalf("ZCode edit bypassed shared lifecycle lock: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	configured, err := agentcfg.ZcodeMCPMatches(config, filepath.Join(home, "roca"))
	if err != nil || !configured || !zcodeManagedHookPresent(config) {
		t.Fatalf("serialized integrations missing: mcp=%v hook=%v err=%v", configured, zcodeManagedHookPresent(config), err)
	}
}

func TestZcodeLifecycleLockSerializesRegistryWrites(t *testing.T) {
	home := skillTestHome(t)
	holder := &cliEnv{out: io.Discard, errOut: io.Discard}
	release, err := holder.lockManagedZcodeLifecycle()
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		contender := &cliEnv{build: Build{Version: "test"}, zcodeLockWait: time.Second}
		done <- contender.registerHook(filepath.Join(home, "hook.json"), "claude", "managed")
	}()
	select {
	case err := <-done:
		t.Fatalf("registry write bypassed ZCode lifecycle lock: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestFullUninstallTimesOutBeforeChangingZcodeState(t *testing.T) {
	home := skillTestHome(t)
	config := filepath.Join(home, ".zcode", "cli", "config.json")
	installZcodeTestIntegration(t, "mcp", home)
	holder := &cliEnv{out: io.Discard, errOut: io.Discard}
	release, err := holder.lockManagedZcodeLifecycle()
	if err != nil {
		t.Fatal(err)
	}
	contender := &cliEnv{out: io.Discard, errOut: io.Discard}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	command := uninstallCommand(contender)
	command.SetContext(ctx)
	err = contender.uninstall(command, strings.NewReader(""), false)
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("contended uninstall error = %v", err)
	}
	matched, matchErr := agentcfg.ZcodeMCPMatches(config, filepath.Join(home, "roca"))
	if matchErr != nil || !matched {
		t.Fatalf("timed-out uninstall changed ZCode state: matched=%v err=%v", matched, matchErr)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
}

func TestExplicitZcodeCommandsTimeOutOnLifecycleLock(t *testing.T) {
	for _, operation := range []string{"mcp", "hooks", "skill"} {
		t.Run(operation, func(t *testing.T) {
			home := skillTestHome(t)
			holder := &cliEnv{out: io.Discard, errOut: io.Discard}
			release, err := holder.lockManagedZcodeLifecycle()
			if err != nil {
				t.Fatal(err)
			}
			contender := &cliEnv{out: io.Discard, errOut: io.Discard,
				build: Build{Version: "test"}, zcodeLockWait: 20 * time.Millisecond}
			switch operation {
			case "mcp":
				_, err = contender.installZcodeMCP(filepath.Join(home, ".zcode", "cli", "config.json"), filepath.Join(home, "roca"))
			case "hooks":
				config, wrapper := zcodeTestConfigAndWrapper(home)
				_, _, err = contender.installManagedZcodeHandoffHook(config, wrapper, filepath.Join(home, "roca"))
			case "skill":
				root := rootCommand(contender)
				root.SetArgs([]string{"skill", "install", "zcode"})
				err = root.Execute()
			}
			if releaseErr := release(); releaseErr != nil {
				t.Fatal(releaseErr)
			}
			if err == nil || !strings.Contains(err.Error(), "timed out") {
				t.Fatalf("contended %s error = %v", operation, err)
			}
		})
	}
}

func TestZcodePurgeDiscoversRegistryUnderLifecycleLock(t *testing.T) {
	home := skillTestHome(t)
	env := &cliEnv{out: io.Discard, errOut: io.Discard, build: Build{Version: "test"}}
	release, err := env.lockManagedZcodeLifecycle()
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan lifecycle.Report, 1)
	go func() {
		report := lifecycle.Report{Purged: true, Deleted: []string{}}
		env.withdrawTheIntegrations(&report, true)
		done <- report
	}()
	select {
	case report := <-done:
		t.Fatalf("purge discovered targets before acquiring lifecycle lock: %#v", report)
	case <-time.After(50 * time.Millisecond):
	}
	custom := filepath.Join(home, "custom", "config.json")
	preimage, err := zcodeMCPPathPreimage(custom)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := agentcfg.InstallZcodeMCP(custom, filepath.Join(home, "roca"), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := env.recordZcodeMCPPreimage(custom, filepath.Join(home, "roca"), agentcfg.ZcodeMCPPreimageMCPServers, false, preimage); err != nil {
		t.Fatal(err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	report := <-done
	if len(report.Errors) != 0 {
		t.Fatalf("purge errors = %v", report.Errors)
	}
	if _, err := os.Stat(custom); !os.IsNotExist(err) {
		t.Fatalf("late registered custom ZCode config survived purge: %v", err)
	}
}

func TestZcodeMCPReinstallRefreshesExecutableProvenance(t *testing.T) {
	home := skillTestHome(t)
	config := filepath.Join(home, ".zcode", "cli", "config.json")
	first := filepath.Join(home, "bin", "roca-v1")
	second := filepath.Join(home, "bin", "roca-v2")
	for _, executable := range []string{first, second} {
		runZcodeTestCLI(t, "mcp", "install", "zcode", "--executable", executable)
	}
	registry, err := artifact.LoadRegistry(filepath.Join(home, ".roca", "artifacts.json"))
	if err != nil {
		t.Fatal(err)
	}
	entry, found := registry.Find(artifactKindMCP, agentcfg.RuntimeZcode, config)
	if !found || entry.Executable != second || !entry.CreatedConfig || !entry.CreatedConfigDir ||
		!entry.CreatedRoot || entry.RootIdentity == "" {
		t.Fatalf("refreshed MCP provenance = %#v, found=%v", entry, found)
	}
	report := purgeZcodeTestIntegrations(true)
	if len(report.Errors) != 0 {
		t.Fatalf("purge errors = %v", report.Errors)
	}
	if _, err := os.Stat(filepath.Join(home, ".zcode")); !os.IsNotExist(err) {
		t.Fatalf("continuously owned MCP tree survived purge: %v", err)
	}
}

func TestZcodeFullUninstallDiscoversRegistryUnderLifecycleLock(t *testing.T) {
	home := skillTestHome(t)
	env := &cliEnv{out: io.Discard, errOut: io.Discard, build: Build{Version: "test"}}
	release, err := env.lockManagedZcodeLifecycle()
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan lifecycle.Report, 1)
	go func() {
		report := lifecycle.Report{Purged: true, Deleted: []string{}}
		env.withdrawTheIntegrations(&report, false)
		done <- report
	}()
	select {
	case report := <-done:
		t.Fatalf("full uninstall discovered targets before acquiring lifecycle lock: %#v", report)
	case <-time.After(50 * time.Millisecond):
	}
	custom := filepath.Join(home, "custom", "config.json")
	preimage, err := zcodeMCPPathPreimage(custom)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := agentcfg.InstallZcodeMCP(custom, filepath.Join(home, "roca"), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := env.recordZcodeMCPPreimage(custom, filepath.Join(home, "roca"),
		agentcfg.ZcodeMCPPreimageMCPServers, false, preimage); err != nil {
		t.Fatal(err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	report := <-done
	if len(report.Errors) != 0 {
		t.Fatalf("full uninstall errors = %v", report.Errors)
	}
	matched, err := agentcfg.ZcodeMCPMatches(custom, filepath.Join(home, "roca"))
	if err != nil || matched {
		t.Fatalf("late registered custom MCP survived full uninstall: matched=%v err=%v", matched, err)
	}
}

func TestManagedArtifactLocksRejectSymlinksWithoutChangingTargets(t *testing.T) {
	for _, test := range []struct {
		name     string
		lockPath func(string) string
		acquire  func(*cliEnv, string) (func() error, error)
	}{
		{
			name: "ZCode lifecycle",
			lockPath: func(home string) string {
				return filepath.Join(home, ".roca", "artifacts.json.lock")
			},
			acquire: func(env *cliEnv, _ string) (func() error, error) {
				return env.lockManagedZcodeLifecycle()
			},
		},
		{
			name: "artifact registry",
			lockPath: func(home string) string {
				return filepath.Join(home, ".roca", "artifacts.json.lock")
			},
			acquire: func(_ *cliEnv, home string) (func() error, error) {
				return lockArtifactRegistry(filepath.Join(home, ".roca", "artifacts.json"))
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := skillTestHome(t)
			if err := os.MkdirAll(filepath.Join(home, ".roca"), 0o700); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(home, "operator-data")
			if err := os.WriteFile(target, []byte("operator data\n"), 0o744); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(target, 0o744); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, test.lockPath(home)); err != nil {
				t.Fatal(err)
			}
			release, err := test.acquire(&cliEnv{}, home)
			if release != nil || err == nil || !strings.Contains(err.Error(), "non-regular managed lock") {
				t.Fatalf("symlink lock result: release=%v err=%v", release != nil, err)
			}
			info, statErr := os.Stat(target)
			body, readErr := os.ReadFile(target)
			if statErr != nil || readErr != nil || info.Mode().Perm() != 0o744 || string(body) != "operator data\n" {
				t.Fatalf("lock target changed: info=%v body=%q statErr=%v readErr=%v", info, body, statErr, readErr)
			}
		})
	}
}

func TestZcodeConfigCleanupRetainsReplacedEmptyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	before := `{}`
	if err := os.WriteFile(path, []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}
	preimage, err := agentcfg.ZcodeMCPPreimage(before)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := agentcfg.Install(agentcfg.RuntimeZcode, path, "roca"); err != nil {
		t.Fatal(err)
	}
	outcome, err := agentcfg.UninstallZcodeMCP(path, preimage)
	if err != nil || outcome.FileIdentity == nil {
		t.Fatalf("withdrawal identity: outcome=%#v err=%v", outcome, err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	err = cleanupCreatedZcodePaths(artifact.Entry{CreatedConfig: true}, path, nil, outcome.FileIdentity)
	if err == nil {
		t.Fatal("replacement cleanup was not reported")
	}
	if body, err := os.ReadFile(path); err != nil || string(body) != `{}` {
		t.Fatalf("operator replacement changed: body=%q err=%v", body, err)
	}
}

func TestFreshZcodeMCPPurgeRemovesCreatedRuntimePaths(t *testing.T) {
	assertFreshZcodePurgeRemovesRuntimePaths(t, "mcp")
}

func TestZcodeMCPPurgePreservesPreexistingConfig(t *testing.T) {
	home := skillTestHome(t)
	config := filepath.Join(home, ".zcode", "cli", "config.json")
	initial := `{"operator":true}`
	writeFile(t, config, initial)
	installZcodeTestIntegration(t, "mcp", home)
	report := purgeZcodeTestIntegrations(true)
	if len(report.Errors) != 0 {
		t.Fatalf("purge errors = %v", report.Errors)
	}
	if body, err := os.ReadFile(config); err != nil || string(body) != initial {
		t.Fatalf("preexisting config changed: body=%q err=%v", body, err)
	}
}

func TestZcodeMCPPurgeReportsUnprovenConfigAfterOrdinaryUninstall(t *testing.T) {
	home := skillTestHome(t)
	installZcodeTestIntegration(t, "mcp", home)
	runZcodeTestCLI(t, "mcp", "uninstall", "zcode")
	installZcodeTestIntegration(t, "mcp", home)
	report := purgeZcodeTestIntegrations(true)
	config := filepath.Join(home, ".zcode", "cli", "config.json")
	if !strings.Contains(strings.Join(report.Errors, "\n"), config) {
		t.Fatalf("unproven config not reported: %v", report.Errors)
	}
	if _, err := os.Stat(config); err != nil {
		t.Fatalf("unproven config removed: %v", err)
	}
}

func TestAConfigPathIsAcceptedForOneNamedRuntime(t *testing.T) {
	out, _ := runRootSplit(t, contractBuild(), nil,
		"mcp", "status", "claude", "--config", "/tmp/not-read.json")
	if !strings.Contains(out, "claude") || !strings.Contains(out, "/tmp/not-read.json") {
		t.Errorf("a single named runtime did not report over the named file:\n%s", out)
	}
}
