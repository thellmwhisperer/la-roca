package ingest

import "testing"

// Every platform is resolved on any platform: the machine travels as data, so a
// Linux or Windows layout is a table case and not a machine nobody has.

func TestRootsOnMacOS(t *testing.T) {
	roots := ResolveRoots(Environment{GOOS: "darwin", Home: "/Users/op"}, Settings{})
	want := map[string]string{
		"claude projects": "/Users/op/.claude/projects",
		"desktop":         "/Users/op/Library/Application Support/Claude/claude-code-sessions",
		"cowork":          "/Users/op/Library/Application Support/Claude/local-agent-mode-sessions",
		"codex":           "/Users/op/.codex",
		"codex sessions":  "/Users/op/.codex/sessions",
		"opencode":        "/Users/op/.local/share/opencode/opencode.db",
		"pi":              "/Users/op/.pi/agent/sessions",
		"hermes":          "/Users/op/.hermes/state.db",
	}
	got := map[string]string{
		"claude projects": roots.ClaudeProjects,
		"desktop":         roots.ClaudeDesktopSessions,
		"cowork":          roots.CoworkSessions,
		"codex":           roots.CodexRoot,
		"codex sessions":  roots.CodexSessions,
		"opencode":        roots.OpenCodeDB,
		"pi":              roots.PiSessions,
		"hermes":          roots.HermesDB,
	}
	for name, expected := range want {
		if got[name] != expected {
			t.Errorf("%s = %q, want %q", name, got[name], expected)
		}
	}
}

func TestRootsOnLinuxFollowTheXDGDirectories(t *testing.T) {
	roots := ResolveRoots(Environment{GOOS: "linux", Home: "/home/op"}, Settings{})
	if roots.ClaudeDesktopSessions != "/home/op/.config/Claude/claude-code-sessions" {
		t.Errorf("desktop = %q", roots.ClaudeDesktopSessions)
	}
	if roots.OpenCodeDB != "/home/op/.local/share/opencode/opencode.db" {
		t.Errorf("opencode = %q", roots.OpenCodeDB)
	}

	// And they follow the variables when the operator moved them.
	moved := ResolveRoots(Environment{
		GOOS: "linux",
		Home: "/home/op",
		Getenv: environmentOf(map[string]string{
			"XDG_CONFIG_HOME": "/home/op/cfg",
			"XDG_DATA_HOME":   "/home/op/data",
		}),
	}, Settings{})
	if moved.ClaudeDesktopSessions != "/home/op/cfg/Claude/claude-code-sessions" {
		t.Errorf("desktop = %q", moved.ClaudeDesktopSessions)
	}
	if moved.OpenCodeDB != "/home/op/data/opencode/opencode.db" {
		t.Errorf("opencode = %q", moved.OpenCodeDB)
	}
}

func TestRootsOnWindowsUseItsOwnSeparatorAndItsOwnVariables(t *testing.T) {
	roots := ResolveRoots(Environment{
		GOOS: "windows",
		Home: `C:\Users\ale`,
		Getenv: environmentOf(map[string]string{
			"APPDATA":      `C:\Users\ale\AppData\Roaming`,
			"LOCALAPPDATA": `C:\Users\ale\AppData\Local`,
		}),
	}, Settings{})
	if roots.ClaudeProjects != `C:\Users\ale\.claude\projects` {
		t.Errorf("claude projects = %q", roots.ClaudeProjects)
	}
	if roots.ClaudeDesktopSessions != `C:\Users\ale\AppData\Roaming\Claude\claude-code-sessions` {
		t.Errorf("desktop = %q", roots.ClaudeDesktopSessions)
	}
	if roots.OpenCodeDB != `C:\Users\ale\AppData\Local\opencode\opencode.db` {
		t.Errorf("opencode = %q", roots.OpenCodeDB)
	}
	if roots.HermesDB != `C:\Users\ale\.hermes\state.db` {
		t.Errorf("hermes = %q", roots.HermesDB)
	}
}

// Under WSL the home is a Linux one and the work lives on the Windows drive. The
// agents' own roots stay under the Linux home, and what crosses /mnt/c is the
// workspace roots, which is exactly where the project decoding has to hold.
func TestUnderWSLTheAgentRootsStayLinuxAndTheWorkspaceCrossesTheMount(t *testing.T) {
	roots := ResolveRoots(Environment{
		GOOS:   "linux",
		Home:   "/home/ale",
		Getenv: environmentOf(map[string]string{"WSL_DISTRO_NAME": "Ubuntu"}),
	}, Settings{WorkspaceRoots: []string{"/mnt/c/Users/ale/code"}})
	if roots.ClaudeProjects != "/home/ale/.claude/projects" {
		t.Errorf("claude projects = %q", roots.ClaudeProjects)
	}
	if got := roots.Workspace.Selected; len(got) != 1 || got[0] != "/mnt/c/Users/ale/code" {
		t.Fatalf("workspace = %v", got)
	}
	project, ok := ProjectFromEncodedDir("C--Users-ale-code-demo", roots.Workspace)
	if !ok || project != "demo" {
		t.Errorf("project = %q, ok = %v", project, ok)
	}
}

// The roots are configuration, never constants. What the operator
// declares wins over the platform default, and it wins over the environment too.
func TestWhatTheOperatorDeclaresWinsOverThePlatformDefault(t *testing.T) {
	roots := ResolveRoots(
		Environment{GOOS: "darwin", Home: "/Users/op",
			Getenv: environmentOf(map[string]string{"CODEX_ROOT": "/from/the/environment"})},
		Settings{CodexRoot: "/declared/by/the/operator"})
	if roots.CodexRoot != "/declared/by/the/operator" {
		t.Errorf("codex root = %q", roots.CodexRoot)
	}
	if roots.CodexSessions != "/declared/by/the/operator/sessions" {
		t.Errorf("codex sessions = %q: they hang off the declared root", roots.CodexSessions)
	}
}

func TestTheEnvironmentWinsOverThePlatformDefault(t *testing.T) {
	roots := ResolveRoots(Environment{GOOS: "darwin", Home: "/Users/op",
		Getenv: environmentOf(map[string]string{
			"CLAUDE_PROJECTS_ROOT": "/elsewhere/projects",
			"HERMES_DB_PATH":       "/elsewhere/state.db",
		})}, Settings{})
	if roots.ClaudeProjects != "/elsewhere/projects" {
		t.Errorf("claude projects = %q", roots.ClaudeProjects)
	}
	if roots.HermesDB != "/elsewhere/state.db" {
		t.Errorf("hermes = %q", roots.HermesDB)
	}
}

func TestATildeInADeclaredRootIsExpandedAgainstTheDeclaredHome(t *testing.T) {
	roots := ResolveRoots(Environment{GOOS: "linux", Home: "/home/op"},
		Settings{PiSessions: "~/sessions/pi", AnthropicExportPaths: []string{"~/exports/claude"}})
	if roots.PiSessions != "/home/op/sessions/pi" {
		t.Errorf("pi = %q", roots.PiSessions)
	}
	if len(roots.ClaudeWebExports) != 1 ||
		roots.ClaudeWebExports[0] != "/home/op/exports/claude" {
		t.Errorf("Claude web exports = %v", roots.ClaudeWebExports)
	}
}

func environmentOf(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}
