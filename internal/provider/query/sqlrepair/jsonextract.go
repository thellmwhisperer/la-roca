package sqlrepair

import (
	"strings"

	rqlite "github.com/rqlite/sql"
)

// preserveJSONExtract rewrites SQLite's `->` / `->>` shorthand to
// json_extract. The shorthand with `->` yields quoted JSON text, so a
// comparison against a SQL string silently returns no rows.
func preserveJSONExtract(stmt string) (string, bool) {
	result := stmt
	changed := false
	for {
		fixed, ok := preserveJSONExtractPass(result)
		if !ok || fixed == result {
			return result, changed
		}
		result = fixed
		changed = true
	}
}

func preserveJSONExtractPass(stmt string) (string, bool) {
	leftStarts := jsonArrowLeftStarts(stmt)
	if len(leftStarts) == 0 {
		return stmt, false
	}
	var rebuilt strings.Builder
	changed := false
	cursor := 0
	for i := 0; i < len(stmt); {
		if next, ok := skipQuotedOrComment(stmt, i); ok {
			i = next
			continue
		}
		width := jsonArrowWidth(stmt, i)
		if width == 0 {
			i++
			continue
		}
		leftStart, leftEnd := jsonArrowLeft(stmt, i, leftStarts)
		if leftStart < cursor {
			i++
			continue
		}
		expr := stmt[leftStart:leftEnd]
		chainEnd := leftEnd
		chainChanged := false
		arrow := i
		for {
			width = jsonArrowWidth(stmt, arrow)
			rightStart, rightEnd := jsonArrowRight(stmt, arrow+width)
			if rightStart < 0 {
				break
			}
			if isJSONExtractPath(stmt[rightStart:rightEnd]) {
				expr = "json_extract(" + expr + ", " + stmt[rightStart:rightEnd] + ")"
				chainChanged = true
			} else {
				expr += stmt[chainEnd:rightEnd]
			}
			chainEnd = rightEnd
			next := skipSpaceRight(stmt, rightEnd)
			if jsonArrowWidth(stmt, next) == 0 {
				break
			}
			arrow = next
		}
		if chainEnd == leftEnd {
			i++
			continue
		}
		if !chainChanged {
			i = chainEnd
			continue
		}
		rebuilt.WriteString(stmt[cursor:leftStart])
		rebuilt.WriteString(expr)
		cursor = chainEnd
		i = chainEnd
		changed = true
	}
	if !changed {
		return stmt, false
	}
	rebuilt.WriteString(stmt[cursor:])
	return rebuilt.String(), true
}

func isJSONExtractPath(operand string) bool {
	return len(operand) >= 3 && operand[0] == '\'' && operand[1] == '$' && operand[len(operand)-1] == '\''
}

func jsonArrowWidth(stmt string, i int) int {
	if i+1 >= len(stmt) || stmt[i] != '-' || stmt[i+1] != '>' {
		return 0
	}
	if i+2 < len(stmt) && stmt[i+2] == '>' {
		return 3
	}
	return 2
}

func skipQuotedOrComment(stmt string, i int) (int, bool) {
	switch stmt[i] {
	case '\'', '"', '`':
		return quotedEnd(stmt, i, stmt[i]), true
	case '[':
		return bracketEnd(stmt, i), true
	case '-':
		if i+1 < len(stmt) && stmt[i+1] == '-' {
			return lineCommentEnd(stmt, i+2), true
		}
	case '/':
		if i+1 < len(stmt) && stmt[i+1] == '*' {
			return blockCommentEnd(stmt, i+2), true
		}
	}
	return i, false
}

func jsonArrowLeft(stmt string, arrow int, starts map[int]int) (int, int) {
	end := skipSpaceLeft(stmt, arrow)
	start, ok := starts[arrow]
	if !ok || start < 0 || start >= end {
		return -1, -1
	}
	return start, end
}

type jsonArrowVisitor struct {
	starts map[int]int
}

func (v *jsonArrowVisitor) Visit(node rqlite.Node) (rqlite.Visitor, rqlite.Node, error) {
	if expr, ok := node.(*rqlite.BinaryExpr); ok &&
		(expr.Op == rqlite.JSON_EXTRACT_JSON || expr.Op == rqlite.JSON_EXTRACT_SQL) {
		if start := jsonExprStart(expr.X); start >= 0 {
			v.starts[expr.OpPos.Offset] = start
		}
	}
	return v, node, nil
}

