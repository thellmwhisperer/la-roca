/*
@overview AXI TOON row rendering with match-centered excerpts and contextual help. ~250 lines, no public symbols.

	READING GUIDE
	-------------
	1. Start at rowOutput
	2. Read excerpt for relevance-preserving clipping
	3. Read toonValue and queryHelp on demand

	MAIN FLOW
	---------
	columns and rows -> stable TOON table -> contextual AXI help

	PUBLIC API
	----------
	None; this file serves the CLI package.

	INTERNALS
	---------
	rowOutput, toonValue, renderHelp, queryHelp, columnOrder, excerpt, matchPosition, trim

@exports
@deps fmt/regexp/sort/strconv/strings, internal query/service
*/
package cli

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/thellmwhisperer/la-roca/internal/provider/query"
	"github.com/thellmwhisperer/la-roca/internal/provider/service"
)

// -- 1/3 CORE · rowOutput -- <- START HERE

// Readable rendering of a set of rows.
//
// It is generic over columns on purpose. The compiler has 33 templates and each
// one returns the row shape its question asks for: a count brings a figure, a
// grouping brings a pair, a session brings eight columns and the search brings
// its union of four sources. A rendering that names columns by hand only paints
// well the family it was written for, and the rest come out empty; that is
// exactly what happened when it painted "source" and "text".
//
// The --json output hands over the whole answer. This is what a human sees at a
// terminal, and that is why it flattens, clips and keeps quiet about the columns
// with no value.

// fieldWidth preserves the existing terminal budget for every text cell.
const fieldWidth = 160

// rowOutput paints uniform rows as the tabular form emitted by AXI tools. The
// declared count and fixed width make truncation or malformed output visible.
//
// A single row with a single column is printed bare: the answer to "how many
// memories are there" is the number, and a table around it is ceremony.
func rowOutput(columns []string, rows []map[string]any, terms ...string) string {
	if len(rows) == 0 {
		return ""
	}
	if len(rows) == 1 && len(columns) == 1 && len(rows[0]) == 1 {
		if value, ok := rows[0][columns[0]]; ok && value != nil {
			return trim(asText(value), fieldWidth)
		}
	}

	order := columnOrder(columns, rows)
	var out strings.Builder
	fmt.Fprintf(&out, "rows[%d]{", len(rows))
	for i, column := range order {
		if i > 0 {
			out.WriteByte(',')
		}
		out.WriteString(toonKey(column))
	}
	out.WriteString("}:")
	term := strings.Join(terms, "+")
	for _, row := range rows {
		out.WriteString("\n  ")
		for i, column := range order {
			if i > 0 {
				out.WriteByte(',')
			}
			out.WriteString(toonValue(row[column], term))
		}
	}
	return out.String()
}

// -/ 1/3

// -- 2/3 HELPER · columnOrder and TOON scalar formatting --

// columnOrder respects the query's order and appends at the end whatever the row
// carries and the list does not declare, because losing a piece of data to a
// badly passed list would be hiding the answer.
func columnOrder(columns []string, rows []map[string]any) []string {
	present := make(map[string]bool)
	for _, row := range rows {
		for column := range row {
			present[column] = true
		}
	}
	if present["source"] && present["id"] && present["created_at"] && present["text"] {
		columns = append([]string{"source", "id", "created_at", "text"}, columns...)
	}
	declared := make(map[string]bool, len(present))
	order := make([]string, 0, len(present))
	for _, column := range columns {
		if present[column] && !declared[column] {
			declared[column] = true
			order = append(order, column)
		}
	}
	var extras []string
	for column := range present {
		if !declared[column] {
			extras = append(extras, column)
		}
	}
	// A map's iteration order is random in Go: without sorting, the same row
	// would print differently on every run.
	sort.Strings(extras)
	return append(order, extras...)
}

func toonValue(value any, term string) string {
	if value == nil {
		return "null"
	}
	switch v := value.(type) {
	case string:
		return toonString(excerpt(v, term, fieldWidth))
	case []byte:
		return toonString(excerpt(string(v), term, fieldWidth))
	case bool:
		return strconv.FormatBool(v)
	case int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64, float32, float64:
		return asText(v)
	default:
		return toonString(excerpt(asText(v), term, fieldWidth))
	}
}

func toonString(value string) string {
	value = singleLine(value)
	if !toonNeedsQuotes(value) {
		return value
	}
	return quoteTOON(value)
}

var toonNumber = regexp.MustCompile(`^[+-]?[0-9]+(?:\.[0-9]+)?(?:[eE][+-]?[0-9]+)?$`)

