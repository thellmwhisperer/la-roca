package ingest

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/thellmwhisperer/la-roca/internal/ingest/parsers"
)

// The scan only proves a file exists under a declared root and declares the
// identity a path can carry. What a file says about itself is the parser's word,
// and it outranks this one.

// sessionFileName is the UUID a Claude transcript is named with. A file with
// another name under that root is not a transcript, and reading it as one is how
// a scan invents sessions.
var sessionFileName = regexp.MustCompile(
	`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// Target is one artefact the scan found.
type Target struct {
	Path string
	Kind parsers.Kind
	// SourceAgent is who produced it. It is what the query layer groups by, so a
	// wrong one is worse than an absent one.
	SourceAgent string
	Project     string
	SessionID   string
	FileName    string
	SourceType  string
	SkillName   string
	// SidecarPath is the metadata file paired with a Cowork audit transcript.
	SidecarPath string
	// ExclusionReason marks a discovered artefact that policy counts but never
	// fingerprints, opens, parses, or writes.
	ExclusionReason string
}

// Plan is everything one run is going to look at, with what the scan already has
// to say about it.
type Plan struct {
	Targets  []Target
	Excluded []Target
	// Scanned counts the artefacts found per source, which is what `--dry-run`
	// reports and what tells an operator whether a root is being seen at all.
	Scanned map[string]int
	// WorkspaceRoots resolve session identity only; their files are not content.
	WorkspaceRoots []string
	// DetectedAgents names the runtimes whose routes or stores exist.
	DetectedAgents []string
	Warnings       []string
}

var supportedAgentFamilies = []string{
	"claude", "claude-desktop", "cowork", "codex", "opencode", "pi", "hermes",
}

// MissingAgentFamilies returns the supported families not present in detected,
// in the same stable order used by machine detection.
func MissingAgentFamilies(detected []string) []string {
	present := make(map[string]bool, len(detected))
	for _, family := range detected {
		present[family] = true
	}
	missing := make([]string, 0, len(supportedAgentFamilies))
	for _, family := range supportedAgentFamilies {
		if !present[family] {
			missing = append(missing, family)
		}
	}
	return missing
}

// Scan walks every root in the v1 matrix and returns what one run would read.
//
// It reads no content: a root that does not exist contributes nothing, and that
// is the normal state of a machine that does not run that agent.
func Scan(roots Roots) Plan {
	plan := Plan{
		Scanned:        map[string]int{},
		WorkspaceRoots: roots.Workspace.Selected,
		DetectedAgents: DetectAgents(roots),
	}
	plan.add(scanClaudeMemories(roots), "claude_memory_files")
	plan.addCodex(scanCodexFiles(roots))
	plan.add(scanClaudeSessions(roots, &plan), "session_files")
	plan.add(scanCodexSessions(roots), "codex_session_files")
	plan.add(scanDesktopSessions(roots), "claude_desktop_files")
	plan.add(scanCoworkSessions(roots), "cowork_files")
	plan.add(scanSubagents(roots), "subagent_files")
	plan.add(scanPiSessions(roots), "pi_session_files")
	plan.add(existingFile(roots.OpenCodeDB, Target{
		Kind: parsers.KindOpenCodeDB, SourceAgent: "opencode"}), "opencode_databases")
	plan.add(existingFile(roots.HermesDB, Target{
		Kind: parsers.KindHermesDB, SourceAgent: "hermes"}), "hermes_databases")
	return plan
}

func (p *Plan) addCodex(found []Target) {
	p.Scanned["codex_files"] += len(found)
	for _, target := range found {
		if target.ExclusionReason != "" {
			p.Excluded = append(p.Excluded, target)
			continue
		}
		p.Targets = append(p.Targets, target)
	}
}

// DetectAgents reports the agents whose route or store exists, in the stable
// order shown by init and doctor. Existence is the contract: an empty store is
// still an installed agent, while a configured default path that is absent is
// not one.
//
// The global ~/.claude/CLAUDE.md is configuration and not a store, so it does
// not detect claude: a machine that has only written instructions and never run
// a session has no ingestable content.
func DetectAgents(roots Roots) []string {
	claude := pathExists(roots.ClaudeProjects)
	for _, root := range roots.SubagentRoots {
		claude = claude || pathExists(root)
	}
	candidates := []struct {
		name    string
		present bool
	}{
		{"claude", claude},
		{"claude-desktop", pathExists(roots.ClaudeDesktopSessions)},
		{"cowork", pathExists(roots.CoworkSessions)},
		{"codex", pathExists(roots.CodexRoot) || pathExists(roots.CodexSessions) || isFile(roots.CodexStateDB)},
		{"opencode", isFile(roots.OpenCodeDB)},
		{"pi", pathExists(roots.PiSessions)},
		{"hermes", isFile(roots.HermesDB)},
	}
	detected := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.present {
			detected = append(detected, candidate.name)
		}
	}
	return detected
}

// add files what one source found. Every source calls it exactly once, which is
// what makes a plan always report every counter even at zero: `+= 0` registers
// the key, and a source missing from the report reads as one nobody looked at.
func (p *Plan) add(targets []Target, key string) {
	p.Scanned[key] += len(targets)
	p.Targets = append(p.Targets, targets...)
}

// scanClaudeMemories finds the per-project memory files. MEMORY.md is the index
// the memories point at, not a memory: ingesting it would store a table of
// contents as if it were knowledge. The global ~/.claude/CLAUDE.md is an
// instruction file and not memory (the sources ruling that excludes repository
// AGENTS.md/CLAUDE.md applies to it too), so it is not read here.
func scanClaudeMemories(roots Roots) []Target {
	var targets []Target
	for _, dir := range subdirectories(roots.ClaudeProjects) {
		project, _ := ProjectFromEncodedDir(dir, roots.Workspace)
		memoryDir := filepath.Join(roots.ClaudeProjects, dir, "memory")
		for _, name := range filesIn(memoryDir) {
			if !strings.HasSuffix(name, ".md") || name == "MEMORY.md" {
				continue
			}
			targets = append(targets, Target{
				Path:        filepath.Join(memoryDir, name),
				Kind:        parsers.KindClaudeMemory,
				SourceAgent: "claude",
				Project:     project,
				FileName:    name,
			})
		}
	}
	return targets
}

// scanCodexFiles finds the memories, rules and skills Codex keeps as files.
// `default.rules` is the shipped default and not the operator's rule.
func scanCodexFiles(roots Roots) []Target {
	var targets []Target
	for _, name := range filesIn(filepath.Join(roots.CodexRoot, "memories")) {
		if !strings.HasSuffix(name, ".md") {
			continue
		}
		kind := parsers.KindCodexFile
		exclusion := ""
		switch name {
		case "raw_memories.md":
			kind = parsers.KindCodexMemoryAggregate
		case "MEMORY.md", "memory_summary.md":
			exclusion = "derived Codex memory aggregate is excluded"
		}
		targets = append(targets, Target{
			Path:            filepath.Join(roots.CodexRoot, "memories", name),
			Kind:            kind,
			SourceAgent:     "codex",
			FileName:        name,
			SourceType:      "memory",
			ExclusionReason: exclusion,
		})
	}
	for _, name := range filesIn(filepath.Join(roots.CodexRoot, "rules")) {
		if strings.HasSuffix(name, ".rules") && name != "default.rules" {
			targets = append(targets, Target{
				Path:        filepath.Join(roots.CodexRoot, "rules", name),
				Kind:        parsers.KindCodexFile,
				SourceAgent: "codex",
				FileName:    name,
				SourceType:  "rule",
			})
		}
	}
	skills := filepath.Join(roots.CodexRoot, "skills")
	for _, dir := range subdirectories(skills) {
		path := filepath.Join(skills, dir, "SKILL.md")
		if !isFile(path) {
			continue
		}
		targets = append(targets, Target{
			Path:        path,
			Kind:        parsers.KindCodexFile,
			SourceAgent: "codex",
			FileName:    "SKILL.md",
			SourceType:  "skill",
			SkillName:   dir,
		})
	}
	return targets
}

// scanClaudeSessions finds the transcripts, and diagnoses the project directories
// no declared root explains.
func scanClaudeSessions(roots Roots, plan *Plan) []Target {
	var targets []Target
	var ambiguous []string
	for _, dir := range subdirectories(roots.ClaudeProjects) {
		project, resolved := ProjectFromEncodedDir(dir, roots.Workspace)
		full := filepath.Join(roots.ClaudeProjects, dir)
		names := filesIn(full)
		if !resolved && len(names) > 0 {
			ambiguous = append(ambiguous, dir)
		}
		for _, name := range names {
			if !strings.HasSuffix(name, ".jsonl") {
				continue
			}
			id := strings.TrimSuffix(name, ".jsonl")
			if !sessionFileName.MatchString(id) {
				continue
			}
			targets = append(targets, Target{
				Path:        filepath.Join(full, name),
				Kind:        parsers.KindClaudeSession,
				SourceAgent: "claude",
				Project:     project,
				SessionID:   id,
				FileName:    name,
			})
		}
	}
	if len(ambiguous) > 0 {
		// The diagnosis names the remedy and never the path: an encoded absolute
		// path is not a project, and persisting it as one is what this guards.
		plan.Warnings = append(plan.Warnings, fmt.Sprintf(
			"%d project directories encode an absolute path no declared workspace "+
				"root explains, so their sessions are stored with no project: declare "+
				"the root under workspace_roots and run the ingest again", len(ambiguous)))
	}
	return targets
}

// scanCodexSessions walks the rollouts, which Codex files by date and not by
// project.
func scanCodexSessions(roots Roots) []Target {
	targets := filesUnder(roots.CodexSessions, ".jsonl", Target{
		Kind: parsers.KindCodexSession, SourceAgent: "codex"})
	for i := range targets {
		targets[i].SessionID = strings.TrimSuffix(targets[i].FileName, ".jsonl")
	}
	return targets
}

func scanDesktopSessions(roots Roots) []Target {
	return filesUnder(roots.ClaudeDesktopSessions, ".json", Target{
		Kind: parsers.KindSessionMetadata, SourceAgent: "claude-desktop"})
}

// scanCoworkSessions pairs each metadata file with the audit transcript that
// hangs off it. The metadata comes first on purpose: it is what declares the
// session's identity and title, and the transcript merges over it.
func scanCoworkSessions(roots Roots) []Target {
	var targets []Target
	for _, metadata := range filesUnder(roots.CoworkSessions, ".json", Target{
		Kind: parsers.KindSessionMetadata, SourceAgent: "cowork"}) {
		targets = append(targets, metadata)
		audit := filepath.Join(strings.TrimSuffix(metadata.Path, ".json"), "audit.jsonl")
		if isFile(audit) {
			targets = append(targets, Target{
				Path:        audit,
				Kind:        parsers.KindCoworkAudit,
				SourceAgent: "cowork",
				FileName:    "audit.jsonl",
				SidecarPath: metadata.Path,
			})
		}
	}
	return targets
}

// scanSubagents discovers the transcripts under both layouts the runtime has
// used: the flat `subagents/` of a project and the one nested under a session.
func scanSubagents(roots Roots) []Target {
	var targets []Target
	seen := map[string]bool{}
	for _, root := range roots.SubagentRoots {
		for _, dir := range subdirectories(root) {
			project, _ := ProjectFromEncodedDir(dir, roots.Workspace)
			paths := jsonlIn(filepath.Join(root, dir, "subagents"))
			for _, session := range subdirectories(filepath.Join(root, dir)) {
				if session == "subagents" || session == "memory" {
					continue
				}
				paths = append(paths, jsonlIn(filepath.Join(root, dir, session, "subagents"))...)
			}
			for _, path := range paths {
				key := realPath(path)
				if seen[key] {
					continue
				}
				seen[key] = true
				targets = append(targets, Target{
					Path:        path,
					Kind:        parsers.KindSubagent,
					SourceAgent: "claude",
					Project:     project,
					FileName:    filepath.Base(path),
				})
			}
		}
	}
	return targets
}

// scanPiSessions reads exactly `sessions/<encoded-cwd>/*.jsonl`, with no
// recursion and no symlinks: Pi's own layout, and nothing that a link into it
// could smuggle in.
func scanPiSessions(roots Roots) []Target {
	var targets []Target
	for _, dir := range subdirectories(roots.PiSessions) {
		for _, path := range jsonlIn(filepath.Join(roots.PiSessions, dir)) {
			targets = append(targets, Target{
				Path:        path,
				Kind:        parsers.KindPiSession,
				SourceAgent: "pi",
				FileName:    filepath.Base(path),
			})
		}
	}
	return targets
}

// existingFile is one target for a path that is there and nothing for one that
// is not, which is the normal state of a source this machine does not run.
func existingFile(path string, shape Target) []Target {
	if !isFile(path) {
		return nil
	}
	shape.Path, shape.FileName = path, filepath.Base(path)
	return []Target{shape}
}

// --- disk, kept in one place ---

// subdirectories are the real directories inside a path, sorted, without
// following symlinks. A missing or unreadable root is an empty answer: a machine
// that does not run that agent has no root for it.
func subdirectories(root string) []string {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var names []string
	for _, entry := range entries {
		if entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	return names
}

func filesIn(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var names []string
	for _, entry := range entries {
		if entry.Type().IsRegular() {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	return names
}

func jsonlIn(dir string) []string {
	var paths []string
	for _, name := range filesIn(dir) {
		if filepath.Ext(name) == ".jsonl" {
			paths = append(paths, filepath.Join(dir, name))
		}
	}
	return paths
}

// filesUnder collects every file under a root whose name ends in `extension`,
// each one a copy of the shape its source declared. An undeclared root and an
// unreadable branch both contribute nothing, which is the normal state of a
// machine that does not run that agent.
func filesUnder(root, extension string, shape Target) []Target {
	if root == "" {
		return nil
	}
	var targets []Target
	filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || !entry.Type().IsRegular() ||
			!strings.HasSuffix(entry.Name(), extension) {
			return nil
		}
		shape.Path, shape.FileName = path, entry.Name()
		targets = append(targets, shape)
		return nil
	})
	return targets
}

func isFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// realPath resolves the links so one file reached by two names is one file.
func realPath(path string) string {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return path
	}
	return resolved
}
