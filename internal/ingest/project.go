// Package ingest is the impure half: it scans the disk, opens the databases the
// other agents keep, and writes. The parsing lives apart, in parsers/, which
// never touches any of that.
//
// # How the files are named on each side of that line
//
// A file in parsers/ is named after the artefact format it decodes, because
// that is the whole of what it knows: parsers/codex.go reads the bytes of a
// Codex rollout. A file here is named after the job it does against the world,
// because a source is more than its format: codex_state.go opens the state
// database Codex keeps beside its rollouts and enriches records the rollout
// parser already produced.
//
// The two are never given the same name. `ingest/codex.go` next to
// `parsers/codex.go` cost a wave the question of which one a stack trace meant,
// and the answer was not in either file: format parser and source adapter are
// different roles, and a name that hides the difference is the smell, not the
// duplication.
package ingest

import (
	"sort"
	"strings"
	"unicode"
)

// The agents encode a working directory as a directory name by replacing the
// separators with dashes. `/w/demo` becomes `-w-demo` and `C:\code\demo` becomes
// `C--code-demo`. The encoding is lossy: `-w-demo` could be `/w/demo` or
// `/w-demo`, so it is only decoded relative to a declared workspace root, and
// what no root explains stays unnamed.

// mntPrefix is where WSL mounts the Windows drives. It is what makes a
// repository under `C:\` and the same repository under `/mnt/c/` two spellings of
// one directory, and it is the reason a root declared in either spelling has to
// explain a directory encoded in the other.
const mntPrefix = "/mnt/"

// WorkspaceRoots are the operator's declared roots, canonicalized once and kept
// with every encoding each of them can be recognized by.
type WorkspaceRoots struct {
	// Selected are the roots as they will be scanned, in declaration order.
	Selected []string
	// encodings are sorted longest first, so the most specific root wins.
	encodings []rootEncoding
}

type rootEncoding struct {
	encoded string
	// caseInsensitive is true for the Windows spellings, because Windows paths
	// are.
	caseInsensitive bool
}

// ResolveWorkspaceRoots canonicalizes what the operator declared: trims, drops
// the empties, removes a trailing separator and keeps one entry per root.
func ResolveWorkspaceRoots(declared []string) WorkspaceRoots {
	var roots WorkspaceRoots
	seen := map[string]bool{}
	for _, raw := range declared {
		root := cleanRoot(raw)
		if root == "" || seen[root] {
			continue
		}
		seen[root] = true
		roots.Selected = append(roots.Selected, root)
		// A root that names no directory of its own explains nothing: decoding
		// against the filesystem root would leave the whole encoded remainder and
		// file that path as if it were a project name. It is still scanned.
		if baseName(root) == "" {
			continue
		}
		for _, spelling := range spellings(root) {
			windows := isWindowsSyntax(spelling)
			roots.encodings = append(roots.encodings, rootEncoding{
				encoded:         encodeRoot(spelling),
				caseInsensitive: windows,
			})
		}
	}
	sort.SliceStable(roots.encodings, func(i, j int) bool {
		return len(roots.encodings[i].encoded) > len(roots.encodings[j].encoded)
	})
	return roots
}

