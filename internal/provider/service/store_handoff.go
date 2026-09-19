package service

import (
	"cmp"
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"
	"time"
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

func refuseHandoffWrite(physical string, origin string, authorship Authorship) error {
	if physical != "handoff" {
		return nil
	}
	return refuseHandoffWriter(origin, authorship)
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
			"recommended shape uses the labels %s; example: %s",
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

type handoffAutoSupersede struct {
	currentID         int64
	currentSupersedes any
	currentIDs        []int64
	keepID            int64
}

// planHandoffAutoSupersede fills the one-current-per-project contract for an
// active named project.
func planHandoffAutoSupersede(ctx context.Context, db memoryQuerier, physical string, req StoreRequest, status string) (handoffAutoSupersede, error) {
	if physical != "handoff" || status != "active" {
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
	ids := make([]int64, len(heads))
	for i, head := range heads {
		ids[i] = head.id
	}
	if req.Supersedes != 0 {
		for _, head := range heads {
			if head.id == req.Supersedes {
				return handoffAutoSupersede{currentIDs: ids, keepID: req.Supersedes}, nil
			}
		}
		return handoffAutoSupersede{currentIDs: ids}, nil
	}
	current := heads[len(heads)-1]
	return handoffAutoSupersede{
		currentID: current.id, currentSupersedes: current.supersedes, currentIDs: ids, keepID: current.id,
	}, nil
}

type currentHandoffHead struct {
	id             int64
	supersedes     any
	createdAt      time.Time
	createdAtValid bool
}

func currentProjectHandoffs(ctx context.Context, db memoryQuerier, project string) ([]currentHandoffHead, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT candidate.id, candidate.supersedes, candidate.created_at
		FROM memories AS candidate
		WHERE candidate.layer = 'handoff'
		  AND candidate.status = 'active'
		  AND candidate.project = ?
		  AND NOT EXISTS (
		      SELECT 1 FROM memories AS replacement
		      WHERE replacement.supersedes = candidate.id
		  )
		`, project)
	if err != nil {
		return nil, fmt.Errorf("look for the current project handoffs: %w", err)
	}
	defer rows.Close()
	var heads []currentHandoffHead
	for rows.Next() {
		var head currentHandoffHead
		var supersedes sql.NullInt64
		var createdAt sql.NullString
		if err := rows.Scan(&head.id, &supersedes, &createdAt); err != nil {
			return nil, fmt.Errorf("read the current project handoffs: %w", err)
		}
		if supersedes.Valid {
			head.supersedes = supersedes.Int64
		}
		head.createdAt, head.createdAtValid = normalizeCreatedAt(createdAt.String)
		heads = append(heads, head)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read the current project handoffs: %w", err)
	}
	slices.SortFunc(heads, func(left, right currentHandoffHead) int {
		if left.createdAtValid != right.createdAtValid {
			if left.createdAtValid {
				return -1
			}
			return 1
		}
		if left.createdAtValid {
			if left.createdAt.Before(right.createdAt) {
				return -1
			}
			if left.createdAt.After(right.createdAt) {
				return 1
			}
		}
		return cmp.Compare(left.id, right.id)
	})
	return heads, nil
}

func repairHandoffHeads(ctx context.Context, db *sql.Tx, currentIDs []int64, keepID int64) error {
	for _, id := range currentIDs {
		if id == keepID {
			continue
		}
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
