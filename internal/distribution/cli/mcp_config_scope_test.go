package cli

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	before := `{"mcp":{}}`
	if err := os.WriteFile(path, []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}
	root := rootCommand(&cliEnv{out: io.Discard, build: Build{Version: "test"}})
	root.SetArgs([]string{"mcp", "install", "zcode", "--config", path, "--executable", filepath.Join(home, "roca")})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
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
	root = rootCommand(&cliEnv{out: io.Discard, build: Build{Version: "test"}})
	root.SetArgs([]string{"mcp", "uninstall", "zcode", "--config", path})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != before {
		t.Fatalf("ZCode MCP preimage changed: want %s, got %s", before, after)
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
	root := rootCommand(&cliEnv{out: io.Discard, build: Build{Version: "test"}})
	root.SetArgs([]string{"mcp", "install", "zcode", "--config", "zcode.json", "--executable", filepath.Join(home, "roca")})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
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
	rollback, err := env.recordZcodeMCPPreimage(path, agentcfg.ZcodeMCPPreimageMCPServers, false)
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
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	before := `{"mcp":{}}`
	if err := os.WriteFile(path, []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}
	root := rootCommand(&cliEnv{out: io.Discard, build: Build{Version: "test"}})
	root.SetArgs([]string{"mcp", "install", "zcode", "--config", path, "--executable", filepath.Join(home, "roca")})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
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

func TestAConfigPathIsAcceptedForOneNamedRuntime(t *testing.T) {
	out, _ := runRootSplit(t, contractBuild(), nil,
		"mcp", "status", "claude", "--config", "/tmp/not-read.json")
	if !strings.Contains(out, "claude") || !strings.Contains(out, "/tmp/not-read.json") {
		t.Errorf("a single named runtime did not report over the named file:\n%s", out)
	}
}
