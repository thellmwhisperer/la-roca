package sqlrepair

import (
	"strconv"
	"strings"
	"unicode"

	rqlite "github.com/rqlite/sql"
)

// dropRepeatedOrderBy collapses consecutive top-level ORDER BY clauses that
// have no set operator between them. Local models tack a second trailing
// ORDER BY onto a compound SELECT; the last clause is the one they meant.
func dropRepeatedOrderBy(stmt string) (string, bool) {
	tokens := topLevelTokens(stmt)
	starts := allPairs(tokens, 0, len(stmt), "order", "by")
	for i := 0; i+1 < len(starts); i++ {
		if hasSetOperatorBetween(tokens, starts[i], starts[i+1]) {
			continue
		}
		return strings.TrimSpace(stmt[:starts[i]] + stmt[starts[i+1]:]), true
	}
	return stmt, false
}

// dropUnorderableCompoundTerms drops a trailing compound ORDER BY term that
// SQLite cannot resolve against any result column in the compound.
func dropUnorderableCompoundTerms(stmt string) (string, bool) {
	tokens := topLevelTokens(stmt)
	seps := setSeparators(tokens)
	if len(seps) == 0 {
		return stmt, false
	}
	last := seps[len(seps)-1].end
	orderAt := firstPair(tokens, last, len(stmt), "order", "by")
	if orderAt < 0 {
		return stmt, false
	}
	columns, ok := compoundResultSet(stmt, tokens, seps, orderAt)
	if !ok {
		return stmt, false
	}
	termsStart := -1
	for i, tok := range tokens {
		if tok.start == orderAt && i+1 < len(tokens) && tokens[i+1].word == "by" {
			termsStart = tokens[i+1].end
			break
		}
	}
	if termsStart < 0 {
		return stmt, false
	}
	termsEnd := len(stmt)
	if limitAt := firstWord(tokens, termsStart, len(stmt), "limit"); limitAt >= 0 {
		termsEnd = limitAt
	} else if offsetAt := firstWord(tokens, termsStart, len(stmt), "offset"); offsetAt >= 0 {
		termsEnd = offsetAt
	}
	terms := splitTopLevelCommas(stmt, termsStart, termsEnd)
	kept := make([]string, 0, len(terms))
	for _, term := range terms {
		text := strings.TrimSpace(stmt[term.start:term.end])
		if text == "" {
			continue
		}
		if keepsCompoundOrderTerm(text, columns) {
			kept = append(kept, text)
		}
	}
	if len(kept) == len(terms) {
		return stmt, false
	}
	var rebuilt strings.Builder
	rebuilt.WriteString(strings.TrimRightFunc(stmt[:orderAt], unicode.IsSpace))
	if len(kept) > 0 {
		rebuilt.WriteString(" ORDER BY ")
		rebuilt.WriteString(strings.Join(kept, ", "))
	}
	tail := strings.TrimLeftFunc(stmt[termsEnd:], unicode.IsSpace)
	if tail != "" {
		rebuilt.WriteByte(' ')
		rebuilt.WriteString(tail)
	}
	return strings.TrimSpace(rebuilt.String()), true
}

// wrapOrderedCompoundBranches pushes a non-last compound branch's own ORDER
// BY or LIMIT into a subquery. SQLite rejects those on a branch, and
// stripping them throws away the branch's top-N. The trailing ORDER BY/LIMIT
// stays on the compound: the parser attaches it there, not to the last branch.
func wrapOrderedCompoundBranches(stmt string) (string, bool) {
	tokens := topLevelTokens(stmt)
	seps := setSeparators(tokens)
	if len(seps) == 0 {
		return stmt, false
	}
	type cut struct{ start, end int }
	branches := make([]cut, 0, len(seps)+1)
	start := 0
	for _, sep := range seps {
		branches = append(branches, cut{start: start, end: sep.start})
		start = sep.end
	}
	var repl []struct {
		start, end int
		text       string
	}
	for _, branch := range branches {
		if !branchHasOrderOrLimit(tokens, branch.start, branch.end) {
			continue
		}
		from, to := trimIndex(stmt, branch.start, branch.end)
		if from >= to {
			return stmt, false
		}
		repl = append(repl, struct {
			start, end int
			text       string
		}{from, to, "SELECT * FROM (" + stmt[from:to] + ")"})
	}
	if len(repl) == 0 {
		return stmt, false
	}
	var rebuilt strings.Builder
	cursor := 0
	for _, item := range repl {
		rebuilt.WriteString(stmt[cursor:item.start])
		rebuilt.WriteString(item.text)
		cursor = item.end
	}
	rebuilt.WriteString(stmt[cursor:])
	return strings.TrimSpace(rebuilt.String()), true
}

type compoundResultColumns struct {
	names       map[string]bool
	expressions map[string]bool
	count       int
}

func compoundResultSet(stmt string, tokens []token, seps []span, orderAt int) (compoundResultColumns, bool) {
	branches := make([]span, 0, len(seps)+1)
	start := 0
	for _, sep := range seps {
		branches = append(branches, span{start: start, end: sep.start})
		start = sep.end
	}
	branches = append(branches, span{start: start, end: orderAt})

	var combined compoundResultColumns
	for _, branch := range branches {
		end := branch.end
		if order := firstPair(tokens, branch.start, end, "order", "by"); order >= 0 {
			end = order
		} else if limit := firstWord(tokens, branch.start, end, "limit"); limit >= 0 {
			end = limit
		}
		from, to := trimIndex(stmt, branch.start, end)
		columns, ok := resultColumns(parseSelect(stmt[from:to]))
		if !ok || combined.count != 0 && columns.count != combined.count {
			return compoundResultColumns{}, false
		}
		if combined.count == 0 {
			combined = columns
			continue
		}
		for name := range columns.names {
			combined.names[name] = true
		}
		for expression := range columns.expressions {
			combined.expressions[expression] = true
		}
	}
	return combined, true
}

