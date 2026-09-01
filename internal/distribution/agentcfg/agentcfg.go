// Package agentcfg declares La Roca in an agent runtime's own configuration
// file and withdraws that declaration again.
//
// This package preserves operator-owned configuration: **the file belongs to
// the operator**. Roca owns one entry and edits it with surgical text ranges
// rather than parse-and-reserialize, preserving comments, ordering, blank
// lines, JSONC, and neighbouring servers. The bounded empty-container and
// ZCode empty-object whitespace exceptions are documented in docs/mcp.md.
package agentcfg

import (
	"encoding/json"
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

// The config formats. One per syntax, not one per runtime; a runtime row below
// supplies its server-map path and entry shape.
const (
	kindTOML  = "toml"
	kindYAML  = "yaml"
	kindJSON  = "json"
	kindJSONC = "jsonc"
)

// runtime is everything that differs between integrations: where a config
// lives, its syntax and server-map path, and the entry shape. The runtime name
// is the map key; adding a runtime is a row.
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

// commandAndArgs is the shared entry fragment for runtimes that store the
// binary and its arguments as separate members.
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
	Runtime      string      `json:"runtime"`
	Path         string      `json:"path"`
	MutationPath string      `json:"mutation_path,omitempty"`
	Changed      bool        `json:"changed"`
	Backup       string      `json:"backup,omitempty"`
	FileIdentity os.FileInfo `json:"-"`
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
	return InstallWithMutationPath(name, path, executable, nil)
}

func InstallWithMutationPath(name, path, executable string, record func(string) error) (Outcome, error) {
	r, err := find(name)
	if err != nil {
		return Outcome{}, err
	}
	if strings.TrimSpace(executable) == "" {
		executable = "roca"
	}
	return edit(name, path, func(text string, _ bool, mutationPath string) (string, error) {
		if record != nil {
			if err := record(mutationPath); err != nil {
				return "", err
			}
		}
		return declare(r, text, executable)
	}, nil, true)
}

func InstallZcodeSessionStartHook(path, marker, command string, timeoutMs int,
	record func(string, bool, bool, string) error) (Outcome, error) {
	return edit(RuntimeZcode, path, func(previous string, existed bool, mutationPath string) (string, error) {
		next, err := DeclareZcodeSessionStartHook(previous, marker, command, timeoutMs)
		if err != nil {
			return "", err
		}
		next, createdEnabled, err := EnsureZcodeHooksEnabled(next)
		if err != nil {
			return "", err
		}
		if record != nil {
			if err := record(previous, createdEnabled, existed, mutationPath); err != nil {
				return "", err
			}
		}
		return next, nil
	}, nil, true)
}

func InstallZcodeMCP(path, executable string, recordPreimage func(string, bool, bool, string) error) (Outcome, error) {
	if strings.TrimSpace(executable) == "" {
		executable = "roca"
	}
	r := runtimes[RuntimeZcode]
	return edit(RuntimeZcode, path, func(text string, existed bool, mutationPath string) (string, error) {
		preimage, err := ZcodeMCPPreimage(text)
		if err != nil {
			return "", err
		}
		_, configured, err := installed(r, text)
		if err != nil {
			return "", err
		}
		if recordPreimage != nil {
			if err := recordPreimage(preimage, configured, existed, mutationPath); err != nil {
				return "", err
			}
		}
		return declare(r, text, executable)
	}, nil, true)
}

// Uninstall withdraws Roca's entry with the bounded empty-container behavior
// documented at the package level. A configuration that is not there is not
// created, and a configuration with no Roca entry is not written to.
func Uninstall(name, path string) (Outcome, error) {
	r, err := find(name)
	if err != nil {
		return Outcome{}, err
	}
	return Edit(name, path, func(text string) (string, error) {
		return withdraw(r, text)
	}, false)
}

func validateZcodeMCPDocument(text string) error {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	r := runtimes[RuntimeZcode]
	view, root, err := rootObject(r, text)
	if err != nil {
		return err
	}
	servers, found, err := objectAtPath(view, root, []string{"mcp", "servers"})
	if err != nil || !found {
		return err
	}
	index := servers.find(ServerName)
	if index < 0 {
		return nil
	}
	entry, err := objectAt(view, servers.members[index].valueStart)
	if err != nil {
		return err
	}
	var entryType, command string
	var args []string
	if len(entry.members) != 3 || !decodeObjectMember(view, entry, "type", &entryType) ||
		!decodeObjectMember(view, entry, "command", &command) ||
		!decodeObjectMember(view, entry, "args", &args) || entryType != "stdio" || command == "" ||
		len(args) != 2 || args[0] != "mcp" || args[1] != "serve" {
		return fmt.Errorf("mcp.servers.%s must be a stdio command with args [\"mcp\", \"serve\"]", ServerName)
	}
	return nil
}

