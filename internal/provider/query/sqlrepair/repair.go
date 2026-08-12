// Package sqlrepair performs narrow, deterministic repairs on model-authored
// SQL before the read-only gate judges it. Nothing in this package authorizes
// execution; the gate still validates the exact repaired statement.
package sqlrepair

import (
	"regexp"
	"strings"

	rqlite "github.com/rqlite/sql"
)

const (
	ThinkingBlock     = "thinking_block"
	CodeFence         = "code_fence"
	SurroundingProse  = "surrounding_prose"
	TrailingSemicolon = "trailing_semicolon"
	RepetitionLoop    = "repetition_loop"
	UnionOrderBy      = "union_order_by"
)

// Result is the candidate sent to the gate and every named repair used to
// produce it. The caller keeps the raw model output separately for audit.
type Result struct {
	SQL     string
	Repairs []string
}

var (
	thinkingBlock = regexp.MustCompile(`(?s)<think>.*?</think>`)
	fencedBlock   = regexp.MustCompile("(?is)```(?:sql)?[ \\t]*\\r?\\n(.*?)\\r?\\n?[ \\t]*```")
	sqlStart      = regexp.MustCompile(`(?i)^\s*(SELECT|WITH)\b`)
)

// Prepare applies only the model-output mistakes named above. The UNION
// fallback is accepted only when its result parses as one SELECT; truncated or
// ambiguous output stays untouched for the strict gate to reject.
func Prepare(raw string) Result {
	result := Result{SQL: strings.TrimSpace(raw)}

	if cleaned := thinkingBlock.ReplaceAllString(result.SQL, ""); cleaned != result.SQL {
		result.SQL = cleaned
		result.add(ThinkingBlock)
	}
	if fenced, prose, ok := insideSingleFence(result.SQL); ok {
		result.SQL = fenced
		result.add(CodeFence)
		if prose {
			result.add(SurroundingProse)
		}
	}
	if cleaned := deloop(result.SQL); cleaned != result.SQL {
		result.SQL = cleaned
		result.add(RepetitionLoop)
	}

	result.SQL = strings.TrimSpace(result.SQL)
	if cleaned, ok := stripLeadingProse(result.SQL); ok {
		result.SQL = cleaned
		result.add(SurroundingProse)
	}
	if fixed, ok := softUnionOrderBy(result.SQL); ok {
		result.SQL = fixed
		result.add(UnionOrderBy)
	}
	if cleaned, ok := stripTrailingProse(result.SQL); ok {
		result.SQL = cleaned
		result.add(SurroundingProse)
	}
	if cleaned, ok := stripTrailingSemicolons(result.SQL); ok {
		result.SQL = cleaned
		result.add(TrailingSemicolon)
	}

	if !parsesAsSingleSelect(result.SQL) {
		if fixed, ok := aggressiveUnionOrderBy(result.SQL); ok && parsesAsSingleSelect(fixed) {
			result.SQL = fixed
			result.add(UnionOrderBy)
		}
	}
	result.SQL = strings.TrimSpace(result.SQL)
	return result
}

func (r *Result) add(name string) {
	for _, existing := range r.Repairs {
		if existing == name {
			return
		}
	}
	r.Repairs = append(r.Repairs, name)
}

func insideSingleFence(text string) (string, bool, bool) {
	matches := fencedBlock.FindAllStringSubmatchIndex(text, -1)
	if len(matches) != 1 {
		return text, false, false
	}
	match := matches[0]
	content := strings.TrimSpace(text[match[2]:match[3]])
	if !sqlStart.MatchString(content) {
		return text, false, false
	}
	prefix, suffix := strings.TrimSpace(text[:match[0]]), strings.TrimSpace(text[match[1]:])
	if strings.Contains(prefix, "```") || strings.Contains(suffix, "```") {
		return text, false, false
	}
	return content, prefix != "" || suffix != "", true
}

func stripLeadingProse(text string) (string, bool) {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		if !sqlStart.MatchString(line) {
			continue
		}
		if i == 0 || !allProse(lines[:i]) {
			return text, false
		}
		return strings.TrimSpace(strings.Join(lines[i:], "\n")), true
	}
	return text, false
}

