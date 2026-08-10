/*
Package axi is the single owner of the AXI TOON text both surfaces of La Roca
hand to a reader.

The shell and the MCP plug answer the same question with the same rows, and a
reader is a reader: a human at a terminal and an agent over a pipe see the same
compact shape. That shape is the route narration, a rows[N]{cols}: table of
clipped values, and the contextual help that follows it. This package paints it,
and the two surfaces compose it for their result types instead of each keeping
a second copy (the duplication gate ships at zero, and a second renderer would
be the first clone).

RowOutput is the renderer: it turns a set of uniform rows into the tabular form
every AXI tool emits, generic over columns so a count, a grouping and a search
all paint honestly. RenderHelp and QueryHelp carry the deterministic next steps.
The composers in compose.go build the full text for a query, an exec, a health
report and a store, and that is what the MCP plug puts in the readable half of a
tool result.
*/
package axi

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/thellmwhisperer/la-roca/internal/provider/query"
	"github.com/thellmwhisperer/la-roca/internal/provider/service"
)

// FieldWidth preserves the terminal budget for every text cell: a field is
// clipped to this many runes so a single wide row never drowns the table. It is
// the same value the shell has always used, kept here so the two surfaces
// cannot drift.
const FieldWidth = 160

// RowOutput paints uniform rows as the tabular form emitted by AXI tools. The
// declared count and fixed width make truncation or malformed output visible.
//
// A single row with a single column is printed bare: the answer to "how many
// memories are there" is the number, and a table around it is ceremony.
func RowOutput(columns []string, rows []map[string]any, terms ...string) string {
	if len(rows) == 0 {
		return ""
	}
	if len(rows) == 1 && len(columns) == 1 && len(rows[0]) == 1 {
		if value, ok := rows[0][columns[0]]; ok && value != nil {
			return trim(asText(value), FieldWidth)
		}
	}

	order := columnOrder(columns, rows)
	term := strings.Join(terms, "+")
	var out strings.Builder
	fmt.Fprintf(&out, "rows[%d]{", len(rows))
	for i, column := range order {
		if i > 0 {
			out.WriteByte(',')
		}
		out.WriteString(toonKey(column))
	}
	out.WriteString("}:")
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

// columnOrder respects the query's order and appends at the end whatever the
// row carries and the list does not declare, because losing a piece of data to
// a badly passed list would be hiding the answer.
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
		return toonString(excerpt(v, term, FieldWidth))
	case []byte:
		return toonString(excerpt(string(v), term, FieldWidth))
	case bool:
		return strconv.FormatBool(v)
	case int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64, float32, float64:
		return asText(v)
	default:
		return toonString(excerpt(asText(v), term, FieldWidth))
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

// RenderHelp paints the deterministic next-step lines that follow a row table.
// Empty input paints nothing.
func RenderHelp(lines ...string) string {
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

// QueryHelp builds the contextual help for a query result: the route to the
// complete envelope, and when there are rows, the path from the compiled SQL to
// an expanded view of them.
func QueryHelp(res service.QueryResult) string {
	question := shellArg(res.Question)
	json := fmt.Sprintf("Run `roca query %s --json` for the complete result envelope", question)
	if res.RowCount == 0 {
		return RenderHelp(
			"Run `roca query \"<shorter keywords>\"` to broaden the search",
			fmt.Sprintf("Run `roca query %s --json` to inspect the route and SQL", question),
		)
	}
	lines := []string{json,
		fmt.Sprintf("Run `roca query %s --sql-only`, then `roca exec \"<SELECT>\" --max-chars 2000` to inspect or expand rows", question)}
	return RenderHelp(lines...)
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

// excerpt clips a human field around its longest visible search term. It never
// changes the row itself, so the structured envelope continues to carry the
// complete text.
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
