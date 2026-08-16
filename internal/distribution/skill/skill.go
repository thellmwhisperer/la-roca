// Package skill installs the agent skills that teach runtimes how to use La
// Roca: the embedded SKILL.md and the generated semantic catalog, each written
// to one personal skill path per supported runtime, with no edits to any other
// file the operator owns.
//
// SKILL.md, embedded below, is the only copy in the repository. The catalog
// skill is composed at install time from the semantic fragments of the
// installed plugin manifests.
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
	"github.com/thellmwhisperer/la-roca/internal/securefile"
)

//go:embed SKILL.md
var content string

// SkillName is the directory and frontmatter name every runtime receives.
const SkillName = "roca"

// CatalogName is the directory and frontmatter name of the generated semantic
// catalog skill, roca-semantica.
const CatalogName = "roca-semantica"

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
	Missing bool `json:"missing,omitempty"`
	// Unregistered means no registry record stands behind the zones on disk, so
	// the refusal is about provenance rather than about an edit.
	Unregistered bool   `json:"unregistered,omitempty"`
	SystemSHA256 string `json:"system_sha256,omitempty"`
	// Removed lists every directory an uninstall took away: the skill's
	// directory (roca or roca-semantica) and, when it left it hollow, the skills
	// directory above it. It is empty for an install and for an uninstall that
	// changed nothing.
	Removed []string `json:"removed,omitempty"`
}

// rootOf is how each runtime's personal skills directory is reached from home.
// dirVar moves the whole root; pathVar names a config FILE whose parent holds
// skills/ (OpenCode). Claude's skill root is ~/.claude even though its MCP
// config file sits at ~/.claude.json — different file, different directory.
//
// Grok Build and Qwen Code are skill seats only: their user skill directories
// were measured on a real machine (`grok inspect` listing user skills, Qwen's
// skill manager debug log naming its user-level directory), and their MCP
// configuration surfaces are not part of that measurement, so agentcfg's
// smaller runtime set stays what `roca mcp install` knows.
var rootOf = map[string]struct {
	dirVar, pathVar string
	dir             []string
}{
	agentcfg.RuntimeClaude:   {dirVar: "CLAUDE_CONFIG_DIR", dir: []string{".claude"}},
	agentcfg.RuntimeCodex:    {dirVar: "CODEX_HOME", dir: []string{".codex"}},
	agentcfg.RuntimeGrok:     {dirVar: "GROK_HOME", dir: []string{".grok"}},
	agentcfg.RuntimeOpencode: {pathVar: "OPENCODE_CONFIG", dir: []string{".config", "opencode"}},
	agentcfg.RuntimeHermes:   {dirVar: "HERMES_HOME", dir: []string{".hermes"}},
	agentcfg.RuntimePi:       {dirVar: "PI_CODING_AGENT_DIR", dir: []string{".pi", "agent"}},
	agentcfg.RuntimeQwen:     {dirVar: "QWEN_HOME", dir: []string{".qwen"}},
}

// Content is the canonical SKILL.md body shipped inside the binary.
func Content() string { return content }

// LegacySignature opens every SKILL.md this product has shipped. A pre-zone
// file that starts with it came from an older release, so a migration replaces
// it instead of preserving a stale copy of the skill as operator content.
func LegacySignature() string { return "---\nname: " + SkillName + "\n" }

// Runtimes are the supported skill seats, sorted. Five of them are also the
// MCP runtimes agentcfg knows; grok and qwen are skill seats only.
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
	return pathOf(name, home, env, SkillName)
}

// CatalogPath resolves where one runtime keeps the generated semantic catalog
// skill under home.
func CatalogPath(name, home string, env func(string) string) (string, error) {
	return pathOf(name, home, env, CatalogName)
}

func pathOf(name, home string, env func(string) string, skillName string) (string, error) {
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
	return filepath.Join(root, "skills", skillName, "SKILL.md"), nil
}

