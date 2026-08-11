package ingest

import (
	"context"
	"database/sql"
	"path/filepath"
	"slices"
	"strings"
)

// Codex keeps a state database next to its rollouts. It is read for one reason:
// the rollout does not say which agent ran it, and the state database does. That
// is the difference between a corpus where every Codex session is `codex` and one
// where each named agent can be asked about.

// codexThreadColumns are the columns worth reading. Only the ones this build finds
// are selected: Codex has migrated its state schema before, and a reader that
// demanded all of them would stop enriching anything the day it adds one.
var codexThreadColumns = []string{
	"model_provider", "model", "git_branch", "git_origin_url", "tokens_used",
	"agent_nickname", "agent_role", "reasoning_effort", "cli_version",
	"sandbox_policy", "approval_mode",
}

// codexEnrichment is what the state database had to say about one rollout.
type codexEnrichment struct {
	Metadata    map[string]any
	SourceAgent string
}

// enrichCodex looks the rollout up in Codex's own state database.
//
// The match is deliberately conservative: the session id or the rollout path has
// to resolve to exactly one thread row. Two rows matching is no answer, and
// picking one would attribute a session to whichever agent sorted first. A missing
// database, table or column simply means no enrichment.
func enrichCodex(ctx context.Context, statePath, sessionID, rolloutPath string) codexEnrichment {
	if statePath == "" || !isFile(statePath) {
		return codexEnrichment{}
	}
	db, err := openForeign(statePath)
	if err != nil {
		return codexEnrichment{}
	}
	defer db.Close()

	columns, err := tableColumns(ctx, db, "threads")
	if err != nil || !columns["id"] {
		return codexEnrichment{}
	}
	selected := []string{"id"}
	for _, name := range append([]string{"rollout_path"}, codexThreadColumns...) {
		if columns[name] {
			selected = append(selected, name)
		}
	}

	var conditions []string
	var args []any
	if sessionID != "" {
		conditions = append(conditions, "id = ?")
		args = append(args, sessionID)
	}
	if rolloutPath != "" && columns["rollout_path"] {
		absolute, err := filepath.Abs(rolloutPath)
		if err == nil {
			conditions = append(conditions, "rollout_path = ?")
			args = append(args, absolute)
		}
	}
	if len(conditions) == 0 {
		return codexEnrichment{}
	}

	statement := "SELECT " + strings.Join(selected, ", ") + " FROM threads WHERE " +
		strings.Join(conditions, " OR ") + " LIMIT 2"
	rows, err := queryRows(ctx, db, statement, args...)
	if err != nil || len(rows) != 1 {
		return codexEnrichment{}
	}

	found := rows[0]
	enrichment := codexEnrichment{Metadata: map[string]any{}}
	for _, name := range codexThreadColumns {
		if !found.has(name) {
			continue
		}
		if text := found.text(name); text != "" {
			enrichment.Metadata[name] = text
			continue
		}
		if number, ok := found.number(name); ok {
			enrichment.Metadata[name] = number
		}
	}
	if id := found.text("id"); id != "" {
		enrichment.Metadata["codex_thread_id"] = id
	}
	if path := found.text("rollout_path"); path != "" {
		enrichment.Metadata["codex_rollout_path"] = path
	}
	enrichment.SourceAgent = codexSourceAgent(found.text("agent_nickname"))
	enrichment.mergeSpawnEdges(ctx, db, found.text("id"))
	return enrichment
}

// mergeSpawnEdges records the delegation tree Codex keeps apart from the threads:
// which thread spawned this one and which ones it spawned. It is what makes a
// child rollout findable from its parent.
func (e *codexEnrichment) mergeSpawnEdges(ctx context.Context, db *sql.DB, threadID string) {
	if threadID == "" {
		return
	}
	columns, err := tableColumns(ctx, db, "thread_spawn_edges")
	if err != nil || !columns["parent_thread_id"] || !columns["child_thread_id"] {
		return
	}
	rows, err := queryRows(ctx, db,
		`SELECT parent_thread_id, child_thread_id FROM thread_spawn_edges
		 WHERE parent_thread_id = ? OR child_thread_id = ?
		 ORDER BY parent_thread_id, child_thread_id`, threadID, threadID)
	if err != nil || len(rows) == 0 {
		return
	}
	var parents, children []string
	for _, edge := range rows {
		parent, child := edge.text("parent_thread_id"), edge.text("child_thread_id")
		if child == threadID && parent != "" && !slices.Contains(parents, parent) {
			parents = append(parents, parent)
		}
		if parent == threadID && child != "" && !slices.Contains(children, child) {
			children = append(children, child)
		}
	}
	if len(parents) > 0 {
		e.Metadata["codex_parent_thread_ids"] = parents
	}
	if len(children) > 0 {
		e.Metadata["codex_child_thread_ids"] = children
	}
}

// codexSourceAgent keeps attribution on the closed family name. The nickname is
// retained independently in metadata, where it can refine a query without
// inventing a new family value in sessions.source_agent.
func codexSourceAgent(nickname string) string {
	return "codex"
}
