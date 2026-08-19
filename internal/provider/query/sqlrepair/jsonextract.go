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
	arrows := jsonArrows(stmt)
	if len(arrows) == 0 {
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
		leftStart, leftEnd := jsonArrowLeft(stmt, i, arrows)
		if leftStart < cursor {
			i++
			continue
		}
		expr := stmt[leftStart:leftEnd]
		chainEnd := leftEnd
		chainChanged := false
		arrow := i
		for {
			operand, ok := arrows[arrow]
			if !ok {
				break
			}
			if operand.path {
				expr = "json_extract(" + expr + ", " + stmt[operand.rightStart:operand.rightEnd] + ")"
				chainChanged = true
			} else {
				expr += stmt[chainEnd:operand.rightEnd]
			}
			chainEnd = operand.rightEnd
			next := skipSpaceRight(stmt, operand.rightEnd)
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

func jsonArrowLeft(stmt string, arrow int, arrows map[int]jsonArrow) (int, int) {
	end := skipSpaceLeft(stmt, arrow)
	operand, ok := arrows[arrow]
	if !ok || operand.leftStart < 0 || operand.leftStart >= end {
		return -1, -1
	}
	return operand.leftStart, end
}

type jsonArrow struct {
	leftStart  int
	rightStart int
	rightEnd   int
	path       bool
}

type jsonArrowVisitor struct {
	stmt        string
	arrows      map[int]jsonArrow
	byteOffsets []int
}

func (v *jsonArrowVisitor) Visit(node rqlite.Node) (rqlite.Visitor, rqlite.Node, error) {
	if expr, ok := node.(rqlite.SelectExpr); ok {
		_, err := rqlite.Walk(v, expr.SelectStatement)
		return nil, node, err
	}
	if expr, ok := node.(*rqlite.BinaryExpr); ok &&
		(expr.Op == rqlite.JSON_EXTRACT_JSON || expr.Op == rqlite.JSON_EXTRACT_SQL) {
		leftStart := runeToByteOffset(v.byteOffsets, jsonExprStart(expr.X))
		op := runeToByteOffset(v.byteOffsets, expr.OpPos.Offset)
		if leftStart >= 0 && op >= 0 {
			width := jsonArrowWidth(v.stmt, op)
			rightStart, rightEnd := jsonArrowRight(v.stmt, op+width)
			if width > 0 && rightStart >= 0 {
				v.arrows[op] = jsonArrow{
					leftStart:  leftStart,
					rightStart: rightStart,
					rightEnd:   rightEnd,
					path:       isJSONExtractPathExpr(expr.Y),
				}
			}
		}
	}
	return v, node, nil
}

func (v *jsonArrowVisitor) VisitEnd(node rqlite.Node) (rqlite.Node, error) {
	return node, nil
}

func jsonArrows(stmt string) map[int]jsonArrow {
	sel := parseSelect(stmt)
	if sel == nil {
		return nil
	}
	visitor := &jsonArrowVisitor{
		stmt:        stmt,
		arrows:      make(map[int]jsonArrow),
		byteOffsets: runeByteOffsets(stmt),
	}
	if _, err := rqlite.Walk(visitor, sel); err != nil {
		return nil
	}
	return visitor.arrows
}

func isJSONExtractPathExpr(expr rqlite.Expr) bool {
	for {
		paren, ok := expr.(*rqlite.ParenExpr)
		if !ok {
			break
		}
		expr = paren.X
	}
	lit, ok := expr.(*rqlite.StringLit)
	return ok && strings.HasPrefix(lit.Value, "$")
}

func runeByteOffsets(text string) []int {
	var offsets []int
	for offset := range text {
		offsets = append(offsets, offset)
	}
	return offsets
}

func runeToByteOffset(offsets []int, offset int) int {
	if offset < 0 || offset >= len(offsets) {
		return -1
	}
	return offsets[offset]
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
