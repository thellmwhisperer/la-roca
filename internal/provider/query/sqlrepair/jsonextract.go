package sqlrepair

import "strings"

// preserveJSONExtract rewrites SQLite's `->` / `->>` shorthand to
// json_extract. The shorthand with `->` yields quoted JSON text, so a
// comparison against a SQL string silently returns no rows.
func preserveJSONExtract(stmt string) (string, bool) {
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
		leftStart, leftEnd := jsonArrowLeft(stmt, i)
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

func jsonArrowLeft(stmt string, arrow int) (int, int) {
	end := skipSpaceLeft(stmt, arrow)
	if end <= 0 {
		return -1, -1
	}
	if stmt[end-1] == ')' {
		open := matchingOpenParen(stmt, end-1)
		if open < 0 {
			return -1, -1
		}
		nameEnd := skipSpaceLeft(stmt, open)
		start := qualifiedIdentStart(stmt, nameEnd)
		if start < 0 {
			start = open
		}
		return start, end
	}
	start := qualifiedIdentStart(stmt, end)
	if start < 0 || start >= end {
		return -1, -1
	}
	return start, end
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

func qualifiedIdentStart(stmt string, end int) int {
	start := identStart(stmt, end)
	if start < 0 {
		return -1
	}
	for {
		dot := skipSpaceLeft(stmt, start)
		if dot <= 0 || stmt[dot-1] != '.' {
			return start
		}
		next := identStart(stmt, skipSpaceLeft(stmt, dot-1))
		if next < 0 {
			return start
		}
		start = next
	}
}

func identStart(stmt string, end int) int {
	if end <= 0 {
		return -1
	}
	switch stmt[end-1] {
	case '"', '\'', '`':
		quote := stmt[end-1]
		for i := end - 2; i >= 0; i-- {
			if stmt[i] != quote {
				continue
			}
			if i > 0 && stmt[i-1] == quote {
				i--
				continue
			}
			return i
		}
		return -1
	case ']':
		for i := end - 2; i >= 0; i-- {
			if stmt[i] == '[' {
				return i
			}
		}
		return -1
	}
	i := end
	for i > 0 && isWordPart(stmt[i-1]) {
		i--
	}
	if i == end || !isWordStart(stmt[i]) {
		return -1
	}
	return i
}

func matchingOpenParen(stmt string, closeAt int) int {
	var opens []int
	for i := 0; i <= closeAt; {
		if next, ok := skipQuotedOrComment(stmt, i); ok {
			i = next
			continue
		}
		switch stmt[i] {
		case '(':
			opens = append(opens, i)
		case ')':
			if len(opens) == 0 {
				i++
				continue
			}
			if i == closeAt {
				return opens[len(opens)-1]
			}
			opens = opens[:len(opens)-1]
		}
		i++
	}
	return -1
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
