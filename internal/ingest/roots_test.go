package ingest

import (
	"path/filepath"
	"strings"
	"testing"
)

// Every platform is resolved on any platform: the machine travels as data, so a
// Linux or Windows layout is a table case and not a machine nobody has.

func TestRootsOnMacOS(t *testing.T) {
	roots := ResolveRoots(Environment{GOOS: "darwin", Home: "/Users/op"}, Settings{})
	want := map[string]string{
		"claude projects": "/Users/op/.claude/projects",
		"claude config":   "/Users/op/.claude.json",
		"desktop":         "/Users/op/Library/Application Support/Claude/claude-code-sessions",
		"cowork":          "/Users/op/Library/Application Support/Claude/local-agent-mode-sessions",
		"codex":           "/Users/op/.codex",
		"codex sessions":  "/Users/op/.codex/sessions",
		"opencode":        "/Users/op/.local/share/opencode/opencode.db",
		"pi root":         "/Users/op/.pi",
		"pi":              "/Users/op/.pi/agent/sessions",
		"hermes":          "/Users/op/.hermes/state.db",
		"grok":            "/Users/op/.grok/sessions",
		"grok memtrace":   "/Users/op/.grok/memtrace",
	}
	got := map[string]string{
		"claude projects": roots.ClaudeProjects,
		"claude config":   roots.ClaudeConfig,
		"desktop":         roots.ClaudeDesktopSessions,
		"cowork":          roots.CoworkSessions,
		"codex":           roots.CodexRoot,
		"codex sessions":  roots.CodexSessions,
		"opencode":        roots.OpenCodeDB,
		"pi root":         roots.PiRoot,
		"pi":              roots.PiSessions,
		"hermes":          roots.HermesDB,
		"grok":            roots.GrokSessions,
		"grok memtrace":   roots.GrokMemtrace,
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

func TestGrokSessionsFollowTheEnvironment(t *testing.T) {
	roots := ResolveRoots(Environment{
		GOOS: "linux",
		Home: "/home/op",
		Getenv: environmentOf(map[string]string{
			"GROK_SESSIONS_ROOT": "/home/op/data/grok-sessions",
		}),
	}, Settings{})
	if roots.GrokSessions != "/home/op/data/grok-sessions" {
		t.Errorf("grok sessions = %q", roots.GrokSessions)
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
		Settings{PiSessions: "~/sessions/pi"})
	if roots.PiSessions != "/home/op/sessions/pi" {
		t.Errorf("pi = %q", roots.PiSessions)
	}
}

func TestAnExplicitExportPathIsScopedToOneInvocationAndDetectedByShape(t *testing.T) {
	base := ResolveRoots(Environment{GOOS: "linux", Home: "/home/op"}, Settings{})
	if len(base.ClaudeWebExports) != 0 || len(base.ChatGPTWebExports) != 0 {
		t.Fatalf("live roots contain standing exports: %+v", base)
	}
	for _, test := range []struct {
		name, fixture string
		claude        bool
	}{
		{"Claude", "anthropic-export", true},
		{"ChatGPT", "openai-export-v1", false},
		{"sharded ChatGPT", "openai-export-sharded", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join("testdata", test.fixture)
			got, err := WithExportPath(base, path)
			if err != nil {
				t.Fatalf("export %q: %v", path, err)
			}
			if (len(got.ClaudeWebExports) == 1) != test.claude ||
				(len(got.ChatGPTWebExports) == 1) == test.claude {
				t.Fatalf("roots = %+v", got)
			}
			if len(base.ClaudeWebExports) != 0 || len(base.ChatGPTWebExports) != 0 {
				t.Fatalf("base roots were mutated: %+v", base)
			}
		})
	}
}

// A vendor nobody can read off the folder is nobody's vendor. The refusal names
// both layouts, because the operator knows which product they exported and the
// binary does not.
func TestADirectoryWithNeitherExportShapeIsRefusedNamingBothOfThem(t *testing.T) {
	base := ResolveRoots(Environment{GOOS: "linux", Home: "/home/op"}, Settings{})
	for _, test := range []struct{ name, root string }{
		{"empty directory", t.TempDir()},
		{"the export's parent", "testdata"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := WithExportPath(base, test.root)
			if err == nil {
				t.Fatalf("roots = %+v, want a refusal", got)
			}
			for _, shape := range []string{"memories.json", "conversations-*.json"} {
				if !strings.Contains(err.Error(), shape) {
					t.Errorf("refusal %q does not name %q", err, shape)
				}
			}
		})
	}
}

func environmentOf(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}
