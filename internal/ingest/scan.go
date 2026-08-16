package ingest

import (
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
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
	"claude", "claude-desktop", "cowork", "codex", "opencode", "pi", "hermes", "grok", "cursor",
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
	plan.add(existingFile(filepath.Join(roots.CodexRoot, "history.jsonl"), Target{
		Kind: parsers.KindCodexHistory, SourceAgent: "codex",
	}), "codex_history_files")
	plan.add(scanDesktopSessions(roots), "claude_desktop_files")
	plan.add(scanCoworkSessions(roots), "cowork_files")
	plan.add(scanSubagents(roots), "subagent_files")
	piFiles := scanPiStore(roots, &plan)
	plan.Scanned["pi_files"] += len(piFiles)
	var piSessions []Target
	for _, target := range piFiles {
		if target.ExclusionReason != "" {
			plan.Excluded = append(plan.Excluded, target)
			continue
		}
		piSessions = append(piSessions, target)
	}
	plan.add(piSessions, "pi_session_files")
	plan.add(scanGrokSessions(roots), "grok_session_files")
	plan.add(scanClaudeWebExports(roots), "claude_web_export_files")
	plan.add(scanChatGPTWebExports(roots, &plan), "chatgpt_web_export_files")
	plan.add(existingFile(roots.OpenCodeDB, Target{
		Kind: parsers.KindOpenCodeDB, SourceAgent: "opencode"}), "opencode_databases")
	plan.add(existingFile(roots.HermesDB, Target{
		Kind: parsers.KindHermesDB, SourceAgent: "hermes"}), "hermes_databases")
	if roots.Home != "" {
		addRegisteredParsers(roots, &plan, parsers.Registered())
	}
	return plan
}

// addRegisteredParsers is the generic contribution route. A registry line may
// declare narrow session-store locations; the scanner turns every regular file
// under them into the same Target the established source-specific scanners
// emit. Detect runs only after the fingerprint gate, so an unchanged file is
// still never opened for parsing. Syntax never appears here.
func addRegisteredParsers(roots Roots, plan *Plan, registered []parsers.Registration) {
	for _, contribution := range registered {
		if contribution.Name == "" || contribution.Parser == nil || len(contribution.Locations) == 0 {
			continue
		}
		source := contribution.SourceAgent
		if source == "" {
			source = contribution.Name
		}
		shape := Target{Kind: parsers.Kind(contribution.Name), SourceAgent: source}
		declared, refused := contribution.ResolveLocations(roots.Home)
		for _, location := range refused {
			// A caller assembling only the roots it needs has no home against
			// which a contributed relative store can resolve. That source is
			// absent from this invocation, not misconfigured; ResolveRoots always
			// supplies Home for real CLI runs.
			if roots.Home == "" && !filepath.IsAbs(location) {
				continue
			}
			plan.Warnings = append(plan.Warnings, fmt.Sprintf(
				"parser %q declares the unusable location %q", contribution.Name, location))
		}
		var targets []Target
		present := false
		for _, root := range declared {
			if pathExists(root) {
				present = true
			}
			targets = append(targets, filesUnder(root, "", shape)...)
		}
		plan.add(targets, contribution.Name+"_files")
		if present && !slices.Contains(plan.DetectedAgents, source) {
			plan.DetectedAgents = append(plan.DetectedAgents, source)
		}
	}
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
		{"claude-web", anyPathExists(roots.ClaudeWebExports)},
		{"chatgpt-web", anyPathExists(roots.ChatGPTWebExports)},
		{"cowork", pathExists(roots.CoworkSessions)},
		{"codex", pathExists(roots.CodexRoot) || pathExists(roots.CodexSessions) || isFile(roots.CodexStateDB)},
		{"opencode", isFile(roots.OpenCodeDB)},
		{"pi", pathExists(roots.PiRoot) || pathExists(roots.PiSessions)},
		{"hermes", isFile(roots.HermesDB)},
		{"grok", pathExists(roots.GrokSessions)},
	}
	detected := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.present {
			detected = append(detected, candidate.name)
		}
	}
	return detected
}

