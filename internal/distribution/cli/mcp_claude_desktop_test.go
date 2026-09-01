package cli

import (
	"os"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/distribution/agentcfg"
)

// `roca mcp install claude-desktop` has to find the native config without
// `--config`. Pointing the existing claude writer there by hand was the
// workaround; the named runtime is the one-command install.
func TestClaudeDesktopInstallWritesTheNativeConfig(t *testing.T) {
	home := hermeticHome(t)
	t.Setenv(EnvExecutable, "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("APPDATA", "")

	path, err := agentcfg.ConfigPath(agentcfg.RuntimeClaudeDesktop, home, os.Getenv)
	if err != nil {
		t.Fatal(err)
	}

	out, err := runRootErr(t, Build{Version: "test"}, nil, "mcp", "install", "claude-desktop")
	if err != nil {
		t.Fatalf("mcp install claude-desktop: %v\n%s", err, out)
	}
	if !strings.Contains(out, path) {
		t.Errorf("receipt does not name the native config %q:\n%s", path, out)
	}

	configured, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read native config: %v", err)
	}
	text := string(configured)
	if !strings.Contains(text, `"mcpServers"`) || !strings.Contains(text, `"type": "stdio"`) {
		t.Errorf("native config is not the claude mcpServers stdio shape:\n%s", text)
	}
	if !strings.Contains(text, "mcp") || !strings.Contains(text, "serve") {
		t.Errorf("native config does not launch `mcp serve`:\n%s", text)
	}
}
