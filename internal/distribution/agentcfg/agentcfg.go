// Package agentcfg declares La Roca in an agent runtime's own configuration
// file, and withdraws it again leaving every other byte where it was.
//
// This package preserves operator-owned configuration:
// **the file belongs to the operator**. Roca owns exactly one entry inside it,
// and everything else — comments, ordering, blank lines, the JSONC the runtime
// tolerates, the neighbouring servers — has to come back untouched. That is why
// the edits here are surgical text-range edits over the bytes that are there
// and not a parse-and-reserialize round trip: reserializing is easy and it
// silently eats the operator's comments.
package agentcfg

import (
	"fmt"
	"os"
	"path/filepath"
	goruntime "runtime"
	"sort"
	"strings"

	"github.com/thellmwhisperer/la-roca/internal/securefile"
)

// The supported runtimes.
const (
	RuntimeCodex         = "codex"
	RuntimeClaude        = "claude"
	RuntimeClaudeDesktop = "claude-desktop"
	RuntimeOpencode      = "opencode"
	RuntimeHermes        = "hermes"
	RuntimePi            = "pi"
	RuntimeZcode         = "zcode"
)

// Skill seats whose user skill directory this product measured but whose MCP
// configuration surface it did not: they name skill destinations only and stay
// outside the runtime map `roca mcp install` edits.
const (
	RuntimeGrok   = "grok"
	RuntimeQwen   = "qwen"
	RuntimeCursor = "cursor"
)

// ServerName is the entry Roca owns.
const ServerName = "roca"

// The states a runtime's configuration can be in.
const (
	StateConfigured    = "configured"
	StateNotConfigured = "not-configured"
	StateMissing       = "missing"
	StateInvalid       = "invalid"
	StateUnreadable    = "unreadable"
)

// The config formats. One per shape, not one per runtime: three runtimes speak
// JSON and they differ only in which key holds their server map.
const (
	kindTOML  = "toml"
	kindYAML  = "yaml"
	kindJSON  = "json"
	kindJSONC = "jsonc"
)

// runtime is everything that differs between the five: where its config lives,
// what format it is in, which key holds its MCP servers, and the shape of the
// entry inside that map. The name is the map key; adding a runtime is a row.
type runtime struct {
	kind string
	// dirVar is the environment variable that moves the config directory, and
	// dir/file are where it lives under the home when nothing overrides it.
	dirVar string
	dir    []string
	file   string
	// pathVar names the whole file instead of its directory, which is how
	// OpenCode is configured.
	pathVar string
	// serversKey is the member holding the map of MCP servers.
	serversKey string
	// parents are JSON objects traversed before serversKey.
	parents []string
	// entry renders the value Roca owns inside that map.
	entry func(executable string) fields
}

// fields is one entry as ordered key/value pairs. Ordered because a config file
// is read by people, and because a stable rendering is what makes installing
// twice write nothing the second time.
type fields []field

type field struct {
	key   string
	value any
}

// commandAndArgs is the entry three of the five runtimes take: the binary and
// its arguments as two members of their own.
func commandAndArgs(executable string) fields {
	return fields{{"command", executable}, {"args", []string{"mcp", "serve"}}}
}

var runtimes = map[string]runtime{
	RuntimeCodex: {
		kind: kindTOML, dirVar: "CODEX_HOME", dir: []string{".codex"},
		file: "config.toml", serversKey: "mcp_servers", entry: commandAndArgs,
	},
	RuntimeClaude: {
		kind: kindJSON, dirVar: "CLAUDE_CONFIG_DIR", file: ".claude.json",
		serversKey: "mcpServers",
		entry: func(e string) fields {
			return append(fields{{"type", "stdio"}}, commandAndArgs(e)...)
		},
	},
	RuntimeClaudeDesktop: {
		kind: kindJSON, serversKey: "mcpServers",
		entry: func(e string) fields {
			return append(fields{{"type", "stdio"}}, commandAndArgs(e)...)
		},
	},
	RuntimeOpencode: {
		kind: kindJSONC, dir: []string{".config", "opencode"}, file: "opencode.json",
		pathVar: "OPENCODE_CONFIG", serversKey: "mcp",
		entry: func(e string) fields {
			return fields{
				{"type", "local"}, {"command", []string{e, "mcp", "serve"}}, {"enabled", true},
			}
		},
	},
	RuntimeHermes: {
		kind: kindYAML, dirVar: "HERMES_HOME", dir: []string{".hermes"},
		file: "config.yaml", serversKey: "mcp_servers", entry: commandAndArgs,
	},
	RuntimePi: {
		kind: kindJSONC, dirVar: "PI_CODING_AGENT_DIR", dir: []string{".pi", "agent"},
		file: "mcp.json", serversKey: "mcpServers", entry: commandAndArgs,
	},
	RuntimeZcode: {
		kind: kindJSON, dir: []string{".zcode", "cli"},
		file: "config.json", parents: []string{"mcp"}, serversKey: "servers",
		entry: func(e string) fields {
			return append(fields{{"type", "stdio"}}, commandAndArgs(e)...)
		},
	},
}