// scanClaudeWebExports reads only the two v1 files from directories the
// operator declared. In particular it never walks projects/ or design_chats/.
func scanClaudeWebExports(roots Roots) []Target {
	var targets []Target
	seen := map[string]bool{}
	for _, root := range roots.ClaudeWebExports {
		for _, shape := range []struct {
			name string
			kind parsers.Kind
		}{
			{"memories.json", parsers.KindClaudeWebMemories},
			{"conversations.json", parsers.KindClaudeWebConversations},
		} {
			path := filepath.Join(root, shape.name)
			key := realPath(path)
			if !isFile(path) || seen[key] {
				continue
			}
			seen[key] = true
			targets = append(targets, Target{
				Path: path, Kind: shape.kind, SourceAgent: "claude-web",
				FileName: shape.name,
			})
		}
	}
	return targets
}

// scanChatGPTWebExports reads both generations of the export conversation
// layout and accounts for the records that this build deliberately leaves out.
//
// A declaration nobody can read and a directory whose layout nobody recognizes
// are different problems with different remedies, so they are diagnosed apart
// and neither is passed over in silence.
func scanChatGPTWebExports(roots Roots, plan *Plan) []Target {
	var legacy, rest []Target
	seen := map[string]bool{}
	for _, root := range roots.ChatGPTWebExports {
		names, err := readFiles(root)
		if err != nil {
			plan.Warnings = append(plan.Warnings, fmt.Sprintf(
				"OpenAI export path %q cannot be read (%v): pass the extracted "+
					"export directory to `roca ingest` again", root, err))
			continue
		}
		recognized := false
		for _, name := range names {
			path := filepath.Join(root, name)
			target := Target{Path: path, SourceAgent: "chatgpt-web", FileName: name}
			isLegacy := false
			switch {
			case name == "conversations.json":
				recognized, isLegacy = true, true
				target.Kind = parsers.KindChatGPTWebConversations
			case strings.HasPrefix(name, "conversations-") && strings.HasSuffix(name, ".json"):
				recognized = true
				target.Kind = parsers.KindChatGPTWebConversations
			case name == "shared_conversations.json":
				target.ExclusionReason = "shared ChatGPT conversations are out of scope"
			case name == "codex.json":
				// The one companion that carries conversations of its own. Counting it
				// as left out by design is not a warning, and it is the only signal an
				// operator gets that a whole content file went unread.
				target.ExclusionReason = "Codex conversations in a ChatGPT export are out of scope"
			case name == "conversation_asset_file_names.json" || name == "chat.html" ||
				name == "ads.json":
				continue
			default:
				target.ExclusionReason = "ChatGPT export attachment is out of scope"
			}
			key := realPath(path)
			if seen[key] {
				continue
			}
			seen[key] = true
			if isLegacy {
				legacy = append(legacy, target)
				continue
			}
			rest = append(rest, target)
		}
		if !recognized {
			plan.Warnings = append(plan.Warnings, fmt.Sprintf(
				"unrecognized OpenAI export layout at %q: expected conversations.json or conversations-*.json",
				root))
		}
	}
	// Legacy snapshots come first so a run reads them in the order it prefers
	// them. Retaining their richer provenance is the writer's reconciliation and
	// not this ordering, which says nothing about a snapshot ingested months ago.
	return append(legacy, rest...)
}

func anyPathExists(paths []string) bool {
	for _, path := range paths {
		if pathExists(path) {
			return true
		}
	}
	return false
}

// add files what one source found. Every source calls it exactly once, which is
// what makes a plan always report every counter even at zero: `+= 0` registers
// the key, and a source missing from the report reads as one nobody looked at.
func (p *Plan) add(targets []Target, key string) {
	p.Scanned[key] += len(targets)
	for _, target := range targets {
		if target.ExclusionReason != "" {
			p.Excluded = append(p.Excluded, target)
			continue
		}
		p.Targets = append(p.Targets, target)
	}
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
		exclusion := runnerExclusion(roots, dir)
		memoryDir := filepath.Join(roots.ClaudeProjects, dir, "memory")
		for _, name := range filesIn(memoryDir) {
			if !strings.HasSuffix(name, ".md") || name == "MEMORY.md" {
				continue
			}
			targets = append(targets, Target{
				Path:            filepath.Join(memoryDir, name),
				Kind:            parsers.KindClaudeMemory,
				SourceAgent:     "claude",
				Project:         project,
				FileName:        name,
				ExclusionReason: exclusion,
			})
		}
	}
	return targets
}

