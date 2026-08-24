package toolcallobserver

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/thellmwhisperer/la-roca/internal/ingest"
	"github.com/thellmwhisperer/la-roca/pkg/parsers"
)

// Process is one ancestor of the observer, nearest parent first.
type Process struct {
	Command     string
	Arguments   []string
	Environment map[string]string
	Cwd         string
	OpenFiles   []string
}

// Evidence is the fact table the resolver reads. Nothing here is inferred.
type Evidence struct {
	Processes   []Process
	Environment map[string]string
	Roots       ingest.Roots
}

// Session is the invoking session file the ingest scan already knows.
type Session struct {
	Harness string
	Kind    parsers.Kind
	Path    string
	ID      string
}

// Resolve finds the session file of the agent that invoked the observer.
// It refuses when that file is not a unique fact.
func Resolve(evidence Evidence) (Session, error) {
	harness := invokingHarness(evidence)
	if harness == "" {
		return Session{}, fmt.Errorf("cannot watch this session: no invoking agent was found in the process tree")
	}
	id := sessionID(harness, evidence.Environment)
	if id == "" && hasSessionEnvironment(harness) {
		return Session{}, fmt.Errorf("cannot watch this session: %s is running but its session identity is not in the process environment",
			ProductName(harness))
	}
	var matches []ingest.Target
	if id != "" {
		matches = sessionMatches(harness, id, evidence.Roots)
	} else {
		matches = openFileMatches(harness, evidence)
	}
	if len(matches) == 0 {
		if id != "" {
			return Session{}, fmt.Errorf("cannot watch this session: the %s transcript for this session is not in the session store",
				ProductName(harness))
		}
		return Session{}, fmt.Errorf("cannot watch this session: %s is running but its session identity is not in the process environment and no unique open session file names it",
			ProductName(harness))
	}
	if len(matches) != 1 {
		if id != "" {
			return Session{}, fmt.Errorf("cannot watch this session: more than one %s transcript matches this session",
				ProductName(harness))
		}
		return Session{}, fmt.Errorf("cannot watch this session: more than one open %s session file matches this session",
			ProductName(harness))
	}
	return Session{Harness: harness, Kind: matches[0].Kind, Path: matches[0].Path, ID: id}, nil
}

func hasSessionEnvironment(harness string) bool {
	switch harness {
	case "claude", "grok", "codex", "hermes":
		return true
	default:
		return false
	}
}

func sessionMatches(harness, id string, roots ingest.Roots) []ingest.Target {
	var matches []ingest.Target
	for _, target := range ingest.Scan(roots).Targets {
		if target.SourceAgent != harness || targetSessionID(target) != id {
			continue
		}
		if target.Kind == parsers.KindGrokSessionMetadata {
			continue
		}
		matches = append(matches, target)
	}
	return matches
}

func targetSessionID(target ingest.Target) string {
	if target.SessionID != "" {
		return target.SessionID
	}
	if target.Kind == parsers.KindPiSession {
		return piSessionID(target.Path)
	}
	return ""
}

func piSessionID(path string) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	if !scanner.Scan() {
		return ""
	}
	var header struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(scanner.Bytes(), &header); err != nil {
		return ""
	}
	return header.ID
}

func openFileMatches(harness string, evidence Evidence) []ingest.Target {
	open := map[string]bool{}
	for _, process := range evidence.Processes {
		for _, file := range process.OpenFiles {
			open[normalizedPath(file)] = true
		}
	}
	var matches []ingest.Target
	for _, target := range ingest.Scan(evidence.Roots).Targets {
		if target.SourceAgent != harness {
			continue
		}
		if target.Kind == parsers.KindGrokSessionMetadata {
			continue
		}
		if open[normalizedPath(target.Path)] {
			matches = append(matches, target)
		}
	}
	return matches
}

func normalizedPath(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return filepath.Clean(path)
}

func invokingHarness(evidence Evidence) string {
	for _, process := range evidence.Processes {
		if name := harnessFromCommand(process.Command, process.Arguments); name != "" {
			return name
		}
	}
	found := envHarnesses(evidence.Environment)
	if len(found) == 1 {
		return found[0]
	}
	return ""
}

func harnessFromCommand(command string, arguments []string) string {
	names := []string{command}
	if len(arguments) > 0 {
		names = append(names, arguments[0])
	}
	for _, raw := range names {
		name := strings.ToLower(strings.TrimSuffix(filepath.Base(raw), ".exe"))
		switch {
		case name == "claude" || name == "claude-code":
			return "claude"
		case name == "grok":
			return "grok"
		case name == "codex" || strings.HasPrefix(name, "codex-"):
			return "codex"
		case name == "opencode":
			return "opencode"
		case name == "pi" || name == "pi-signed" || name == "pi-launcher":
			return "pi"
		case name == "cursor" || name == "cursor-agent":
			return "cursor"
		case name == "hermes" || name == "hermes-agent":
			return "hermes"
		}
	}
	return ""
}

func envHarnesses(environment map[string]string) []string {
	var found []string
	if environment["CLAUDECODE"] == "1" || strings.TrimSpace(environment["CLAUDE_CODE_SESSION_ID"]) != "" {
		found = append(found, "claude")
	}
	if environment["GROK_AGENT"] == "1" || strings.TrimSpace(environment["GROK_SESSION_ID"]) != "" {
		found = append(found, "grok")
	}
	if strings.TrimSpace(environment["CODEX_THREAD_ID"]) != "" {
		found = append(found, "codex")
	}
	if strings.TrimSpace(environment["HERMES_SESSION_ID"]) != "" {
		found = append(found, "hermes")
	}
	if strings.TrimSpace(environment["PI_SESSION_ID"]) != "" {
		found = append(found, "pi")
	}
	return found
}

func sessionID(harness string, environment map[string]string) string {
	switch harness {
	case "claude":
		return strings.TrimSpace(environment["CLAUDE_CODE_SESSION_ID"])
	case "grok":
		return strings.TrimSpace(environment["GROK_SESSION_ID"])
	case "codex":
		return strings.TrimSpace(environment["CODEX_THREAD_ID"])
	case "hermes":
		return strings.TrimSpace(environment["HERMES_SESSION_ID"])
	case "pi":
		return strings.TrimSpace(environment["PI_SESSION_ID"])
	default:
		return ""
	}
}

func ProductName(harness string) string {
	switch harness {
	case "claude":
		return "Claude Code"
	case "grok":
		return "Grok"
	case "codex":
		return "Codex"
	case "opencode":
		return "OpenCode"
	case "pi":
		return "Pi"
	case "cursor":
		return "Cursor"
	case "hermes":
		return "Hermes"
	default:
		return harness
	}
}
