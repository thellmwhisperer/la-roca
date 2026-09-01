package service

import (
	"fmt"
	"regexp"
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

var sessionHandoffSurfaces = map[string]bool{
	SurfaceCLI: true,
	SurfaceMCP: true,
}

var handoffShapeLabel = regexp.MustCompile(
	`(?i)\b(branch/scope|branch|scope|done|current\s+state|state|next\s+step|next)\s*:`,
)

var requiredHandoffFields = []string{"branch/scope", "done", "state", "next"}

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
	agent := strings.ToLower(strings.TrimSpace(authorship.Agent))
	surface := strings.ToLower(strings.TrimSpace(authorship.Surface))
	if (origin == "human" || origin == "agent") &&
		sessionHandoffHarnesses[agent] && sessionHandoffSurfaces[surface] {
		return nil
	}
	return fmt.Errorf(
		"handoff is reserved for session writers (a session harness writing from cli or mcp); " +
			"progress belongs in tasks-axi; delivery belongs in the pr field; " +
			"a session decision belongs in layer decision; job state belongs in a layer with expires_at")
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
		"a handoff must name branch/scope, done, current state, and next step (missing or blank %s); "+
			"declare replacement with --supersedes, not in prose",
		strings.Join(missing, ", "))
}
