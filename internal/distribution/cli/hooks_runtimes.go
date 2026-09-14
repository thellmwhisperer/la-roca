package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/thellmwhisperer/la-roca/internal/distribution/agentcfg"
)

// The transports a session hook travels on. One per file shape, not one per
// runtime: Codex and Cursor differ only in which member holds their session
// event, and pi and OpenCode only in the language of the file La Roca writes.
const (
	transportJSONHooks    = "json-hooks"
	transportZcodeWrapper = "zcode-wrapper"
	transportScript       = "script"
)

// hookRuntime is everything that differs between the harnesses `roca hooks
// install` supports: where the session hook lives, how that file is shaped, and
// what the harness calls its session-start event. Adding a harness is a row.
type hookRuntime struct {
	transport string
	// event is the member holding the session-start hooks. Harnesses disagree
	// about its capitalization and La Roca must write theirs, not its own.
	event string
	// nested marks a document whose entries group their commands under a
	// "hooks" array, the shape Claude Code introduced and Codex adopted.
	nested bool
	// timeoutKey and timeout declare the per-hook budget in the units the
	// harness reads. An empty key writes no budget at all.
	timeoutKey string
	timeout    int
	// document is the set of top-level members a freshly created file needs
	// beside its hooks, such as Cursor's schema version.
	document map[string]any
	// locate resolves the file this runtime's hook is written into.
	locate func() (string, error)
}

var hookRuntimes = map[string]hookRuntime{
	agentcfg.RuntimeClaude: {
		transport: transportJSONHooks, event: claudeSessionStartEvent, nested: true,
		locate: claudeSettingsPath,
	},
	agentcfg.RuntimeCodex: {
		transport: transportJSONHooks, event: "SessionStart", nested: true,
		timeoutKey: "timeout", timeout: 15, locate: codexHooksPath,
	},
	agentcfg.RuntimeCursor: {
		transport: transportJSONHooks, event: "sessionStart",
		document: map[string]any{"version": 1}, locate: cursorHooksPath,
	},
	agentcfg.RuntimeZcode: {
		transport: transportZcodeWrapper, event: "SessionStart",
		locate: func() (string, error) { return hookConfigPath(agentcfg.RuntimeZcode) },
	},
	agentcfg.RuntimePi: {
		transport: transportScript, locate: piExtensionPath,
	},
	agentcfg.RuntimeOpencode: {
		transport: transportScript, locate: opencodePluginPath,
	},
}

func hookRuntimeNames() []string {
	names := make([]string, 0, len(hookRuntimes))
	for name := range hookRuntimes {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func supportedHookRuntime(name string) error {
	if _, ok := hookRuntimes[name]; ok {
		return nil
	}
	return fmt.Errorf("unsupported hook runtime %q (want %s)",
		name, strings.Join(hookRuntimeNames(), ", "))
}

// sessionHookCommand is the command line an installed hook launches. It names
// this executable's absolute path for the same reason every other installer
// does: a harness runs its hooks in a non-interactive shell where a bare `roca`
// is whatever PATH happens to hold.
func sessionHookCommand(executable, runtime string, req sessionRequest) string {
	parts := append([]string{
		shellQuote(executable), "hooks", "run", "session", "--runtime", runtime,
	}, req.flags()...)
	return strings.Join(parts, " ")
}

// sessionHookInvocation recognizes La Roca's own session entry for one runtime
// whatever binary path and flags it was installed with, so a reinstall repoints
// it instead of adding a second one and an uninstall finds it after a move.
func sessionHookInvocation(runtime string) *regexp.Regexp {
	return regexp.MustCompile(
		`^` + shellCommandExecutablePattern +
			`[ \t]+hooks[ \t]+run[ \t]+session[ \t]+--runtime[ \t]+` + regexp.QuoteMeta(runtime) +
			`(?:[ \t]+--pills)?(?:[ \t]+--handoff)?[ \t]*$`)
}

// hookArtifactPath is the file one runtime's session hook is written into. It
// is not the file `roca mcp install` edits: a harness that declares MCP servers
// in one document routinely declares its hooks in another.
func hookArtifactPath(runtime string) (string, error) {
	entry, ok := hookRuntimes[runtime]
	if !ok {
		return "", fmt.Errorf("unsupported hook runtime %q (want %s)",
			runtime, strings.Join(hookRuntimeNames(), ", "))
	}
	return entry.locate()
}

func homeDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("I do not know where your HOME is")
	}
	return home, nil
}

// runtimeRoot resolves a harness's own home, honouring the environment
// variable that harness documents so a test, a sandbox, or a second install can
// move it without moving HOME.
func runtimeRoot(dirVar string, fallback ...string) (string, error) {
	home, err := homeDir()
	if err != nil {
		return "", err
	}
	if declared := os.Getenv(dirVar); declared != "" {
		return agentcfg.Expand(declared, home), nil
	}
	return filepath.Join(append([]string{home}, fallback...)...), nil
}

func codexHooksPath() (string, error) {
	root, err := runtimeRoot("CODEX_HOME", ".codex")
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "hooks.json"), nil
}

func cursorHooksPath() (string, error) {
	root, err := runtimeRoot("CURSOR_HOME", ".cursor")
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "hooks.json"), nil
}

func piExtensionPath() (string, error) {
	root, err := runtimeRoot("PI_CODING_AGENT_DIR", ".pi", "agent")
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "extensions", "roca-session.ts"), nil
}

// opencodePluginPath follows OpenCode's own configuration variable, which names
// the config file rather than its directory, and keeps the plugin beside it.
func opencodePluginPath() (string, error) {
	home, err := homeDir()
	if err != nil {
		return "", err
	}
	root := filepath.Join(home, ".config", "opencode")
	if declared := os.Getenv("OPENCODE_CONFIG"); declared != "" {
		root = filepath.Dir(agentcfg.Expand(declared, home))
	}
	return filepath.Join(root, "plugins", "roca-session.js"), nil
}
