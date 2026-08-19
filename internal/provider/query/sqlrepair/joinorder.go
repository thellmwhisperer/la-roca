package sqlrepair

import (
	"strings"

	rqlite "github.com/rqlite/sql"
)

// qualifyJoinOrderBy qualifies a bare ORDER BY column after a JOIN when
// exactly one selected or grouped expression owns that name. Explicit SELECT
// aliases win. When two tables expose the same bare name the statement is
// left alone so the gate can reject it as ambiguous.
//
// Qualification is applied at our scanner's byte offsets. The parser's
// NamePos is a rune offset, which drifts on a non-ASCII literal such as 'é'.
func qualifyJoinOrderBy(stmt string) (string, bool) {
	sel := parseSelect(stmt)
	if sel == nil || sel.Compound != nil || len(sel.OrderingTerms) == 0 || !sourceHasJoin(sel.Source) {
		return stmt, false
	}
	aliases := map[string]struct{}{}
	candidates := map[string][]string{}
	for _, col := range sel.Columns {
		if col.Alias != nil {
			aliases[strings.ToLower(col.Alias.Name)] = struct{}{}
		}
		addOrderCandidate(candidates, col.Expr)
	}
	for _, expr := range sel.GroupByExprs {
		addOrderCandidate(candidates, expr)
	}

	qualify := map[string]string{}
	for _, term := range sel.OrderingTerms {
		ident, ok := term.X.(*rqlite.Ident)
		if !ok {
			continue
		}
		name := strings.ToLower(ident.Name)
		if _, aliased := aliases[name]; aliased {
			continue
		}
		tables := candidates[name]
		if len(tables) != 1 {
			continue
		}
		qualify[name] = tables[0]
	}
	if len(qualify) == 0 {
		return stmt, false
	}

	tokens := topLevelTokens(stmt)
	orderAt := firstPair(tokens, 0, len(stmt), "order", "by")
	if orderAt < 0 {
		return stmt, false
	}
	end := len(stmt)
	if limitAt := firstWord(tokens, orderAt, len(stmt), "limit"); limitAt >= 0 {
		end = limitAt
	} else if offsetAt := firstWord(tokens, orderAt, len(stmt), "offset"); offsetAt >= 0 {
		end = offsetAt
	}

	type prefix struct {
		at    int
		table string
	}
	var prefixes []prefix
	for _, tok := range tokens {
		if tok.start < orderAt || tok.end > end {
			continue
		}
		table, ok := qualify[tok.word]
		if !ok {
			continue
		}
		if tok.start > 0 && stmt[tok.start-1] == '.' {
			continue
		}
		if tok.end < len(stmt) && stmt[tok.end] == '.' {
			continue
		}
		prefixes = append(prefixes, prefix{at: tok.start, table: table})
	}
	if len(prefixes) == 0 {
		return stmt, false
	}
	out := stmt
	for i := len(prefixes) - 1; i >= 0; i-- {
		at := prefixes[i].at
		out = out[:at] + prefixes[i].table + "." + out[at:]
	}
	return out, true
}

func addOrderCandidate(candidates map[string][]string, expr rqlite.Expr) {
	ref, ok := expr.(*rqlite.QualifiedRef)
	if !ok || ref.Table == nil || ref.Column == nil || ref.Star.IsValid() {
		return
	}
	name := strings.ToLower(ref.Column.Name)
	table := ref.Table.Name
	for _, existing := range candidates[name] {
		if strings.EqualFold(existing, table) {
			return
		}
	}
	candidates[name] = append(candidates[name], table)
}

func sourceHasJoin(src rqlite.Source) bool {
	switch s := src.(type) {
	case *rqlite.JoinClause:
		return true
	case *rqlite.ParenSource:
		return sourceHasJoin(s.X)
	default:
		return false
	}
}