// spellings are the ways one directory can be written on the machine that
// declared it. A path on a Windows drive has two under WSL, and an agent running
// on either side of that bridge encoded the one it saw.
func spellings(root string) []string {
	all := []string{root}
	if drive, rest, ok := cutDrive(root); ok {
		all = append(all, mntPrefix+strings.ToLower(drive)+"/"+
			strings.ReplaceAll(strings.TrimPrefix(rest, `\`), `\`, "/"))
		return all
	}
	if rest, ok := cutMnt(root); ok {
		drive, path, _ := strings.Cut(rest, "/")
		all = append(all, strings.ToUpper(drive)+`:\`+strings.ReplaceAll(path, "/", `\`))
	}
	return all
}

// encodeRoot is the agents' own encoding: the separators become dashes, and on
// Windows the colon after the drive letter does too.
func encodeRoot(root string) string {
	if isWindowsSyntax(root) {
		return strings.NewReplacer(":", "-", `\`, "-").Replace(root)
	}
	return strings.ReplaceAll(root, "/", "-")
}

// ProjectFromEncodedDir decodes an agent's project directory name.
//
// The second value is false when the name is an encoded absolute path that no
// declared root explains. That is not an error and not a project either: it is
// the ambiguous case the operator fixes by declaring the root, and the caller
// turns it into a diagnosis that names that remedy.
func ProjectFromEncodedDir(name string, roots WorkspaceRoots) (string, bool) {
	if name == "" {
		return "", true
	}
	if !isEncodedAbsolute(name) {
		return name, true
	}
	for _, root := range roots.encodings {
		prefix := root.encoded + "-"
		candidate, ok := trimEncodedPrefix(name, prefix, root.caseInsensitive)
		if !ok || candidate == "" || isEncodedAbsolute(candidate) {
			continue
		}
		return candidate, true
	}
	return "", false
}

// ProjectFromPath decodes the project out of the directory a session file lives
// in. It is the last resort, behind what the artefact's own metadata declares.
func ProjectFromPath(path string, roots WorkspaceRoots) (string, bool) {
	return ProjectFromEncodedDir(baseName(dirName(path)), roots)
}

// ProjectFromCwd is the project a working directory names: its last segment, in
// either path syntax. An empty directory names no project, which is what
// baseName already answers.
func ProjectFromCwd(cwd string) string { return baseName(cwd) }

// ProjectFromMetadataCwd is the same, except that a cwd inside the runtime's own
// session store names no project. Filing those sessions under "sessions" would
// invent a project nobody has.
func ProjectFromMetadataCwd(cwd string) string {
	if !isWindowsSyntax(cwd) && strings.HasPrefix(cwd, "/sessions/") {
		return ""
	}
	return baseName(cwd)
}

// isEncodedAbsolute recognizes the two encodings: a POSIX path, which starts with
// the dash its leading separator became, and a Windows one, which starts with a
// drive letter followed by the two dashes its colon and separator became.
func isEncodedAbsolute(name string) bool {
	if strings.HasPrefix(name, "-") {
		return true
	}
	return len(name) >= 3 && unicode.IsLetter(rune(name[0])) && name[1:3] == "--"
}

func trimEncodedPrefix(name, prefix string, caseInsensitive bool) (string, bool) {
	if caseInsensitive {
		if len(name) < len(prefix) ||
			!strings.EqualFold(name[:len(prefix)], prefix) {
			return "", false
		}
		return name[len(prefix):], true
	}
	if !strings.HasPrefix(name, prefix) {
		return "", false
	}
	return name[len(prefix):], true
}

// --- path syntax, without asking the host which one it is ---
//
// A corpus is ingested on the machine that produced it, but a path inside a
// transcript can name either syntax, and the resolution has to be testable for
// every platform on any platform.

func isWindowsSyntax(path string) bool {
	if strings.Contains(path, `\`) {
		return true
	}
	_, _, ok := cutDrive(path)
	return ok
}

// cutDrive splits `C:\rest` into its drive letter and the rest.
func cutDrive(path string) (string, string, bool) {
	if len(path) < 2 || path[1] != ':' || !unicode.IsLetter(rune(path[0])) {
		return "", "", false
	}
	return path[:1], path[2:], true
}

// cutMnt splits `/mnt/c/rest` into `c/rest`.
func cutMnt(path string) (string, bool) {
	if !strings.HasPrefix(path, mntPrefix) {
		return "", false
	}
	rest := path[len(mntPrefix):]
	drive, _, found := strings.Cut(rest, "/")
	if !found || len(drive) != 1 || !unicode.IsLetter(rune(drive[0])) {
		return "", false
	}
	return rest, true
}

// cleanRoot trims the surrounding spaces and the trailing separator, which is
// what makes "/w" and "/w/" one root and not two.
func cleanRoot(raw string) string {
	root := strings.TrimSpace(raw)
	if root == "" {
		return ""
	}
	for len(root) > 1 && (strings.HasSuffix(root, "/") || strings.HasSuffix(root, `\`)) {
		root = root[:len(root)-1]
	}
	return root
}

// baseName is the last segment of a path in either syntax.
func baseName(path string) string {
	trimmed := cleanRoot(path)
	if index := strings.LastIndexAny(trimmed, `/\`); index >= 0 {
		return trimmed[index+1:]
	}
	if _, rest, ok := cutDrive(trimmed); ok {
		return rest
	}
	return trimmed
}

// dirName is everything before that last segment.
func dirName(path string) string {
	if index := strings.LastIndexAny(path, `/\`); index >= 0 {
		return path[:index]
	}
	return ""
}
