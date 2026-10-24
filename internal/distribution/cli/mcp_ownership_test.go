package cli

import (
	"encoding/json"
	"io"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// The zcode sidecar owns exactly the value La Roca wrote at mcp.servers.roca.
// Every case drives `roca mcp install|uninstall zcode` against a throwaway file.

const operatorRoca = `{"mcp":{"servers":{"roca":{"type":"stdio","command":"/opt/operator/roca-fork","args":["serve"]}}}}`

func runZcodeMCP(t *testing.T, action, config string) string {
	t.Helper()
	var out strings.Builder
	root := rootCommand(&cliEnv{out: &out, errOut: io.Discard, build: Build{Version: "test"}})
	args := []string{"mcp", action, "zcode", "--config", config}
	if action == "install" {
		args = append(args, "--executable", "/usr/local/bin/roca")
	}
	root.SetArgs(args)
	if err := root.Execute(); err != nil {
		t.Fatalf("mcp %s zcode: %v", action, err)
	}
	return out.String()
}

func zcodeMCPConfig(t *testing.T, body string) string {
	t.Helper()
	config := filepath.Join(t.TempDir(), "config.json")
	writeFile(t, config, body)
	return config
}

func requireJSONEqual(t *testing.T, path, want string) {
	t.Helper()
	var got, expected any
	if err := json.Unmarshal(mustRead(t, path), &got); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(want), &expected); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, expected) {
		t.Fatalf("config = %s, want %s", mustRead(t, path), want)
	}
}

func TestZcodeMCPStaleClaimLeavesTheOperatorEntryAlone(t *testing.T) {
	config := zcodeMCPConfig(t, "{}\n")
	runZcodeMCP(t, "install", config)
	writeFile(t, config, operatorRoca)
	for _, action := range []string{"install", "uninstall"} {
		out := runZcodeMCP(t, action, config)
		if !strings.Contains(out, "operator") {
			t.Fatalf("%s did not say it left the operator entry alone: %q", action, out)
		}
		if got := string(mustRead(t, config)); got != operatorRoca {
			t.Fatalf("%s touched the operator entry: %s", action, got)
		}
	}
}

func TestZcodeMCPNeverAdoptsAnUnclaimedEntry(t *testing.T) {
	for name, body := range map[string]string{
		"operator value": operatorRoca,
		"identical value": `{"mcp":{"servers":{"roca":{"type":"stdio","command":"/usr/local/bin/roca",` +
			`"args":["mcp","serve"]}}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			config := zcodeMCPConfig(t, body)
			runZcodeMCP(t, "install", config)
			if got := string(mustRead(t, config)); got != body {
				t.Fatalf("install claimed an unclaimed entry: %s", got)
			}
			runZcodeMCP(t, "uninstall", config)
			if got := string(mustRead(t, config)); got != body {
				t.Fatalf("uninstall removed an unclaimed entry: %s", got)
			}
		})
	}
}

func TestZcodeMCPUninstallNeverPrunesOperatorContainers(t *testing.T) {
	for _, body := range []string{`{"mcpServers":{}}`, `{"mcp":{"servers":{}}}`} {
		config := zcodeMCPConfig(t, body)
		runZcodeMCP(t, "uninstall", config)
		if got := string(mustRead(t, config)); got != body {
			t.Fatalf("uninstall rewrote %s as %s", body, got)
		}
	}

	config := zcodeMCPConfig(t, "{}\n")
	runZcodeMCP(t, "install", config)
	writeFile(t, config, `{"mcp":{"servers":{}}}`)
	runZcodeMCP(t, "install", config)
	runZcodeMCP(t, "uninstall", config)
	requireJSONEqual(t, config, `{"mcp":{"servers":{}}}`)
}

const legacySidecar = `{"roca":"owned-containers-v1","mcp":["mcp","mcp.servers"]}` + "\n"

func TestZcodeMCPLegacyClaimUpdatesItsOwnEntry(t *testing.T) {
	config := zcodeMCPConfig(t, `{"theme":"dark","mcp":{"servers":{"roca":{"type":"stdio",`+
		`"command":"/old/place/roca","args":["mcp","serve"]}}}}`)
	writeFile(t, config+".roca-owned", legacySidecar)
	runZcodeMCP(t, "install", config)
	requireJSONEqual(t, config, `{"theme":"dark","mcp":{"servers":{"roca":{"type":"stdio",`+
		`"command":"/usr/local/bin/roca","args":["mcp","serve"]}}}}`)
	runZcodeMCP(t, "uninstall", config)
	requireJSONEqual(t, config, `{"theme":"dark"}`)
}

func TestZcodeMCPLegacyClaimOnAnEditedEntryIsTheOperators(t *testing.T) {
	body := `{"mcp":{"servers":{"roca":{"type":"stdio","command":"/old/place/roca",` +
		`"args":["mcp","serve","--verbose"]}}}}`
	config := zcodeMCPConfig(t, body)
	writeFile(t, config+".roca-owned", legacySidecar)
	for _, action := range []string{"install", "uninstall"} {
		runZcodeMCP(t, action, config)
		if got := string(mustRead(t, config)); got != body {
			t.Fatalf("%s touched an edited legacy entry: %s", action, got)
		}
	}
}

func TestZcodeConcurrentMCPAndHookInstallsKeepBothClaims(t *testing.T) {
	home, config := zcodeHookTestPaths(t)
	writeZcodeHookExecutable(t, home, "#!/bin/sh\nexit 0\n")
	writeFile(t, config, `{"theme":"dark"}`+"\n")

	concurrently := func(mcpAction, hookAction string) {
		var wg sync.WaitGroup
		errs := make([]error, 2)
		wg.Add(2)
		go func() {
			defer wg.Done()
			root := rootCommand(&cliEnv{out: io.Discard, errOut: io.Discard, build: Build{Version: "test"}})
			args := []string{"mcp", mcpAction, "zcode", "--config", config}
			if mcpAction == "install" {
				args = append(args, "--executable", "/usr/local/bin/roca")
			}
			root.SetArgs(args)
			errs[0] = root.Execute()
		}()
		go func() {
			defer wg.Done()
			errs[1] = executeZcodeHooks(hookAction)
		}()
		wg.Wait()
		for _, err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
	}

	concurrently("install", "install")
	var owned struct {
		MCP    []string          `json:"mcp"`
		Hooks  []string          `json:"hooks"`
		Claims map[string]string `json:"claims"`
	}
	if err := json.Unmarshal(mustRead(t, config+".roca-owned"), &owned); err != nil {
		t.Fatal(err)
	}
	if len(owned.MCP) == 0 || len(owned.Hooks) == 0 || owned.Claims["mcp.servers.roca"] == "" {
		t.Fatalf("a concurrent install lost its claim: %+v", owned)
	}

	concurrently("uninstall", "uninstall")
	requireJSONEqual(t, config, `{"theme":"dark"}`)
}
