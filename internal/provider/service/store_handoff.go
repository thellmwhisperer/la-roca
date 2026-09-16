package service

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Session harnesses are the interactive runtimes this product already stamps
// into source_agent. A handoff is a session-continuity record, not a worker
// progress receipt.
var sessionHandoffHarnesses = map[string]bool{
	"claude": true, "claude-code": true,
	"codex": true, "cursor": true, "grok": true,
	"hermes": true, "opencode": true, "pi": true, "qwen": true,
}

// sessionAgentAliases map MCP clientInfo names and other runtime aliases onto
// the canonical harness the allowlist already knows.
var sessionAgentAliases = map[string]string{
	"claude-desktop": "claude",
	"claude desktop": "claude",
	"cowork":         "claude",
	"claude-cowork":  "claude",
	"claude cowork":  "claude",
	"claude code":    "claude",
	"claude-code":    "claude",
	"claude-ai":      "claude",
	"claude ai":      "claude",
	"codex-cli":      "codex",
	"codex cli":      "codex",
	"hermes-agent":   "hermes",
	"pi-signed":      "pi",
	"pi-launcher":    "pi",
}

var sessionHandoffSurfaces = map[string]bool{
	SurfaceCLI: true,
	SurfaceMCP: true,
}

var handoffShapeLabel = regexp.MustCompile(
	`(?i)\b(branch/scope|branch|scope|done|current\s+state|state|next\s+step|next)\s*:`,
)

var requiredHandoffFields = []string{"branch/scope", "done", "state", "next"}

const acceptedHandoffLabels = "branch/scope:, done:, state: (or current state:), and next:"

const acceptedHandoffExample = `layer=handoff content="branch: main\ndone: recorded\nstate: stored\nnext: continue"`

// CanonicalSessionAgent maps a client or flag name onto the harness the
// session-writer allowlist knows. Unknown names stay as the trimmed lowercase
// form so a refusal can name what it saw.
func CanonicalSessionAgent(name string) string {
	agent := strings.ToLower(strings.TrimSpace(name))
	if agent == "" {
		return ""
	}
	if canonical, ok := sessionAgentAliases[agent]; ok {
		return canonical
	}
	return agent
}

func refuseHandoffWrite(physical string, origin string, authorship Authorship, content string) error {
	if physical != "handoff" {
		return nil
	}
	if err := refuseHandoffWriter(origin, authorship); err != nil {
		return err
	}
	return refuseHandoffShape(content)
}

func refuseHandoffWriter(origin string, authorship Authorship) error {
	agent := CanonicalSessionAgent(authorship.Agent)
	surface := strings.ToLower(strings.TrimSpace(authorship.Surface))
	if (origin == "human" || origin == "agent") &&
		sessionHandoffHarnesses[agent] && sessionHandoffSurfaces[surface] {
		return nil
	}
	return fmt.Errorf(
		"handoff refused: agent=%q surface=%q origin=%q; session writers are %s writing from cli or mcp; "+
			"progress belongs in tasks-axi; delivery belongs in the pr field; "+
			"a session decision belongs in layer decision; job state belongs in a layer with expires_at; "+
			"accepted shape uses the labels %s; example: %s",
		valueOr(agent, UnknownAuthor), valueOr(surface, UnknownAuthor), valueOr(origin, UnknownAuthor),
		sessionWriterNames(), acceptedHandoffLabels, acceptedHandoffExample)
}

func sessionWriterNames() string {
	names := make([]string, 0, len(sessionHandoffHarnesses))
	for name := range sessionHandoffHarnesses {
		names = append(names, name)
	}
	slices.Sort(names)
	return strings.Join(names, ", ")
}

func refuseHandoffShape(content string) error {
	matches := handoffShapeLabel.FindAllStringSubmatchIndex(content, -1)
	populated := map[string]bool{}
	for i, match := range matches {
		valueEnd := len(content)
		if i+1 < len(matches) {
			valueEnd = matches[i+1][0]
		}
		if strings.TrimSpace(content[match[1]:valueEnd]) == "" {
			continue
		}
		label := strings.ToLower(strings.Join(strings.Fields(content[match[2]:match[3]]), " "))
		switch label {
		case "branch/scope", "branch", "scope":
			populated["branch/scope"] = true
		case "current state", "state":
			populated["state"] = true
		case "next step", "next":
			populated["next"] = true
		default:
			populated[label] = true
		}
	}
	missing := make([]string, 0, len(requiredHandoffFields))
	for _, field := range requiredHandoffFields {
		if !populated[field] {
			missing = append(missing, field)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf(
		"a handoff must use the labels %s (missing or blank %s); a SUPERSEDE in prose goes with --supersedes",
		acceptedHandoffLabels, strings.Join(missing, ", "))
}
