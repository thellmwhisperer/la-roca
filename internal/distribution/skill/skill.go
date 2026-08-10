// Package skill installs the canonical agent skill that teaches runtimes how
// to use La Roca. One embedded SKILL.md, five personal skill paths, no edits
// to any other file the operator owns.
//
// SKILL.md is the only edit path. skills/roca/SKILL.md in the Agent Plugins
// package is a generated copy of the same bytes (go:generate below); the CLI
// test suite fails the build if they diverge.
package skill

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	_ "embed"

	"github.com/thellmwhisperer/la-roca/internal/distribution/agentcfg"
)

//go:generate cp SKILL.md ../../../skills/roca/SKILL.md

//go:embed SKILL.md
var content string

// SkillName is the directory and frontmatter name every runtime receives.
const SkillName = "roca"

// Outcome is what one install did. Changed false means the file already held
// the canonical text — the normal result of a second install.
type Outcome struct {
	Runtime string `json:"runtime"`
	Path    string `json:"path"`
	Changed bool   `json:"changed"`
	// Removed lists every directory an uninstall took away: the roca skill
	// directory and, when it left it hollow, the skills directory above it. It
	// is empty for an install and for an uninstall that changed nothing.
	Removed []string `json:"removed,omitempty"`
}

// rootOf is how each runtime's personal skills directory is reached from home.
// dirVar moves the whole root; pathVar names a config FILE whose parent holds
// skills/ (OpenCode). Claude's skill root is ~/.claude even though its MCP
// config file sits at ~/.claude.json — different file, different directory.
var rootOf = map[string]struct {
	dirVar, pathVar string
	dir             []string
}{
	agentcfg.RuntimeClaude:   {dirVar: "CLAUDE_CONFIG_DIR", dir: []string{".claude"}},
	agentcfg.RuntimeCodex:    {dirVar: "CODEX_HOME", dir: []string{".codex"}},
	agentcfg.RuntimeOpencode: {pathVar: "OPENCODE_CONFIG", dir: []string{".config", "opencode"}},
	agentcfg.RuntimeHermes:   {dirVar: "HERMES_HOME", dir: []string{".hermes"}},
	agentcfg.RuntimePi:       {dirVar: "PI_CODING_AGENT_DIR", dir: []string{".pi", "agent"}},
}

// Content is the canonical SKILL.md body shipped inside the binary.
func Content() string { return content }

// Runtimes are the supported agents, sorted — the same five agentcfg knows.
func Runtimes() []string {
	names := make([]string, 0, len(rootOf))
	for name := range rootOf {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Path resolves where one runtime keeps the roca skill under home.
func Path(name, home string, env func(string) string) (string, error) {
	spec, ok := rootOf[name]
	if !ok {
		return "", unknown(name)
	}
	if env == nil {
		env = func(string) string { return "" }
	}
	root := filepath.Join(append([]string{home}, spec.dir...)...)
	if spec.pathVar != "" {
		if declared := env(spec.pathVar); declared != "" {
			root = filepath.Dir(agentcfg.Expand(declared, home))
		}
	}
	if spec.dirVar != "" {
		if declared := env(spec.dirVar); declared != "" {
			root = agentcfg.Expand(declared, home)
		}
	}
	return filepath.Join(root, "skills", SkillName, "SKILL.md"), nil
}

// Uninstall removes the roca skill directory from one runtime.
// Only a file whose content matches the canonical skill is removed. The skills
// directory above it is taken back too when the install left it hollow, so
// withdrawing the skill leaves no empty chain behind: os.Remove is the whole
// guard, since another skill's directory keeps it from being empty.
func Uninstall(name, path string) (Outcome, error) {
	if _, ok := rootOf[name]; !ok {
		return Outcome{}, unknown(name)
	}
	out := Outcome{Runtime: name, Path: path}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return out, nil
	}
	previous, err := os.ReadFile(path)
	if err != nil {
		return out, fmt.Errorf("read %s: %w", path, err)
	}
	if string(previous) != content {
		return out, nil
	}
	dir := filepath.Dir(path)
	if filepath.Base(dir) != SkillName {
		return out, nil
	}
	if err := os.RemoveAll(dir); err != nil {
		return out, fmt.Errorf("remove %s: %w", dir, err)
	}
	out.Changed = true
	out.Removed = []string{dir}
	if skillsDir := filepath.Dir(dir); skillsDir != dir {
		if err := os.Remove(skillsDir); err == nil {
			out.Removed = append(out.Removed, skillsDir)
		}
	}
	return out, nil
}

// Install writes the canonical skill at path. Idempotent: identical bytes are
// left alone and Changed is false. Only this file is created or replaced.
func Install(name, path string) (Outcome, error) {
	if _, ok := rootOf[name]; !ok {
		return Outcome{}, unknown(name)
	}
	out := Outcome{Runtime: name, Path: path}
	previous, err := os.ReadFile(path)
	switch {
	case err == nil && string(previous) == content:
		return out, nil
	case err != nil && !os.IsNotExist(err):
		return out, fmt.Errorf("read %s: %w", path, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return out, fmt.Errorf("create the directory of %s: %w", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return out, fmt.Errorf("write %s: %w", path, err)
	}
	out.Changed = true
	return out, nil
}

func unknown(name string) error {
	return fmt.Errorf("unsupported skill runtime %q (want %s)",
		name, strings.Join(Runtimes(), ", "))
}