func toonNeedsQuotes(value string) bool {
	if value == "" || value == "true" || value == "false" || value == "null" ||
		strings.Trim(value, " \t") != value || strings.ContainsAny(value, ",:\"\\[]{}\n\r\t") ||
		strings.HasPrefix(value, "-") || strings.HasPrefix(value, "#") {
		return true
	}
	for _, r := range value {
		if r < 0x20 {
			return true
		}
	}
	return toonNumber.MatchString(value)
}

func toonKey(key string) string {
	if key == "" || !asciiLetter(key[0]) && key[0] != '_' {
		return quoteTOON(key)
	}
	for i := 1; i < len(key); i++ {
		if !asciiLetter(key[i]) && (key[i] < '0' || key[i] > '9') && key[i] != '_' && key[i] != '.' {
			return quoteTOON(key)
		}
	}
	return key
}

func asciiLetter(b byte) bool { return b >= 'A' && b <= 'Z' || b >= 'a' && b <= 'z' }

func quoteTOON(value string) string {
	var out strings.Builder
	out.WriteByte('"')
	for _, r := range value {
		switch r {
		case '\\':
			out.WriteString(`\\`)
		case '"':
			out.WriteString(`\"`)
		case '\n':
			out.WriteString(`\n`)
		case '\r':
			out.WriteString(`\r`)
		case '\t':
			out.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(&out, `\u%04x`, r)
			} else {
				out.WriteRune(r)
			}
		}
	}
	out.WriteByte('"')
	return out.String()
}

func renderHelp(lines ...string) string {
	if len(lines) == 0 {
		return ""
	}
	var out strings.Builder
	fmt.Fprintf(&out, "help[%d]:", len(lines))
	for _, line := range lines {
		out.WriteString("\n  - ")
		out.WriteString(toonString(line))
	}
	return out.String()
}

func queryHelp(res service.QueryResult) string {
	question := shellArg(res.Question)
	json := fmt.Sprintf("Run `roca query %s --json` for the complete result envelope", question)
	if res.RowCount == 0 {
		return renderHelp(
			"Run `roca query \"<shorter keywords>\"` to broaden the search",
			fmt.Sprintf("Run `roca query %s --json` to inspect the route and SQL", question),
		)
	}
	lines := []string{json,
		fmt.Sprintf("Run `roca query %s --sql-only`, then `roca exec \"<SELECT>\" --max-chars 2000` to inspect or expand rows", question)}
	return renderHelp(lines...)
}

func shellArg(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `"`, `\"`, `$`, `\$`, "`", "\\`")
	return `"` + replacer.Replace(value) + `"`
}

func asText(value any) string {
	switch v := value.(type) {
	case []byte:
		return singleLine(string(v))
	case string:
		return singleLine(v)
	case float64:
		// An average does not read well with seventeen decimals.
		return strconv.FormatFloat(v, 'g', 6, 64)
	case float32:
		return strconv.FormatFloat(float64(v), 'g', 6, 32)
	default:
		return singleLine(fmt.Sprint(value))
	}
}

func singleLine(text string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(text, "\n", " ")), " ")
}

// -/ 2/3

// -- 3/3 HELPER · excerpt and trim --

// excerpt clips a human field around its longest visible search term. It never
// changes the row itself, so JSON continues to carry the complete text.
func excerpt(text, terms string, width int) string {
	text = singleLine(text)
	runes := []rune(text)
	if len(runes) <= width {
		return text
	}
	position, matched := matchPosition(text, terms)
	if position < 0 {
		return trim(text, width)
	}
	start := max(0, position-(width-matched)/2)
	start = min(start, len(runes)-width)
	visible := runes[start : start+width]
	if start > 0 {
		visible[0] = '…'
	}
	if start+width < len(runes) {
		visible[len(visible)-1] = '…'
	}
	return string(visible)
}

func matchPosition(text, terms string) (int, int) {
	tokens := strings.Fields(strings.ReplaceAll(query.Normalize(terms), "+", " "))
	sort.SliceStable(tokens, func(i, j int) bool {
		return len([]rune(tokens[i])) > len([]rune(tokens[j]))
	})
	plain := []rune(query.Fold(strings.ToLower(text)))
	for _, token := range tokens {
		if at := strings.Index(string(plain), token); at >= 0 {
			return len([]rune(string(plain)[:at])), len([]rune(token))
		}
	}
	return -1, 0
}

func trim(text string, width int) string {
	runes := []rune(text)
	if len(runes) <= width {
		return text
	}
	return string(runes[:width-1]) + "…"
}

// -/ 3/3