func (v *jsonArrowVisitor) VisitEnd(node rqlite.Node) (rqlite.Node, error) {
	return node, nil
}

func jsonArrowLeftStarts(stmt string) map[int]int {
	sel := parseSelect(stmt)
	if sel == nil {
		return nil
	}
	visitor := &jsonArrowVisitor{starts: make(map[int]int)}
	if _, err := rqlite.Walk(visitor, sel); err != nil {
		return nil
	}
	return visitor.starts
}

func jsonExprStart(expr rqlite.Expr) int {
	switch expr := expr.(type) {
	case *rqlite.BinaryExpr:
		return jsonExprStart(expr.X)
	case *rqlite.BindExpr:
		return expr.NamePos.Offset
	case *rqlite.BlobLit:
		return expr.ValuePos.Offset
	case *rqlite.BoolLit:
		return expr.ValuePos.Offset
	case *rqlite.Call:
		return expr.Name.NamePos.Offset
	case *rqlite.CaseExpr:
		return expr.Case.Offset
	case *rqlite.CastExpr:
		return expr.Cast.Offset
	case *rqlite.CollateExpr:
		return jsonExprStart(expr.X)
	case *rqlite.Exists:
		if expr.Not.IsValid() {
			return expr.Not.Offset
		}
		return expr.Exists.Offset
	case *rqlite.ExprList:
		return expr.Lparen.Offset
	case *rqlite.Ident:
		return expr.NamePos.Offset
	case *rqlite.Null:
		return jsonExprStart(expr.X)
	case *rqlite.NullLit:
		return expr.Pos.Offset
	case *rqlite.NumberLit:
		return expr.ValuePos.Offset
	case *rqlite.ParenExpr:
		return expr.Lparen.Offset
	case *rqlite.QualifiedRef:
		if expr.Schema != nil {
			return expr.Schema.NamePos.Offset
		}
		if expr.Table != nil {
			return expr.Table.NamePos.Offset
		}
	case *rqlite.Raise:
		return expr.Raise.Offset
	case *rqlite.Range:
		return jsonExprStart(expr.X)
	case *rqlite.StringLit:
		return expr.ValuePos.Offset
	case *rqlite.TimestampLit:
		return expr.ValuePos.Offset
	case *rqlite.UnaryExpr:
		return expr.OpPos.Offset
	}
	return -1
}

func jsonArrowRight(stmt string, from int) (int, int) {
	i := skipSpaceRight(stmt, from)
	if i >= len(stmt) {
		return -1, -1
	}
	switch stmt[i] {
	case '\'', '"', '`':
		return i, quotedEnd(stmt, i, stmt[i])
	case '[':
		return i, bracketEnd(stmt, i)
	case '(':
		closeAt := matchingCloseParen(stmt, i)
		if closeAt < 0 {
			return -1, -1
		}
		return i, closeAt
	}
	if isWordStart(stmt[i]) || stmt[i] >= '0' && stmt[i] <= '9' {
		start := i
		for i < len(stmt) && (isWordPart(stmt[i]) || stmt[i] == '.') {
			i++
		}
		return start, i
	}
	return -1, -1
}

func skipSpaceLeft(stmt string, i int) int {
	for i > 0 && (stmt[i-1] == ' ' || stmt[i-1] == '\t' || stmt[i-1] == '\n' || stmt[i-1] == '\r') {
		i--
	}
	return i
}

func skipSpaceRight(stmt string, i int) int {
	for i < len(stmt) && (stmt[i] == ' ' || stmt[i] == '\t' || stmt[i] == '\n' || stmt[i] == '\r') {
		i++
	}
	return i
}

func matchingCloseParen(stmt string, openAt int) int {
	depth := 0
	for i := openAt; i < len(stmt); {
		if next, ok := skipQuotedOrComment(stmt, i); ok {
			i = next
			continue
		}
		switch stmt[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i + 1
			}
		}
		i++
	}
	return -1
}
