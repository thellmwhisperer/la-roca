package query

import (
	"fmt"
	"strings"

	"github.com/thellmwhisperer/la-roca/internal/store/search"
)

// Plan is a search the rescue runs: which term, against which layer, with what
// cap. It carries no classification and no agent or project — those were the
// compiler's, and v1 has none.
type Plan struct {
	Template string `json:"template"`
	Term     string `json:"term,omitempty"`
	Layer    string `json:"layer,omitempty"`
	Limit    int    `json:"limit,omitempty"`
}

// TemplateSearchByTerm is the rescue's only plan: the cross-source FTS5 search.
const TemplateSearchByTerm = "search_all_sources_by_term"

const (
	defaultLimit = 10
	maxLimit     = 1000
)

// RenderSQLFTS compiles the term search against the FTS5 lexical index,
// requiring every word (AND). It is the precise match the golden bench
// measures.
//
// It requires every word, respects the layer when there is one, excludes the
// search-excluded layers and always comes out with a LIMIT. The shape of the SQL
// is not free: the half of the gate that parses the AST does not accept
// `table.rowid`, so every source pulls its rowid inside a subquery, names it and
// joins on the alias.
func RenderSQLFTS(plan Plan, coordinationLayers []string, limit int) (string, error) {
	return renderFTS(plan, coordinationLayers, limit, search.MatchAll)
}

// RenderSQLFTSAny is the lenient form the keyword rescue uses: it finds rows
// that carry ANY of the question's words (OR), ranked by bm25. A fallback shares
// perhaps one word with a memory, so AND would find nothing where OR finds the
// one memory that matters.
func RenderSQLFTSAny(plan Plan, coordinationLayers []string, limit int) (string, error) {
	return renderFTS(plan, coordinationLayers, limit, search.MatchAny)
}

func renderFTS(plan Plan, coordinationLayers []string, limit int, joiner string) (string, error) {
	expression := search.MatchExpression(plan.Term, joiner)
	if expression == "" {
		return "", fmt.Errorf("the term search needs a term and the question offers none")
	}
	if !isValidLimit(limit) {
		limit = defaultLimit
	}

	memories := fmt.Sprintf(
		"SELECT 'memory' AS source, m.id AS id, m.content AS text, "+
			"m.created_at AS created_at, 0 AS source_priority, f.rango AS rango "+
			"FROM (%s) AS f JOIN memories AS m ON m.id = f.fila "+
			"WHERE m.supersedes IS NULL",
		subquery("memories_fts", expression, "", "", limit))

	if plan.Layer != "" {
		// A layer constraint is always respected, and the other three sources
		// have no layer: returning them would be failing to respect it.
		return fmt.Sprintf("%s AND m.layer = %s ORDER BY rango LIMIT %d",
			memories, literal(plan.Layer), limit), nil
	}
	if len(coordinationLayers) > 0 {
		memories += fmt.Sprintf(" AND m.layer NOT IN (%s)", stringList(coordinationLayers))
	}

	parts := []string{
		memories,
		fmt.Sprintf(
			"SELECT 'exchange', e.id, e.agent_text, e.agent_timestamp, 1 AS source_priority, g.rango "+
				"FROM (%s) AS g JOIN exchanges AS e ON e.id = g.fila",
			subquery("exchanges_fts", expression, "agent_text", "", limit)),
		fmt.Sprintf(
			"SELECT 'human', h.id, h.human_text, h.human_timestamp, 1 AS source_priority, i.rango "+
				"FROM (%s) AS i JOIN exchanges AS h ON h.id = i.fila",
			subquery("exchanges_fts", expression, "human_text",
				"human_text NOT LIKE '<task-notification%'", limit)),
		fmt.Sprintf(
			"SELECT 'thinking', t.id, t.full_text, NULL, 2 AS source_priority, j.rango "+
				"FROM (%s) AS j JOIN thinking_blocks AS t ON t.id = j.fila",
			subquery("thinking_fts", expression, "", "", limit)),
	}
	return strings.Join(parts, " UNION ALL ") +
		fmt.Sprintf(" ORDER BY source_priority, rango LIMIT %d", limit), nil
}

