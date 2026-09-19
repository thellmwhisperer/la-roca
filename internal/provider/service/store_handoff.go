package service

import (
	"context"
	"database/sql"
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
	"zcode": true,
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
	"cursor-ide":     "cursor",
	"qwen-code":      "qwen",
	"qwencode":       "qwen",
}

var sessionHandoffSurfaces = map[string]bool{
	SurfaceCLI: true,
	SurfaceMCP: true,
}

var handoffShapeLabel = regexp.MustCompile(
	`(?i)\b(branch/scope|branch|scope|done|current\s+state|state|next)\s*:`,
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

// mcpAgentLookupNames are the lowercase agent identities that count as the
// same MCP writer. CLI writes stay exact: a differing harness name is a
// different author.
func mcpAgentLookupNames(agent string) []string {
	canonical := CanonicalSessionAgent(agent)
	names := []string{strings.ToLower(strings.TrimSpace(agent))}
	if canonical != "" {
		names = append(names, canonical)
	}
	for alias, target := range sessionAgentAliases {
		if target == canonical {
			names = append(names, alias)
		}
	}
	slices.Sort(names)
	return slices.Compact(names)
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
		case "next":
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

type handoffAutoSupersede struct {
	currentID         int64
	currentSupersedes any
	currentIDs        []int64
}

// planHandoffAutoSupersede fills the one-current-per-project contract when the
// writer did not name a predecessor. An explicit supersedes, a non-handoff
// layer, and a project-less write stay as the caller sent them.
func planHandoffAutoSupersede(ctx context.Context, db memoryQuerier, physical string, req StoreRequest, status string) (handoffAutoSupersede, error) {
	if physical != "handoff" || status != "active" || req.Supersedes != 0 {
		return handoffAutoSupersede{}, nil
	}
	project := req.Project
	if project == "" {
		return handoffAutoSupersede{}, nil
	}
	heads, err := currentProjectHandoffs(ctx, db, project)
	if err != nil || len(heads) == 0 {
		return handoffAutoSupersede{}, err
	}
	current := heads[len(heads)-1]
	ids := make([]int64, len(heads))
	for i, head := range heads {
		ids[i] = head.id
	}
	return handoffAutoSupersede{
		currentID: current.id, currentSupersedes: current.supersedes, currentIDs: ids,
	}, nil
}

type currentHandoffHead struct {
	id         int64
	supersedes any
}

func currentProjectHandoffs(ctx context.Context, db memoryQuerier, project string) ([]currentHandoffHead, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT candidate.id, candidate.supersedes
		FROM memories AS candidate
		WHERE candidate.layer = 'handoff'
		  AND candidate.status = 'active'
		  AND candidate.project = ?
		  AND NOT EXISTS (
		      SELECT 1 FROM memories AS replacement
		      WHERE replacement.supersedes = candidate.id
		  )
		ORDER BY candidate.created_at ASC, candidate.id ASC`, project)
	if err != nil {
		return nil, fmt.Errorf("look for the current project handoffs: %w", err)
	}
	defer rows.Close()
	var heads []currentHandoffHead
	for rows.Next() {
		var head currentHandoffHead
		var supersedes sql.NullInt64
		if err := rows.Scan(&head.id, &supersedes); err != nil {
			return nil, fmt.Errorf("read the current project handoffs: %w", err)
		}
		if supersedes.Valid {
			head.supersedes = supersedes.Int64
		}
		heads = append(heads, head)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read the current project handoffs: %w", err)
	}
	return heads, nil
}

// repairHandoffHeads retires every current head except the newest, which the
// incoming store will supersede. It never overwrites a head that already names
// a predecessor: that rewrite would drop the old pointer and resurrect it.
func repairHandoffHeads(ctx context.Context, db *sql.Tx, currentIDs []int64) error {
	for _, id := range currentIDs[:max(0, len(currentIDs)-1)] {
		if err := retireHandoffHead(ctx, db, id); err != nil {
			return err
		}
	}
	return nil
}

func retireHandoffHead(ctx context.Context, db *sql.Tx, id int64) error {
	var n int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM memories WHERE supersedes = ?`, id).Scan(&n); err != nil {
		return fmt.Errorf("check whether a handoff head is already retired: %w", err)
	}
	if n > 0 {
		return nil
	}
	var project sql.NullString
	if err := db.QueryRowContext(ctx,
		`SELECT project FROM memories WHERE id = ?`, id).Scan(&project); err != nil {
		return fmt.Errorf("read the retired handoff project: %w", err)
	}
	_, err := db.ExecContext(ctx, `
		INSERT INTO memories (layer, content, origin, source_agent, source_model, source_surface, project, status, supersedes)
		VALUES ('handoff-retirement', 'retired a parallel current handoff', 'agent', 'unknown', 'unknown', 'unknown', ?, 'resolved', ?)`,
		project, id)
	if err != nil {
		return fmt.Errorf("retire a parallel current handoff: %w", err)
	}
	return nil
}