// UninstallWithChecksum removes one of this product's skill directories from a
// runtime, withdrawing the exact registered SYSTEM zone even when a newer
// binary ships different skill text. Only a file whose content matches that
// zone is removed. The skills directory above it is taken back too when the
// install left it hollow, so withdrawing the skill leaves no empty chain
// behind: os.Remove is the whole guard, since another skill's directory keeps
// it from being empty.
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
	user, unproven := "", false
	if zones, err := artifact.Parse(string(previous)); err == nil {
		if artifact.Checksum(zones.System) != systemSHA256 {
			return out, nil
		}
		user = zones.User
	} else if artifact.Checksum(string(previous)) != systemSHA256 {
		if !strings.HasPrefix(string(previous), LegacySignature()) {
			return out, nil
		}
		unproven = true
	}
	dir := filepath.Dir(path)
	if base := filepath.Base(dir); base != SkillName && base != CatalogName {
		return out, nil
	}
	// Two states hold bytes that exist nowhere else. A pre-zone file recognized
	// by its opening alone is ours by convention, not by checksum, so anything an
	// operator appended before the zones existed is only here; and a USER zone
	// they wrote into is theirs outright. Both leave in a named recovery copy
	// rather than at SKILL.md: a file kept there without its frontmatter is a
	// broken skill the runtime goes on loading after La Roca is gone.
	if unproven || user != "" {
		backup, err := securefile.BackUp(path, previous)
		if err != nil {
			return out, err
		}
		out.Backup = backup
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

// InstallWithOptions writes the zoned canonical skill at path. Idempotent
// installs are left alone and legacy operator bytes are adopted into USER; a
// changed SYSTEM zone is only overridden when force is explicit.
//
// A registered skill the operator deleted is written again without force: the
// install they typed is the consent, and a file that is not there has no bytes
// of theirs to clobber.
func InstallWithOptions(name, path, previousSystemSHA256 string, force bool) (Outcome, error) {
	return installZoned(request{
		runtime: name, path: path, system: content,
		legacySignature: LegacySignature(), previous: previousSystemSHA256,
		force: force, restoreMissing: true,
	})
}

// InstallCatalogWithOptions writes the generated semantic catalog at path with
// the same zoned contract as the canonical skill. The catalog has no legacy
// form — it is a new artifact — so a pre-zone file at its path is the
// operator's and is adopted into USER. restoreMissing is the caller's consent:
// an explicit skill install restores a deleted file, the automatic refresh a
// plugin lifecycle triggers does not.
func InstallCatalogWithOptions(name, path, body, previousSystemSHA256 string,
	force, restoreMissing bool) (Outcome, error) {
	return installZoned(request{
		runtime: name, path: path, system: body,
		previous: previousSystemSHA256, force: force, restoreMissing: restoreMissing,
	})
}

type request struct {
	runtime, path, system string
	legacySignature       string
	previous              string
	force, restoreMissing bool
}

func installZoned(req request) (Outcome, error) {
	if _, ok := rootOf[req.runtime]; !ok {
		return Outcome{}, unknown(req.runtime)
	}
	out := Outcome{Runtime: req.runtime, Path: req.path}
	result, err := artifact.RefreshFile(artifact.FileRequest{
		Path: req.path, System: req.system, LegacySignature: req.legacySignature,
		PreviousSystemSHA256: req.previous, Enabled: true, Force: req.force,
		RestoreMissing: req.restoreMissing,
	})
	out.Backup = result.Backup
	if err != nil {
		return out, err
	}
	out.Changed = result.Changed
	out.Diverged = result.Diverged
	out.Missing = result.Missing
	out.Unregistered = result.Unregistered
	out.SystemSHA256 = result.SystemSHA256
	return out, nil
}

func unknown(name string) error {
	return fmt.Errorf("unsupported skill runtime %q (want %s)",
		name, strings.Join(Runtimes(), ", "))
}