func stripTrailingProse(text string) (string, bool) {
	if parsesAsSingleSelect(text) {
		return text, false
	}
	lines := strings.Split(text, "\n")
	for end := len(lines) - 1; end > 0; end-- {
		if !allProse(lines[end:]) {
			continue
		}
		candidate := strings.TrimSpace(strings.Join(lines[:end], "\n"))
		if parsesAsSingleSelect(candidate) {
			return candidate, true
		}
	}
	return text, false
}

func allProse(lines []string) bool {
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "--") || strings.HasPrefix(trimmed, "/*") ||
			strings.Contains(trimmed, "```") || startsSQLClause(trimmed) {
			return false
		}
	}
	return true
}

func startsSQLClause(line string) bool {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return false
	}
	switch strings.ToUpper(strings.Trim(fields[0], "(")) {
	case "SELECT", "WITH", "FROM", "WHERE", "JOIN", "LEFT", "RIGHT", "INNER", "OUTER",
		"CROSS", "ON", "UNION", "INTERSECT", "EXCEPT", "GROUP", "HAVING", "ORDER",
		"LIMIT", "OFFSET", "AND", "OR", "WHEN", "ELSE", "END":
		return true
	default:
		return false
	}
}

func stripTrailingSemicolons(text string) (string, bool) {
	cleaned := strings.TrimSpace(text)
	changed := false
	for strings.HasSuffix(cleaned, ";") {
		cleaned = strings.TrimSpace(strings.TrimSuffix(cleaned, ";"))
		changed = true
	}
	return cleaned, changed
}

// loopThreshold distinguishes a local model repetition loop from legitimate
// repeated short UNION or JOIN lines.
const loopThreshold = 50

func deloop(text string) string {
	lines := strings.Split(text, "\n")
	seen := make(map[string]bool, len(lines))
	for i, line := range lines {
		key := strings.TrimSpace(line)
		if len(key) <= loopThreshold {
			continue
		}
		if seen[key] {
			return strings.TrimSpace(strings.Join(lines[:i], "\n"))
		}
		seen[key] = true
	}
	return text
}

func parsesAsSingleSelect(stmt string) bool {
	statements, err := rqlite.NewParser(strings.NewReader(stmt)).ParseStatements()
	if err != nil || len(statements) != 1 {
		return false
	}
	_, ok := statements[0].(*rqlite.SelectStatement)
	return ok
}

type token struct {
	word       string
	start, end int
}

type span struct{ start, end int }

func softUnionOrderBy(stmt string) (string, bool) {
	tokens := topLevelTokens(stmt)
	unions := unionSeparators(tokens)
	if len(unions) == 0 {
		return stmt, false
	}
	cuts := make([]span, 0, len(unions))
	branchStart := 0
	for _, union := range unions {
		if order := firstPair(tokens, branchStart, union.start, "order", "by"); order >= 0 {
			cuts = append(cuts, span{start: order, end: union.start})
		}
		branchStart = union.end
	}
	if len(cuts) == 0 {
		return stmt, false
	}
	var fixed strings.Builder
	cursor := 0
	for _, cut := range cuts {
		fixed.WriteString(stmt[cursor:cut.start])
		cursor = cut.end
	}
	fixed.WriteString(stmt[cursor:])
	return strings.TrimSpace(fixed.String()), true
}

