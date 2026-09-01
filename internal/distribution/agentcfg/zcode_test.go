package agentcfg_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/distribution/agentcfg"
)

func TestZcodeUsesTheNestedMCPServersShape(t *testing.T) {
	path := fixtureFile(t, agentcfg.RuntimeZcode)
	if _, err := agentcfg.Install(agentcfg.RuntimeZcode, path, "roca"); err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal([]byte(read(t, path)), &document); err != nil {
		t.Fatal(err)
	}
	mcp, ok := document["mcp"].(map[string]any)
	if !ok {
		t.Fatal("zcode config has no mcp object")
	}
	servers, ok := mcp["servers"].(map[string]any)
	if !ok {
		t.Fatal("zcode config has no nested mcp.servers object")
	}
	roca, ok := servers[agentcfg.ServerName].(map[string]any)
	if !ok || roca["type"] != "stdio" || roca["command"] != "roca" {
		t.Fatalf("zcode roca server = %#v", servers[agentcfg.ServerName])
	}
	if _, flat := document["servers"]; flat {
		t.Fatal("zcode config wrote a flat servers member")
	}
}

func TestZcodeConfigPathTreatsZcodeHomeAsTheRuntimeRoot(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, "elsewhere")
	got, err := agentcfg.ConfigPath(agentcfg.RuntimeZcode, home,
		lookup(map[string]string{"ZCODE_HOME": root}))
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "cli", "config.json")
	if got != want {
		t.Fatalf("path = %q, want %q", got, want)
	}
	got, err = agentcfg.ConfigPath(agentcfg.RuntimeZcode, home, lookup(nil))
	if err != nil {
		t.Fatal(err)
	}
	want = filepath.Join(home, ".zcode", "cli", "config.json")
	if got != want {
		t.Fatalf("default path = %q, want %q", got, want)
	}
}

func TestZcodeUninstallPrunesOnlyContainersThisInstallCreated(t *testing.T) {
	dir := t.TempDir()

	empty := filepath.Join(dir, "empty.json")
	writeFile(t, empty, "{}\n")
	if _, err := agentcfg.Install(agentcfg.RuntimeZcode, empty, "roca"); err != nil {
		t.Fatal(err)
	}
	if _, err := agentcfg.Uninstall(agentcfg.RuntimeZcode, empty); err != nil {
		t.Fatal(err)
	}
	if got := read(t, empty); got != "{}\n" {
		t.Fatalf("empty file did not come back:\n%s", got)
	}
	if _, err := os.Stat(empty + ".roca-owned"); !os.IsNotExist(err) {
		t.Fatal("ownership sidecar survived uninstall of a file this install created")
	}

	ownedMCP := filepath.Join(dir, "owned-mcp.json")
	writeFile(t, ownedMCP, "{\n  \"mcp\": {}\n}\n")
	before := read(t, ownedMCP)
	if _, err := agentcfg.Install(agentcfg.RuntimeZcode, ownedMCP, "roca"); err != nil {
		t.Fatal(err)
	}
	if _, err := agentcfg.Uninstall(agentcfg.RuntimeZcode, ownedMCP); err != nil {
		t.Fatal(err)
	}
	if got := read(t, ownedMCP); got != before {
		t.Fatalf("operator-owned empty mcp was deduced from emptiness:\n--- before ---\n%s\n--- after ---\n%s", before, got)
	}

	ownedServers := filepath.Join(dir, "owned-servers.json")
	writeFile(t, ownedServers, "{\n  \"mcp\": {\n    \"servers\": {}\n  }\n}\n")
	before = read(t, ownedServers)
	if _, err := agentcfg.Install(agentcfg.RuntimeZcode, ownedServers, "roca"); err != nil {
		t.Fatal(err)
	}
	if _, err := agentcfg.Uninstall(agentcfg.RuntimeZcode, ownedServers); err != nil {
		t.Fatal(err)
	}
	if got := read(t, ownedServers); got != before {
		t.Fatalf("operator-owned empty servers was deduced from emptiness:\n--- before ---\n%s\n--- after ---\n%s", before, got)
	}
}