// scanCodexFiles finds the memories and rules Codex keeps as files, plus the
// instruction documents policy requires the report to count and refuse.
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
			Path:            path,
			Kind:            parsers.KindCodexFile,
			SourceAgent:     "codex",
			FileName:        "SKILL.md",
			ExclusionReason: "Codex skill instruction document is excluded",
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
		exclusion := runnerExclusion(roots, dir)
		names := filesIn(full)
		if exclusion == "" && !resolved && len(names) > 0 {
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
				Path:            filepath.Join(full, name),
				Kind:            parsers.KindClaudeSession,
				SourceAgent:     "claude",
				Project:         project,
				SessionID:       id,
				FileName:        name,
				ExclusionReason: exclusion,
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
			exclusion := runnerExclusion(roots, dir)
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
					Path:            path,
					Kind:            parsers.KindSubagent,
					SourceAgent:     "claude",
					Project:         project,
					FileName:        filepath.Base(path),
					ExclusionReason: exclusion,
				})
			}
		}
	}
	return targets
}

func runnerExclusion(roots Roots, encodedDir string) string {
	if roots.RunnerDir != "" {
		for _, path := range []string{roots.RunnerDir, realPath(roots.RunnerDir)} {
			if encodedDir == encodeRoot(cleanRoot(path)) {
				return "La Roca local-binary runner session is excluded"
			}
		}
	}
	return ""
}

// scanPiStore accounts for Pi's whole private tree. Session JSONL is recursive
// because Pi extensions keep child runs below the parent session; everything
// else is named as configuration, runtime state, or an unrecognized artefact.
// WalkDir does not follow symlinks, so a link cannot expand the declared root.
func scanPiStore(roots Roots, plan *Plan) []Target {
	root := roots.PiRoot
	var targets []Target
	seen := map[string]bool{}
	if root != "" {
		filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				if !os.IsNotExist(err) {
					plan.Warnings = append(plan.Warnings,
						fmt.Sprintf("Pi root cannot be read at %q (%v)", path, err))
				}
				return nil
			}
			if !entry.Type().IsRegular() {
				return nil
			}
			relative, relativeErr := filepath.Rel(root, path)
			if relativeErr != nil {
				return nil
			}
			relative = filepath.ToSlash(relative)
			target := Target{Path: path, Kind: parsers.KindPiSession,
				SourceAgent: "pi", FileName: entry.Name()}
			if !piSessionPath(relative) {
				target.ExclusionReason = piFileExclusion(relative)
			}
			targets = append(targets, target)
			seen[realPath(path)] = true
			return nil
		})
	}
	for _, target := range filesUnder(roots.PiSessions, ".jsonl", Target{
		Kind: parsers.KindPiSession, SourceAgent: "pi",
	}) {
		if seen[realPath(target.Path)] {
			continue
		}
		targets = append(targets, target)
	}
	return targets
}

func piSessionPath(relative string) bool {
	if !strings.HasSuffix(relative, ".jsonl") {
		return false
	}
	if strings.HasPrefix(relative, "agent/sessions/") {
		return true
	}
	// Pi briefly wrote sessions directly below agent/. run-history.jsonl is a
	// separate extension log and is the one known exception in that directory.
	return strings.Count(relative, "/") == 1 && strings.HasPrefix(relative, "agent/") &&
		filepath.Base(relative) != "run-history.jsonl"
}

func piFileExclusion(relative string) string {
	switch {
	case strings.HasPrefix(relative, "agent/missions/index/"):
		return "Pi mission index metadata"
	case relative == "agent/run-history.jsonl" || strings.HasSuffix(relative, ".log"):
		return "Pi runtime log"
	case strings.HasPrefix(relative, "agent/npm/") ||
		strings.HasPrefix(relative, "agent/git/") ||
		strings.HasPrefix(relative, "agent/bin/") ||
		strings.HasPrefix(relative, "agent/cache/"):
		return "Pi runtime and package file"
	case strings.HasPrefix(relative, "agent/prompts/") ||
		strings.HasPrefix(relative, "agent/skills/") ||
		strings.HasPrefix(relative, "extensions/") ||
		strings.HasPrefix(relative, "agent/extensions/") ||
		strings.Contains(relative, "/themes/") ||
		strings.HasSuffix(strings.ToUpper(relative), "/AGENTS.MD") ||
		strings.HasSuffix(strings.ToUpper(relative), "/CLAUDE.MD") ||
		strings.HasSuffix(strings.ToUpper(relative), "/SYSTEM.MD") ||
		strings.HasSuffix(strings.ToUpper(relative), "/APPEND_SYSTEM.MD") ||
		strings.HasSuffix(relative, ".json"):
		return "Pi configuration file"
	default:
		return "unrecognized Pi non-session file"
	}
}