func resultColumns(sel *rqlite.SelectStatement) (compoundResultColumns, bool) {
	if sel == nil {
		return compoundResultColumns{}, false
	}
	result := compoundResultColumns{
		names:       make(map[string]bool),
		expressions: make(map[string]bool),
		count:       len(sel.Columns),
	}
	for _, col := range sel.Columns {
		if col.Star.IsValid() {
			return compoundResultColumns{}, false
		}
		if ref, ok := col.Expr.(*rqlite.QualifiedRef); ok && ref.Star.IsValid() {
			return compoundResultColumns{}, false
		}
		if col.Alias != nil {
			result.names[strings.ToLower(col.Alias.Name)] = true
		}
		result.expressions[compoundExpressionKey(col.Expr)] = true
		switch expr := col.Expr.(type) {
		case *rqlite.Ident:
			result.names[strings.ToLower(expr.Name)] = true
		case *rqlite.QualifiedRef:
			if expr.Column != nil {
				result.names[strings.ToLower(expr.Column.Name)] = true
			}
		}
	}
	return result, true
}

func keepsCompoundOrderTerm(term string, columns compoundResultColumns) bool {
	sel := parseSelect("SELECT 1 ORDER BY " + term)
	if sel == nil || len(sel.OrderingTerms) != 1 {
		return false
	}
	expr := sel.OrderingTerms[0].X
	if ordinal, ok := expr.(*rqlite.NumberLit); ok && isDecimalLiteral(ordinal.Value) {
		position, err := strconv.Atoi(ordinal.Value)
		return err == nil && position >= 1 && position <= columns.count
	}
	if columns.expressions[compoundExpressionKey(expr)] {
		return true
	}
	switch ref := expr.(type) {
	case *rqlite.Ident:
		return columns.names[strings.ToLower(ref.Name)]
	case *rqlite.QualifiedRef:
		return false
	}
	return false
}

type foldExpressionIdentifiers struct{}

func (foldExpressionIdentifiers) Visit(node rqlite.Node) (rqlite.Visitor, rqlite.Node, error) {
	if ident, ok := node.(*rqlite.Ident); ok {
		ident.Name = strings.ToLower(ident.Name)
	}
	return foldExpressionIdentifiers{}, node, nil
}

func (foldExpressionIdentifiers) VisitEnd(node rqlite.Node) (rqlite.Node, error) {
	return node, nil
}

func compoundExpressionKey(expr rqlite.Expr) string {
	clone := rqlite.CloneExpr(expr)
	node, err := rqlite.Walk(foldExpressionIdentifiers{}, clone)
	if err != nil {
		return expr.String()
	}
	return node.(rqlite.Expr).String()
}

func isDecimalLiteral(text string) bool {
	if text == "" {
		return false
	}
	for _, r := range text {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func branchHasOrderOrLimit(tokens []token, start, end int) bool {
	return firstPair(tokens, start, end, "order", "by") >= 0 ||
		firstWord(tokens, start, end, "limit") >= 0
}

func setSeparators(tokens []token) []span {
	var seps []span
	for i := 0; i < len(tokens); i++ {
		switch tokens[i].word {
		case "union":
			if i+1 < len(tokens) && tokens[i+1].word == "all" {
				seps = append(seps, span{start: tokens[i].start, end: tokens[i+1].end})
				i++
				continue
			}
			seps = append(seps, span{start: tokens[i].start, end: tokens[i].end})
		case "intersect", "except":
			seps = append(seps, span{start: tokens[i].start, end: tokens[i].end})
		}
	}
	return seps
}

func hasSetOperatorBetween(tokens []token, start, end int) bool {
	for _, tok := range tokens {
		if tok.start < start || tok.end > end {
			continue
		}
		switch tok.word {
		case "union", "intersect", "except":
			return true
		}
	}
	return false
}

func splitTopLevelCommas(stmt string, start, end int) []span {
	var parts []span
	depth := 0
	partStart := start
	i := start
	for i < end {
		switch stmt[i] {
		case '\'', '"', '`':
			i = quotedEnd(stmt, i, stmt[i])
			continue
		case '[':
			i = bracketEnd(stmt, i)
			continue
		case '-':
			if i+1 < end && stmt[i+1] == '-' {
				i = lineCommentEnd(stmt, i+2)
				continue
			}
		case '/':
			if i+1 < end && stmt[i+1] == '*' {
				i = blockCommentEnd(stmt, i+2)
				continue
			}
		case '(':
			depth++
		case ')':
			if depth > 0 {
				depth--
			}
		case ',':
			if depth == 0 {
				parts = append(parts, span{start: partStart, end: i})
				partStart = i + 1
			}
		}
		i++
	}
	parts = append(parts, span{start: partStart, end: end})
	return parts
}

func trimIndex(stmt string, start, end int) (int, int) {
	for start < end && unicode.IsSpace(rune(stmt[start])) {
		start++
	}
	for end > start && unicode.IsSpace(rune(stmt[end-1])) {
		end--
	}
	return start, end
}