func TestZcodeUninstallClearsStaleOwnershipBeforeOperatorContainersAppear(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	writeFile(t, path, "{}\n")
	if _, err := agentcfg.Install(agentcfg.RuntimeZcode, path, "roca"); err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, "{}\n")
	if outcome, err := agentcfg.Uninstall(agentcfg.RuntimeZcode, path); err != nil {
		t.Fatal(err)
	} else if outcome.Changed {
		t.Fatal("uninstall changed a config with no Roca server")
	}
	if _, err := os.Stat(path + ".roca-owned"); !os.IsNotExist(err) {
		t.Fatalf("stale ownership survived explicit uninstall: %v", err)
	}

	before := "{\n  \"mcp\": {\n    \"servers\": {}\n  }\n}\n"
	writeFile(t, path, before)
	if _, err := agentcfg.Install(agentcfg.RuntimeZcode, path, "roca"); err != nil {
		t.Fatal(err)
	}
	if _, err := agentcfg.Uninstall(agentcfg.RuntimeZcode, path); err != nil {
		t.Fatal(err)
	}
	if got := read(t, path); got != before {
		t.Fatalf("stale ownership pruned operator containers:\n--- before ---\n%s\n--- after ---\n%s", before, got)
	}
}

func TestZcodeUninstallRejectsUnreadableOwnershipBeforeEditingConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	writeFile(t, path, "{}\n")
	if _, err := agentcfg.Install(agentcfg.RuntimeZcode, path, "roca"); err != nil {
		t.Fatal(err)
	}
	before := read(t, path)
	writeFile(t, path+".roca-owned", "{not-json\n")

	outcome, err := agentcfg.Uninstall(agentcfg.RuntimeZcode, path)
	if err == nil {
		t.Fatal("uninstall accepted an unrecognized ownership sidecar")
	}
	if outcome.Changed {
		t.Fatal("uninstall reported a config mutation after ownership preflight failed")
	}
	if got := read(t, path); got != before {
		t.Fatalf("uninstall edited config before rejecting ownership:\n--- before ---\n%s\n--- after ---\n%s", before, got)
	}
	if got := read(t, path+".roca-owned"); got != "{not-json\n" {
		t.Fatalf("uninstall edited unrecognized ownership: %s", got)
	}
}

func TestZcodeInstallRejectsUnreadableOwnershipBeforeEditingConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	writeFile(t, path, "{}\n")
	writeFile(t, path+".roca-owned", "{not-json\n")

	outcome, err := agentcfg.Install(agentcfg.RuntimeZcode, path, "roca")
	if err == nil {
		t.Fatal("install accepted an unrecognized ownership sidecar")
	}
	if outcome.Changed {
		t.Fatal("install reported a config mutation after ownership preflight failed")
	}
	if got := read(t, path); got != "{}\n" {
		t.Fatalf("install edited config before rejecting ownership: %s", got)
	}
	if got := read(t, path+".roca-owned"); got != "{not-json\n" {
		t.Fatalf("install edited unrecognized ownership: %s", got)
	}
}

func TestZcodeReinstallPreservesMCPContainerOwnership(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	writeFile(t, path, "{}\n")
	if _, err := agentcfg.Install(agentcfg.RuntimeZcode, path, "roca"); err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, "{\"mcp\":{}}\n")
	if _, err := agentcfg.Install(agentcfg.RuntimeZcode, path, "roca"); err != nil {
		t.Fatal(err)
	}
	if _, err := agentcfg.Uninstall(agentcfg.RuntimeZcode, path); err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal([]byte(read(t, path)), &document); err != nil {
		t.Fatal(err)
	}
	if _, ok := document["mcp"]; ok {
		t.Fatalf("reinstall lost ownership of the product-created mcp container: %#v", document)
	}
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestZcodeInstallLeavesNeighbouringServersAndTheme(t *testing.T) {
	path := fixtureFile(t, agentcfg.RuntimeZcode)
	before := read(t, path)
	if _, err := agentcfg.Install(agentcfg.RuntimeZcode, path, "roca"); err != nil {
		t.Fatal(err)
	}
	body := read(t, path)
	if !strings.Contains(body, `"theme"`) || !strings.Contains(body, "some-other-server") {
		t.Fatalf("install ate neighbouring zcode config: %s", body)
	}
	if _, err := agentcfg.Uninstall(agentcfg.RuntimeZcode, path); err != nil {
		t.Fatal(err)
	}
	if got := read(t, path); got != before {
		t.Fatalf("zcode uninstall did not restore neighbouring config")
	}
}
