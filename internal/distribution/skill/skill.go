// Package skill installs the canonical agent skill that teaches runtimes how
// to use La Roca: one embedded SKILL.md, one personal skill path per supported
// runtime, no edits to any other file the operator owns.
//
// SKILL.md, embedded below, is the only copy in the repository.
package skill

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	_ "embed"

	"github.com/thellmwhisperer/la-roca/internal/artifact"
	"github.com/thellmwhisperer/la-roca/internal/distribution/agentcfg"
)

//go:embed SKILL.md
var content string

// SkillName is the directory and frontmatter name every runtime receives.
const SkillName = "roca"

// Outcome is what one install did. Changed false means the file already held
// the canonical text — the normal result of a second install.
type Outcome struct {
	Runtime  string `json:"runtime"`
	Path     string `json:"path"`
	Changed  bool   `json:"changed"`
	Backup   string `json:"backup,omitempty"`
	Diverged bool   `json:"diverged,omitempty"`
	// Missing means the registered file was gone, so the divergence is a
	// deletion rather than an edit and the two cannot be reported alike.
	Missing      bool   `json:"missing,omitempty"`
	SystemSHA256 string `json:"system_sha256,omitempty"`
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

// LegacySignature opens every SKILL.md this product has shipped. A pre-zone
// file that starts with it came from an older release, so a migration replaces
// it instead of preserving a stale copy of the skill as operator content.
func LegacySignature() string { return "---\nname: " + SkillName + "\n" }

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

// UninstallWithChecksum removes the roca skill directory from one runtime,
// withdrawing the exact registered SYSTEM zone even when a newer binary ships
// different skill text. Only a file whose content matches that zone is removed.
// The skills directory above it is taken back too when the install left it
// hollow, so withdrawing the skill leaves no empty chain behind: os.Remove is
// the whole guard, since another skill's directory keeps it from being empty.
func UninstallWithChecksum(name, path, systemSHA256 string) (Outcome, error) {
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
	user := ""
	if zones, err := artifact.Parse(string(previous)); err == nil {
		if artifact.Checksum(zones.System) != systemSHA256 {
			return out, nil
		}
		user = zones.User
	} else if !weWroteThisPreZoneSkill(string(previous), systemSHA256) {
		return out, nil
	}
	if user != "" {
		changed, err := agentcfg.Edit("artifact", path, func(string) (string, error) {
			return user, nil
		}, false)
		if err != nil {
			return out, err
		}
		out.Changed = changed.Changed
		out.Backup = changed.Backup
		return out, nil
	}
	dir := filepath.Dir(path)
	if filepath.Base(dir) != SkillName {
		return out, nil
	}
	// The canonical file is ours and goes. The directory only follows when
	// nothing else is left in it: RemoveAll took whatever the operator had put
	// beside the skill, which is the half of D-7 that says what La Roca did not
	// create is never deleted. Remove is the same shape the parent already used.
	if err := os.Remove(path); err != nil {
		return out, fmt.Errorf("remove %s: %w", path, err)
	}
	out.Changed = true
	out.Removed = []string{path}
	if err := os.Remove(dir); err != nil {
		return out, nil
	}
	out.Removed = append(out.Removed, dir)
	if skillsDir := filepath.Dir(dir); skillsDir != dir {
		if err := os.Remove(skillsDir); err == nil {
			out.Removed = append(out.Removed, skillsDir)
		}
	}
	return out, nil
}

// weWroteThisPreZoneSkill recognizes a SKILL.md installed before the zones
// existed. Matching only this release's bytes left every earlier release's
// skill behind after an uninstall: a file in the runtime's skills directory
// still teaching agents to run a binary the same command just unlinked.
func weWroteThisPreZoneSkill(body, systemSHA256 string) bool {
	return artifact.Checksum(body) == systemSHA256 ||
		strings.HasPrefix(body, LegacySignature())
}

// InstallWithOptions writes the zoned canonical skill at path. Idempotent
// installs are left alone and legacy operator bytes are adopted into USER; a
// changed SYSTEM zone is only overridden when force is explicit.
func InstallWithOptions(name, path, previousSystemSHA256 string, force bool) (Outcome, error) {
	if _, ok := rootOf[name]; !ok {
		return Outcome{}, unknown(name)
	}
	out := Outcome{Runtime: name, Path: path}
	result, err := artifact.RefreshFile(artifact.FileRequest{
		Path: path, System: content, LegacySignature: LegacySignature(),
		PreviousSystemSHA256: previousSystemSHA256, Enabled: true, Force: force,
	})
	if err != nil {
		return out, err
	}
	out.Changed = result.Changed
	out.Backup = result.Backup
	out.Diverged = result.Diverged
	out.Missing = result.Missing
	out.SystemSHA256 = result.SystemSHA256
	return out, nil
}

func unknown(name string) error {
	return fmt.Errorf("unsupported skill runtime %q (want %s)",
		name, strings.Join(Runtimes(), ", "))
}
