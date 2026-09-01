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

var handoffShapeLabels = []struct {
	name    string
	pattern *regexp.Regexp
}{
	{"branch/scope", regexp.MustCompile(`(?i)\b(?:branch/scope|branch|scope)\s*:`)},
	{"done", regexp.MustCompile(`(?i)\bdone\s*:`)},
	{"state", regexp.MustCompile(`(?i)\b(?:current\s+state|state)\s*:`)},
	{"next", regexp.MustCompile(`(?i)\b(?:next\s+step|next)\s*:`)},
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
	missing := make([]string, 0, len(handoffShapeLabels))
	for _, label := range handoffShapeLabels {
		if !label.pattern.MatchString(content) {
			missing = append(missing, label.name)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf(
		"a handoff must name branch/scope, done, current state, and next step (missing %s); "+
			"declare replacement with --supersedes, not in prose",
		strings.Join(missing, ", "))
}