// aggressiveUnionOrderBy is the fallback for malformed shapes the soft pass
// cannot parse, such as a duplicated final ORDER BY. It removes top-level
// branch ordering and limits, keeps the last final ORDER BY, and never touches
// nested subqueries or CTE UNIONs.
func aggressiveUnionOrderBy(stmt string) (string, bool) {
	tokens := topLevelTokens(stmt)
	unions := unionSeparators(tokens)
	if len(unions) == 0 {
		return stmt, false
	}

	boundaries := make([]span, 0, len(unions)+1)
	start := 0
	for _, union := range unions {
		boundaries = append(boundaries, span{start: start, end: union.start})
		start = union.end
	}
	boundaries = append(boundaries, span{start: start, end: len(stmt)})

	last := boundaries[len(boundaries)-1]
	lastOrders := allPairs(tokens, last.start, last.end, "order", "by")
	finalOrder := ""
	if len(lastOrders) > 0 {
		finalOrder = strings.TrimSpace(stmt[lastOrders[len(lastOrders)-1]:])
	}

	parts := make([]string, 0, len(boundaries))
	for i, branch := range boundaries {
		cut := branch.end
		if order := firstPair(tokens, branch.start, branch.end, "order", "by"); order >= 0 {
			cut = order
		} else if i < len(boundaries)-1 {
			if limit := firstWord(tokens, branch.start, branch.end, "limit"); limit >= 0 {
				cut = limit
			}
		}
		part := strings.TrimSpace(stmt[branch.start:cut])
		if part == "" {
			return stmt, false
		}
		parts = append(parts, part)
	}
	fixed := strings.Join(parts, " UNION ALL ")
	if finalOrder != "" {
		fixed += " " + finalOrder
	}
	return fixed, fixed != strings.TrimSpace(stmt)
}

func unionSeparators(tokens []token) []span {
	var unions []span
	for i := 0; i+1 < len(tokens); i++ {
		if tokens[i].word == "union" && tokens[i+1].word == "all" {
			unions = append(unions, span{start: tokens[i].start, end: tokens[i+1].end})
			i++
		}
	}
	return unions
}

func firstPair(tokens []token, start, end int, first, second string) int {
	positions := allPairs(tokens, start, end, first, second)
	if len(positions) == 0 {
		return -1
	}
	return positions[0]
}

func allPairs(tokens []token, start, end int, first, second string) []int {
	var positions []int
	for i := 0; i+1 < len(tokens); i++ {
		if tokens[i].start >= start && tokens[i+1].end <= end &&
			tokens[i].word == first && tokens[i+1].word == second {
			positions = append(positions, tokens[i].start)
		}
	}
	return positions
}

func firstWord(tokens []token, start, end int, word string) int {
	for _, token := range tokens {
		if token.start >= start && token.end <= end && token.word == word {
			return token.start
		}
	}
	return -1
}

func topLevelTokens(stmt string) []token {
	var tokens []token
	depth := 0
	for i := 0; i < len(stmt); {
		switch stmt[i] {
		case '\'', '"', '`':
			i = quotedEnd(stmt, i, stmt[i])
			continue
		case '[':
			i = bracketEnd(stmt, i)
			continue
		case '-':
			if i+1 < len(stmt) && stmt[i+1] == '-' {
				i = lineCommentEnd(stmt, i+2)
				continue
			}
		case '/':
			if i+1 < len(stmt) && stmt[i+1] == '*' {
				i = blockCommentEnd(stmt, i+2)
				continue
			}
		case '(':
			depth++
			i++
			continue
		case ')':
			if depth > 0 {
				depth--
			}
			i++
			continue
		}
		if depth == 0 && isWordStart(stmt[i]) {
			start := i
			for i++; i < len(stmt) && isWordPart(stmt[i]); i++ {
			}
			tokens = append(tokens, token{word: strings.ToLower(stmt[start:i]), start: start, end: i})
			continue
		}
		i++
	}
	return tokens
}

func quotedEnd(text string, start int, quote byte) int {
	for i := start + 1; i < len(text); i++ {
		if text[i] != quote {
			continue
		}
		if i+1 < len(text) && text[i+1] == quote {
			i++
			continue
		}
		return i + 1
	}
	return len(text)
}

func bracketEnd(text string, start int) int {
	if end := strings.IndexByte(text[start+1:], ']'); end >= 0 {
		return start + end + 2
	}
	return len(text)
}

func lineCommentEnd(text string, start int) int {
	if end := strings.IndexByte(text[start:], '\n'); end >= 0 {
		return start + end + 1
	}
	return len(text)
}

func blockCommentEnd(text string, start int) int {
	if end := strings.Index(text[start:], "*/"); end >= 0 {
		return start + end + 2
	}
	return len(text)
}

func isWordStart(char byte) bool {
	return char == '_' || char >= 'A' && char <= 'Z' || char >= 'a' && char <= 'z'
}

func isWordPart(char byte) bool {
	return isWordStart(char) || char >= '0' && char <= '9' || char == '$'
}
