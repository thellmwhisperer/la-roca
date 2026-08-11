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
	"sort"
	"strings"
)

// The supported runtimes.
const (
	RuntimeCodex    = "codex"
	RuntimeClaude   = "claude"
	RuntimeOpencode = "opencode"
	RuntimeHermes   = "hermes"
	RuntimePi       = "pi"
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
}

// Runtimes are the supported runtimes, sorted, which is the order every message
// to the operator lists them in.
func Runtimes() []string {
	names := make([]string, 0, len(runtimes))
	for name := range runtimes {
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
	r, err := find(name)
	if err != nil {
		return "", err
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
		backup, err := backUp(path, previous)
		if err != nil {
			return outcome, err
		}
		outcome.Backup = backup
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return outcome, fmt.Errorf("create the directory of %s: %w", path, err)
	}
	if err := write(path, next, previous); err != nil {
		return outcome, err
	}
	outcome.Changed = true
	return outcome, nil
}

// backUp copies the previous bytes aside before anything is replaced. An
// earlier recovery copy outranks a later one, so `.bak` is never overwritten.
func backUp(path string, previous []byte) (string, error) {
	backup := path + ".roca.bak"
	for index := 1; ; index++ {
		if _, err := os.Stat(backup); os.IsNotExist(err) {
			break
		} else if err != nil {
			return "", fmt.Errorf("inspect backup %s: %w", backup, err)
		}
		backup = fmt.Sprintf("%s.roca.bak.%d", path, index)
	}
	if err := os.WriteFile(backup, previous, 0o600); err != nil {
		return "", fmt.Errorf("back up %s: %w", path, err)
	}
	return backup, nil
}

// write publishes the new text through a temporary file in the same directory
// and a rename, so a runtime reading the config never sees half of it. The
// previous file's permissions are kept: this file is the operator's.
func write(path, content string, previous []byte) error {
	mode := os.FileMode(0o600)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".roca-config-*")
	if err != nil {
		return fmt.Errorf("stage the write of %s: %w", path, err)
	}
	staged := temporary.Name()
	temporary.Close()
	defer os.Remove(staged)
	if err := os.WriteFile(staged, []byte(content), mode); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	// os.WriteFile applies its mode only when it CREATES the file, and CreateTemp
	// already made this one at 0600, so the computed mode has to be applied here
	// or the operator's own permissions are silently tightened by every edit.
	if err := os.Chmod(staged, mode); err != nil {
		return fmt.Errorf("keep the permissions of %s: %w", path, err)
	}
	// The file may have changed under us between the read and here. Refusing
	// then is better than clobbering the runtime that owns it.
	if previous != nil {
		current, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("re-read %s: %w", path, err)
		}
		if string(current) != string(previous) {
			return fmt.Errorf(
				"%s changed while it was being edited: close the runtime that owns it and try again",
				path)
		}
	}
	return os.Rename(staged, path)
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