// scanGrokSessions walks Grok Build's session store, which files sessions by the
// URL-encoded working directory they ran in: each project directory holds one
// directory per session, and a session holds its update stream and metadata side
// by side. The metadata comes first on purpose, exactly as it does for Cowork:
// it declares the session's identity and span, and the update stream merges over
// it.
//
// updates.jsonl is the only primary content surface. events.jsonl is lifecycle
// telemetry without conversation text. compaction_requests/ and recap_requests/
// are repeated snapshots assembled from this same update stream; reading them
// would duplicate turns rather than recover ones absent from updates.jsonl.
func scanGrokSessions(roots Roots) []Target {
	var targets []Target
	for _, encodedDir := range subdirectories(roots.GrokSessions) {
		project := projectFromGrokDir(encodedDir)
		exclusion := runnerExclusionGrok(roots, encodedDir)
		full := filepath.Join(roots.GrokSessions, encodedDir)
		for _, session := range subdirectories(full) {
			if !sessionFileName.MatchString(session) {
				continue
			}
			summary := filepath.Join(full, session, "summary.json")
			hasSummary := isFile(summary)
			if hasSummary {
				targets = append(targets, Target{
					Path: summary, Kind: parsers.KindGrokSessionMetadata,
					SourceAgent: "grok", Project: project, SessionID: session,
					FileName: "summary.json", ExclusionReason: exclusion,
				})
			}
			updates := filepath.Join(full, session, "updates.jsonl")
			if isFile(updates) {
				targets = append(targets, Target{
					Path: updates, Kind: parsers.KindGrokSession,
					SourceAgent: "grok", Project: project, SessionID: session,
					FileName: "updates.jsonl", ExclusionReason: exclusion,
				})
				if hasSummary {
					// A session without its metadata still ingests: the update stream
					// falls back to the session directory's own identity, and the
					// pair is only ever offered when it is actually there.
					targets[len(targets)-1].SidecarPath = summary
				}
			}
		}
	}
	return targets
}

// projectFromGrokDir decodes the URL-escaped working directory a Grok project
// directory names. Grok's encoding is lossless, so the decoded path is the real
// one and its last segment is the project; there is no ambiguity for a declared
// root to resolve.
func projectFromGrokDir(name string) string {
	decoded, err := url.PathUnescape(name)
	if err != nil || decoded == "" {
		return ""
	}
	return ProjectFromCwd(decoded)
}

// runnerExclusionGrok is runnerExclusion for Grok's URL-escaped directory names.
// It decodes the directory instead of re-escaping the runner root, so the runner
// store stays excluded even when Grok escapes a path differently from Go.
func runnerExclusionGrok(roots Roots, encodedDir string) string {
	if roots.RunnerDir == "" {
		return ""
	}
	decoded, err := url.PathUnescape(encodedDir)
	if err != nil || decoded == "" {
		return ""
	}
	decoded = cleanRoot(decoded)
	for _, path := range []string{roots.RunnerDir, realPath(roots.RunnerDir)} {
		if decoded == cleanRoot(path) {
			return "La Roca local-binary runner session is excluded"
		}
	}
	return ""
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

// filesIn is the regular files of a directory a source may or may not have. A
// missing or unreadable one is an empty answer, which is the normal state of a
// machine that does not run that agent.
func filesIn(dir string) []string {
	names, _ := readFiles(dir)
	return names
}

// readFiles is filesIn for a directory the operator declared, where being
// unreadable is a fact about that declaration and not a machine without the
// agent, so the caller can say so.
func readFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, entry := range entries {
		if entry.Type().IsRegular() {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	return names, nil
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