// subquery pulls out of an FTS5 table the identifiers that match, already
// ordered by relevance and capped.
//
// The per-column filter (`{column} : expression`) is what lets the two columns
// of exchanges be queried separately over a single index: without it, a match in
// what the agent said would count as a match in what the human asked.
func subquery(table, expression, column, filter string, limit int) string {
	match := expression
	if column != "" {
		match = fmt.Sprintf("{%s} : (%s)", column, expression)
	}
	where := fmt.Sprintf("%s MATCH %s", table, literal(match))
	if filter != "" {
		where += " AND " + filter
	}
	return fmt.Sprintf(
		"SELECT rowid AS fila, bm25(%s) AS rango FROM %s WHERE %s "+
			"ORDER BY rango LIMIT %d",
		table, table, where, limit)
}

func isValidLimit(limit int) bool { return limit > 0 && limit <= maxLimit }

// limitClause is the LIMIT the LIKE floor carries, falling back to the house cap
// when the plan does not name one.
func limitClause(p Plan, fallback int) string {
	limit := p.Limit
	if !isValidLimit(limit) {
		limit = fallback
	}
	if !isValidLimit(limit) {
		limit = defaultLimit
	}
	return fmt.Sprintf(" LIMIT %d", limit)
}

// RenderSQLLike is the reference floor: a LIKE over every text column, no index.
// It is the competitor the bench measures the FTS index against and the route
// the search falls to when the database is not indexed yet. It requires every
// word of the term and respects the layer, exactly like the index route.
func RenderSQLLike(plan Plan, coordinationLayers []string) (string, error) {
	if strings.TrimSpace(plan.Term) == "" {
		return "", fmt.Errorf("the term search needs a term and the question offers none")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "SELECT 'memory' AS source, id, content AS text, created_at "+
		"FROM memories WHERE %s AND supersedes IS NULL", likeClauses("content", plan.Term))
	if plan.Layer != "" {
		// A layer constraint is always respected, and the other three sources
		// have no layer: returning them would be failing to respect it.
		fmt.Fprintf(&b, " AND layer = %s ORDER BY created_at DESC%s",
			literal(plan.Layer), limitClause(plan, 10))
		return b.String(), nil
	}
	if len(coordinationLayers) > 0 {
		fmt.Fprintf(&b, " AND layer NOT IN (%s)", stringList(coordinationLayers))
	}
	fmt.Fprintf(&b, " UNION ALL SELECT 'exchange', id, agent_text, agent_timestamp AS created_at "+
		"FROM exchanges WHERE %s", likeClauses("agent_text", plan.Term))
	fmt.Fprintf(&b, " UNION ALL SELECT 'human', MIN(id) AS id, human_text, human_timestamp AS created_at "+
		"FROM exchanges WHERE %s AND human_text NOT LIKE '<task-notification%%' "+
		"GROUP BY session_id, human_timestamp, human_text",
		likeClauses("human_text", plan.Term))
	fmt.Fprintf(&b, " UNION ALL SELECT 'thinking', id, full_text, NULL "+
		"FROM thinking_blocks WHERE %s", likeClauses("full_text", plan.Term))
	fmt.Fprintf(&b, " ORDER BY created_at DESC%s", limitClause(plan, 10))
	return b.String(), nil
}

// likeClauses require the column to match every word of the term: searching for
// "guiones largos" is not searching for "guiones" or "largos".
func likeClauses(column, term string) string {
	parts := strings.Split(term, "+")
	clauses := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		clauses = append(clauses, likeClause(column, part))
	}
	return strings.Join(clauses, " AND ")
}

// likeClause looks for the word as it was written and, when it carries
// diacritics, also without them: the corpus has both forms and LIKE folds
// nothing.
func likeClause(column, part string) string {
	variants := []string{part}
	if plain := Fold(part); plain != part {
		variants = append(variants, plain)
	}
	parts := make([]string, 0, len(variants))
	for _, variant := range variants {
		parts = append(parts, fmt.Sprintf("%s LIKE %s ESCAPE '\\'",
			column, literal("%"+escapeLike(variant)+"%")))
	}
	if len(parts) == 1 {
		return parts[0]
	}
	return "(" + strings.Join(parts, " OR ") + ")"
}

// escapeLike declares the wildcards the value carries so a project called
// `100%_done` does not match half the table.
func escapeLike(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, "%", `\%`)
	return strings.ReplaceAll(value, "_", `\_`)
}

// literal is the single-quoted SQL literal, with its apostrophe doubled. It is
// shared by the FTS rendering here and the layer instruction the prompt writes.
func literal(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

// stringList is the comma-separated list of SQL literals a NOT IN (...) clause
// takes.
func stringList(values []string) string {
	quoted := make([]string, 0, len(values))
	for _, v := range values {
		quoted = append(quoted, literal(v))
	}
	return strings.Join(quoted, ", ")
}
