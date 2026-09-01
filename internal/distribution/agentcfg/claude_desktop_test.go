package agentcfg_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/distribution/agentcfg"
)

// Claude Desktop keeps its MCP config in the platform's application-support
// directory, not under a dotted folder in the home. The machine travels as
// data so a Windows or Linux layout is a table case on any host.
func TestClaudeDesktopConfigPathFollowsThePlatformLayout(t *testing.T) {
	home := t.TempDir()
	appData := filepath.Join(home, "AppData", "Roaming")
	xdg := filepath.Join(home, "cfg")
	cases := []struct {
		name string
		goos string
		env  map[string]string
		want string
	}{
		{
			name: "darwin",
			goos: "darwin",
			want: filepath.Join(home, "Library", "Application Support", "Claude",
				"claude_desktop_config.json"),
		},
		{
			name: "windows APPDATA",
			goos: "windows",
			env:  map[string]string{"APPDATA": appData},
			want: filepath.Join(appData, "Claude", "claude_desktop_config.json"),
		},
		{
			name: "windows default Roaming",
			goos: "windows",
			want: filepath.Join(home, "AppData", "Roaming", "Claude",
				"claude_desktop_config.json"),
		},
		{
			name: "linux XDG",
			goos: "linux",
			env:  map[string]string{"XDG_CONFIG_HOME": xdg},
			want: filepath.Join(xdg, "Claude", "claude_desktop_config.json"),
		},
		{
			name: "linux default config",
			goos: "linux",
			want: filepath.Join(home, ".config", "Claude", "claude_desktop_config.json"),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := agentcfg.ConfigPathForOS(agentcfg.RuntimeClaudeDesktop, home, tc.goos,
				lookup(tc.env))
			if err != nil {
				t.Fatalf("ConfigPathForOS: %v", err)
			}
			if got != tc.want {
				t.Errorf("path = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestClaudeDesktopInstallRefusesSymlinkedConfigWithoutMutation(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "managed.json")
	path := filepath.Join(dir, "claude_desktop_config.json")
	before := []byte(fixtures[agentcfg.RuntimeClaudeDesktop])
	if err := os.WriteFile(target, before, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	outcome, err := agentcfg.Install(agentcfg.RuntimeClaudeDesktop, path, "roca")
	if err == nil {
		t.Fatal("Install accepted a symlinked configuration")
	}
	if outcome.Changed || outcome.Backup != "" {
		t.Fatalf("outcome = %+v, want no mutation", outcome)
	}
	if got, err := os.Readlink(path); err != nil || got != target {
		t.Fatalf("config symlink = %q, %v; want %q", got, err, target)
	}
	if got, err := os.ReadFile(target); err != nil || string(got) != string(before) {
		t.Fatalf("managed target changed: %v\n%s", err, got)
	}
	if _, err := os.Lstat(path + ".roca.bak"); !os.IsNotExist(err) {
		t.Fatalf("unexpected backup beside symlink: %v", err)
	}
}