func ZcodeMCPMatches(path, executable string) (bool, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	r := runtimes[RuntimeZcode]
	view, root, err := rootObject(r, string(body))
	if err != nil {
		return false, err
	}
	servers, found, err := objectAtPath(view, root, []string{"mcp", "servers"})
	if err != nil || !found {
		return false, err
	}
	index := servers.find(ServerName)
	if index < 0 {
		return false, nil
	}
	entry, err := objectAt(view, servers.members[index].valueStart)
	if err != nil || len(entry.members) != 3 {
		return false, err
	}
	var entryType, command string
	var args []string
	if !decodeObjectMember(view, entry, "type", &entryType) ||
		!decodeObjectMember(view, entry, "command", &command) ||
		!decodeObjectMember(view, entry, "args", &args) {
		return false, nil
	}
	return entryType == "stdio" && command == executable &&
		len(args) == 2 && args[0] == "mcp" && args[1] == "serve", nil
}

func decodeObjectMember(view string, entry object, key string, destination any) bool {
	index := entry.find(key)
	if index < 0 {
		return false
	}
	member := entry.members[index]
	return json.Unmarshal([]byte(view[member.valueStart:member.end]), destination) == nil
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
		if name == RuntimeZcode {
			if validationErr := validateZcodeMCPDocument(string(text)); validationErr != nil {
				report.State, report.Error = StateInvalid, validationErr.Error()
				break
			}
		}
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
	return edit(name, path, func(text string, _ bool, _ string) (string, error) {
		return transform(text)
	}, nil, createMissing)
}

// EditWithBackup applies a surgical edit while allowing the recovery copy to
// be transformed before it is written. Credential retirement uses that hook
// to make a deliberately non-byte-exact, secret-free backup.
func EditWithBackup(name, path string, transform, backupTransform func(string) (string, error),
	createMissing bool) (Outcome, error) {
	return edit(name, path, func(text string, _ bool, _ string) (string, error) {
		return transform(text)
	}, backupTransform, createMissing)
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

func configMutationPath(path string) (string, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return path, nil
	}
	if err != nil {
		return "", err
	}
	if info.Mode().IsRegular() {
		return path, nil
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return "", fmt.Errorf("refuse non-regular configuration path %s", path)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("resolve configuration symlink %s: %w", path, err)
	}
	target, err := os.Lstat(resolved)
	if err != nil {
		return "", fmt.Errorf("inspect configuration symlink target %s: %w", path, err)
	}
	if !target.Mode().IsRegular() {
		return "", fmt.Errorf("refuse non-regular configuration symlink target %s", resolved)
	}
	return resolved, nil
}

func edit(name, path string, transform func(string, bool, string) (string, error),
	backupTransform func(string) (string, error), createMissing bool) (Outcome, error) {
	outcome := Outcome{Runtime: name, Path: path}
	target := path
	if name == RuntimeZcode || name == RuntimeClaudeDesktop {
		var err error
		target, err = configMutationPath(path)
		if err != nil {
			return outcome, err
		}
	}

	previous, err := os.ReadFile(target)
	switch {
	case os.IsNotExist(err) && !createMissing:
		return outcome, nil
	case os.IsNotExist(err):
		previous = nil
	case err != nil:
		return outcome, fmt.Errorf("read %s: %w", path, err)
	}

	outcome.MutationPath = target
	next, err := transform(string(previous), previous != nil, target)
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
		backup, err := securefile.BackUp(target, backupContent)
		if err != nil {
			return outcome, err
		}
		outcome.Backup = backup
	}
	if previous == nil {
		if err := securefile.CreatePreservingParentMode(target, []byte(next), 0o600, 0o700); err != nil {
			return outcome, err
		}
	} else if err := securefile.Replace(target, []byte(next), previous); err != nil {
		return outcome, err
	}
	identity, err := os.Lstat(target)
	if err != nil {
		return outcome, err
	}
	outcome.Changed = true
	outcome.FileIdentity = identity
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
	if path == "~" || strings.HasPrefix(path, "~/") || strings.HasPrefix(path, `~\`) {
		return filepath.Join(home, strings.TrimLeft(strings.TrimPrefix(path, "~"), `/\`))
	}
	return path
}