// Runtimes are the supported runtimes, sorted, which is the order every message
// to the operator lists them in.
func Runtimes() []string {
	names := make([]string, 0, len(runtimes))
	for name := range runtimes {
		if name == RuntimeClaudeDesktop &&
			goruntime.GOOS != "darwin" && goruntime.GOOS != "windows" {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Outcome is what one edit did. Changed false means the file already said what
// it had to say, which is the normal result of the second install.
type Outcome struct {
	Runtime string `json:"runtime"`
	Path    string `json:"path"`
	Changed bool   `json:"changed"`
	Backup  string `json:"backup,omitempty"`
}

// Report is one runtime's state, read without touching the file.
type Report struct {
	Runtime string `json:"runtime"`
	Path    string `json:"path"`
	State   string `json:"state"`
	// Instance is the command line the entry launches, so an operator can see
	// at a glance which binary their agent is about to run.
	Instance string `json:"instance,omitempty"`
	Error    string `json:"error,omitempty"`
}

// ConfigPath resolves where one runtime keeps its configuration: the home
// first, then the environment, which wins.
func ConfigPath(name, home string, env func(string) string) (string, error) {
	return ConfigPathForOS(name, home, goruntime.GOOS, env)
}

// ConfigPathForOS is ConfigPath with an explicit platform for deterministic
// path resolution tests and installers that plan a different target platform.
func ConfigPathForOS(name, home, goos string, env func(string) string) (string, error) {
	r, err := find(name)
	if err != nil {
		return "", err
	}
	if name == RuntimeClaudeDesktop {
		switch goos {
		case "darwin":
			return filepath.Join(home, "Library", "Application Support", "Claude",
				"claude_desktop_config.json"), nil
		case "windows":
			root := env("APPDATA")
			if root == "" {
				root = filepath.Join(home, "AppData", "Roaming")
			}
			return filepath.Join(root, "Claude", "claude_desktop_config.json"), nil
		default:
			return "", fmt.Errorf("claude-desktop MCP configuration is supported on macOS and Windows")
		}
	}
	if name == RuntimeZcode {
		root := filepath.Join(home, ".zcode")
		if declared := env("ZCODE_HOME"); declared != "" {
			root = Expand(declared, home)
		}
		return filepath.Join(root, "cli", "config.json"), nil
	}
	if r.pathVar != "" {
		if declared := env(r.pathVar); declared != "" {
			return Expand(declared, home), nil
		}
	}
	directory := filepath.Join(append([]string{home}, r.dir...)...)
	if r.dirVar != "" {
		if declared := env(r.dirVar); declared != "" {
			directory = Expand(declared, home)
		}
	}
	return filepath.Join(directory, r.file), nil
}

// Install declares the stdio server in one runtime's configuration.
func Install(name, path, executable string) (Outcome, error) {
	r, err := find(name)
	if err != nil {
		return Outcome{}, err
	}
	if strings.TrimSpace(executable) == "" {
		executable = "roca"
	}
	return Edit(name, path, func(text string) (string, error) {
		return declare(r, text, executable)
	}, true)
}

func InstallZcodeMCP(path, executable string, recordPreimage func(string, bool) error) (Outcome, error) {
	if strings.TrimSpace(executable) == "" {
		executable = "roca"
	}
	r := runtimes[RuntimeZcode]
	return Edit(RuntimeZcode, path, func(text string) (string, error) {
		preimage, err := ZcodeMCPPreimage(text)
		if err != nil {
			return "", err
		}
		_, configured, err := installed(r, text)
		if err != nil {
			return "", err
		}
		if recordPreimage != nil {
			if err := recordPreimage(preimage, configured); err != nil {
				return "", err
			}
		}
		return declare(r, text, executable)
	}, true)
}

// Uninstall withdraws Roca's entry and leaves the rest of the file exactly as
// it was. A configuration that is not there is not created and a configuration
// with no Roca in it is not written to.
func Uninstall(name, path string) (Outcome, error) {
	r, err := find(name)
	if err != nil {
		return Outcome{}, err
	}
	return Edit(name, path, func(text string) (string, error) {
		return withdraw(r, text)
	}, false)
}

func ZcodeMCPMatches(path, executable string) (bool, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	r := runtimes[RuntimeZcode]
	document, err := editors[r.kind].decode(r, string(body))
	if err != nil {
		return false, err
	}
	mcp, _ := document["mcp"].(map[string]any)
	servers, _ := mcp["servers"].(map[string]any)
	entry, _ := servers[ServerName].(map[string]any)
	if len(entry) != 3 || entry["type"] != "stdio" || entry["command"] != executable {
		return false, nil
	}
	args, _ := entry["args"].([]any)
	return len(args) == 2 && args[0] == "mcp" && args[1] == "serve", nil
}

func UninstallZcodeMCP(path, preimage string) (Outcome, error) {
	if preimage != ZcodeMCPPreimageNone && preimage != ZcodeMCPPreimageServers &&
		preimage != ZcodeMCPPreimageMCPServers {
		return Outcome{Runtime: RuntimeZcode, Path: path}, fmt.Errorf("invalid ZCode MCP preimage %q", preimage)
	}
	r := runtimes[RuntimeZcode]
	return Edit(RuntimeZcode, path, func(text string) (string, error) {
		return withdrawZcodeMCP(r, text, preimage)
	}, false)
}

// Status reads one runtime's configuration without modifying it.
func Status(name, path string) (Report, error) {
	r, err := find(name)
	if err != nil {
		return Report{}, err
	}
	report := Report{Runtime: name, Path: path, State: StateConfigured}
	text, err := os.ReadFile(path)
	switch {
	case os.IsNotExist(err):
		report.State = StateMissing
	case err != nil:
		report.State, report.Error = StateUnreadable, err.Error()
	default:
		instance, found, err := installed(r, string(text))
		switch {
		case err != nil:
			report.State, report.Error = StateInvalid, err.Error()
		case !found:
			report.State = StateNotConfigured
		default:
			report.Instance = instance
		}
	}
	return report, nil
}

// Edit is the spine of every edit: read the exact bytes, transform them, and
// write only when they really changed, having backed up the previous ones
// first. A transform that changes nothing writes nothing, which is what makes a
// second install cost an operator nothing.
//
// It is exported so every configuration integration can share the same safety
// guarantees. Two edit paths would create two sets of ways to lose a file.
func Edit(name, path string, transform func(string) (string, error),
	createMissing bool) (Outcome, error) {
	return edit(name, path, transform, nil, createMissing)
}

// EditWithBackup applies a surgical edit while allowing the recovery copy to
// be transformed before it is written. Credential retirement uses that hook
// to make a deliberately non-byte-exact, secret-free backup.
func EditWithBackup(name, path string, transform, backupTransform func(string) (string, error),
	createMissing bool) (Outcome, error) {
	return edit(name, path, transform, backupTransform, createMissing)
}

// Rewrite transforms an existing file in place without creating a backup or
// returning a reportable Outcome. A missing file and an unchanged transform
// are both no-ops.
func Rewrite(path string, transform func(string) (string, error)) error {
	previous, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	next, err := transform(string(previous))
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if next == string(previous) {
		return nil
	}
	return securefile.Replace(path, []byte(next), previous)
}

func edit(name, path string, transform, backupTransform func(string) (string, error),
	createMissing bool) (Outcome, error) {
	outcome := Outcome{Runtime: name, Path: path}

	previous, err := os.ReadFile(path)
	switch {
	case os.IsNotExist(err) && !createMissing:
		return outcome, nil
	case os.IsNotExist(err):
		previous = nil
	case err != nil:
		return outcome, fmt.Errorf("read %s: %w", path, err)
	}

	next, err := transform(string(previous))
	if err != nil {
		return outcome, fmt.Errorf("%s: %w", path, err)
	}
	if next == string(previous) {
		return outcome, nil
	}

	if previous != nil {
		backupContent := previous
		if backupTransform != nil {
			content, err := backupTransform(string(previous))
			if err != nil {
				return outcome, fmt.Errorf("prepare recovery backup for %s: %w", path, err)
			}
			backupContent = []byte(content)
		}
		backup, err := securefile.BackUp(path, backupContent)
		if err != nil {
			return outcome, err
		}
		outcome.Backup = backup
	}
	if previous == nil {
		if err := securefile.CreatePreservingParentMode(path, []byte(next), 0o600, 0o700); err != nil {
			return outcome, err
		}
	} else if err := securefile.Replace(path, []byte(next), previous); err != nil {
		return outcome, err
	}
	outcome.Changed = true
	return outcome, nil
}

func find(name string) (runtime, error) {
	if r, ok := runtimes[name]; ok {
		return r, nil
	}
	return runtime{}, fmt.Errorf(
		"I do not know the runtime %q. The supported ones are: %s",
		name, strings.Join(Runtimes(), ", "))
}

// Expand turns a leading ~ into home. Shared by every installer that resolves
// an operator-declared path against the same home.
func Expand(path, home string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		return filepath.Join(home, strings.TrimPrefix(path, "~"))
	}
	return path
}
