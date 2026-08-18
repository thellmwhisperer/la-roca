// Package sqlgate is the read-only gate: every piece of SQL that is going to
// touch the database passes through here first, whether it comes from a
// template or from a model.
//
// "Valid" means SQLite accepted the exact statement under an authorization
// callback, not merely that a parser believes it would. The gate opens its own
// schema-only in-memory connection against modernc.org/sqlite/lib and attaches
// the callback there. The application's query connection is not changed.
//
//   - Syntax, tables, columns, verbs and functions: the engine says so. The
//     statement is prepared against an in-memory database that contains only the
//     visible tables. A table the query must not see does not exist there.
//     Writes, ATTACH, PRAGMA and functions outside the list are denied by the
//     callback, so a DELETE does not slip through merely because prepare would
//     succeed on a query_only connection.
//   - LIMIT: imposed on the original text with the same numeric-literal
//     guarantee as before, including both SQLite forms and a trailing comment.
//
// The verdict messages are contract surface and the acceptance suite quotes
// them literally.
package sqlgate

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	rqlite "github.com/rqlite/sql"
	"github.com/thellmwhisperer/la-roca/data"
)

// MaxLimit is the cap the gate guarantees.
const MaxLimit = 1000

// invisibleTables are the ones that exist in the schema but are not queryable:
// the tool's internal state, not the fleet's memory. They are dropped from the
// validation database, so asking about them is asking about what does not exist.
//
// `plugin_schema`, `plugin_migrations`, `migration_batches` and
// `custody_memberships` are the plugin-local DATA SPLIT ledger. A plugin
// declares them so its database stays self-describing, but which batch carried
// which row is custody bookkeeping and never an answer about the fleet.
//
// The block after them is the corpus archive shadow: the version tables, their
// rebuilt FTS indexes, the source evidence, and the compatibility views. They
// are migration machinery until the cutover makes them the serving surface, so
// nothing here answers a question yet.
//
// The memory names continue the list for the same reason: DATA-2's custody
// tables and compatibility view stay shadow-only until the atomic federation
// cutover selects them.
//
// `call_history_segments` and `call_history_state` close the list: they record
// which retained call segment the ops backfill already read and whether its
// parity check passed, which is the backfill's own bookkeeping and never a call
// anyone made.
//
// Exact-dedup runs and remaps are owner-gated maintenance evidence. Typed ID
// resolution can read them, but generated SQL must not treat them as a domain.
var invisibleTables = []string{
	"ingest_file_state", "search_state",
	"plugin_schema", "plugin_migrations", "migration_batches", "custody_memberships",
	"corpus_source_snapshots", "corpus_source_tables", "corpus_source_rows",
	"session_versions", "exchange_versions", "tool_use_versions", "thinking_block_versions",
	"ingest_file_state_versions", "ingest_file_state_heads",
	"session_versions_fts", "exchange_versions_fts", "thinking_block_versions_fts",
	"session_version_memberships", "exchange_version_memberships",
	"tool_use_version_memberships", "thinking_block_version_memberships",
	"ingest_file_state_version_memberships",
	"memory_records", "memory_records_fts", "memory_provenance", "memory_compatibility",
	"call_history_segments", "call_history_state",
	"dedup_runs", "memory_id_remaps", "session_id_remaps",
	"exchange_id_remaps", "thinking_block_id_remaps",
}

var fts5ShadowEndings = []string{"_data", "_idx", "_content", "_docsize", "_config"}

var createVirtualFTS5 = regexp.MustCompile(`(?is)CREATE\s+VIRTUAL\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?["'` + "`" + `]?(\w+)["'` + "`" + `]?\s+USING\s+fts5\s*\(`)

// HiddenTables are the ones the gate does not let through.
//
// It is exported because whoever builds the model's prompt needs the same list:
// offering the model a table the gate is going to reject is offering an answer
// that never runs, and keeping two copies of the list is keeping one that goes
// stale.
func HiddenTables() []string { return slices.Clone(invisibleTables) }

// IsHiddenTable applies the gate's per-schema table rule. Plugin schema
// validation and prompt construction use it so a name hidden in core does not
// become visible merely because it appears behind another qualifier.
func IsHiddenTable(name string) bool {
	lower := strings.ToLower(unqualify(name))
	return slices.Contains(invisibleTables, lower) || strings.HasPrefix(lower, "sqlite_") ||
		strings.HasPrefix(lower, "pragma_")
}

func unqualify(name string) string {
	if index := strings.LastIndex(name, "."); index >= 0 {
		return name[index+1:]
	}
	return name
}

// Gate keeps open the in-memory database statements are prepared against.
type Gate struct {
	engine *engine
}

// Schema is one attached database as the validation engine sees it. Only the
// declared visible tables are created; hidden names stay absent in every schema.
type Schema struct {
	Name   string
	Tables []Table
}

type Table struct {
	Name    string
	Columns []string
	FTS5    bool
}

// Open creates the validation database: the v1 schema minus what is invisible.
func Open() (*Gate, error) {
	return OpenWithSchemas(nil)
}

// OpenWithSchemas creates the core validation database and the qualified
// schemas selected by the plugin semantic router.
func OpenWithSchemas(schemas []Schema) (*Gate, error) {
	eng, err := openEngine()
	if err != nil {
		return nil, err
	}

	if err := eng.exec(data.Schema); err != nil {
		eng.close()
		return nil, fmt.Errorf("apply the schema to the validation database: %w", err)
	}
	// The lexical index goes in too: the FTS route emits SQL that queries it,
	// and a gate that did not know those tables would force skipping it in order
	// to search, which is exactly what the gate exists to prevent.
	if err := eng.exec(data.SearchSchema); err != nil {
		eng.close()
		return nil, fmt.Errorf("apply the search schema to the validation database: %w", err)
	}
	for _, match := range createVirtualFTS5.FindAllStringSubmatch(data.SearchSchema, -1) {
		eng.registerFTSTable("main", match[1])
	}
	for _, table := range invisibleTables {
		if err := eng.exec("DROP TABLE IF EXISTS " + table); err != nil {
			eng.close()
			return nil, fmt.Errorf("hide table %q: %w", table, err)
		}
	}
	for _, schema := range schemas {
		if err := addSchema(eng, schema); err != nil {
			eng.close()
			return nil, err
		}
	}
	if err := eng.attachAuthorizer(); err != nil {
		eng.close()
		return nil, err
	}
	return &Gate{engine: eng}, nil
}

func addSchema(eng *engine, schema Schema) error {
	if schema.Name == "" || strings.EqualFold(schema.Name, "main") || strings.EqualFold(schema.Name, "temp") {
		return fmt.Errorf("invalid attached schema name %q", schema.Name)
	}
	if err := eng.exec("ATTACH DATABASE ':memory:' AS " + quoteIdentifier(schema.Name)); err != nil {
		return fmt.Errorf("create validation schema %q: %w", schema.Name, err)
	}
	for _, table := range schema.Tables {
		if IsHiddenTable(table.Name) {
			continue
		}
		if len(table.Columns) == 0 {
			return fmt.Errorf("validation table %s.%s has no columns", schema.Name, table.Name)
		}
		columns := make([]string, len(table.Columns))
		for index, column := range table.Columns {
			columns[index] = quoteIdentifier(column)
		}
		qualified := quoteIdentifier(schema.Name) + "." + quoteIdentifier(table.Name)
		var statement string
		if table.FTS5 {
			statement = "CREATE VIRTUAL TABLE " + qualified + " USING fts5(" +
				strings.Join(columns, ", ") + ")"
		} else {
			for index := range columns {
				columns[index] += " BLOB"
			}
			statement = "CREATE TABLE " + qualified + " (" + strings.Join(columns, ", ") + ")"
		}
		if err := eng.exec(statement); err != nil {
			return fmt.Errorf("create validation table %s.%s: %w", schema.Name, table.Name, err)
		}
		if table.FTS5 {
			eng.registerFTSTable(schema.Name, table.Name)
		}
	}
	return nil
}

// FTS5ShadowTables returns the internal tables SQLite creates for an FTS5 table.
func FTS5ShadowTables(name string) []string {
	shadows := make([]string, len(fts5ShadowEndings))
	for index, ending := range fts5ShadowEndings {
		shadows[index] = strings.ToLower(name) + ending
	}
	return shadows
}

// IsFTS5DDL reports whether a SQLite CREATE statement declares an FTS5 table.
func IsFTS5DDL(ddl string) bool {
	words := strings.Fields(strings.ToLower(ddl))
	if len(words) < 6 || words[0] != "create" || words[1] != "virtual" || words[2] != "table" {
		return false
	}
	for index := 3; index+1 < len(words); index++ {
		if words[index] == "using" {
			return strings.HasPrefix(words[index+1], "fts5(") || words[index+1] == "fts5"
		}
	}
	return false
}

func quoteIdentifier(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// Close closes the validation database.
func (g *Gate) Close() error { return g.engine.close() }

// Validate returns the statement ready to run, or the reason why it is not
// going to run. The string it returns is the one to execute: it may carry the
// LIMIT that was missing.
func (g *Gate) Validate(stmt string) (string, error) {
	if strings.IndexByte(stmt, 0) >= 0 {
		return "", fmt.Errorf("SQL parse error: embedded NUL byte")
	}
	stmt = strings.TrimSpace(stmt)
	if stmt == "" {
		return "", fmt.Errorf("Empty SQL")
	}
	if n := statementCount(stmt); n > 1 {
		return "", fmt.Errorf("Only one statement is allowed, and this one has %d", n)
	}
	if verb := leadingVerb(stmt); verb != "SELECT" && verb != "WITH" {
		if verb == "" {
			return "", fmt.Errorf("Empty SQL")
		}
		return "", fmt.Errorf("Only SELECT statements are allowed")
	}
	clean, err := enforceLimit(stmt)
	if err != nil {
		return "", err
	}
	if err := g.engine.prepare(clean); err != nil {
		return "", err
	}
	return clean, nil
}

// IsRowCount reports whether the statement's sole result is COUNT(*). It uses
// the same parsed shape as the gate, so text literals and comments cannot be
// mistaken for the aggregate named inside them.
func IsRowCount(stmt string) bool {
	stmts, err := rqlite.NewParser(strings.NewReader(stmt)).ParseStatements()
	if err != nil || len(stmts) != 1 {
		return false
	}
	sel, ok := stmts[0].(*rqlite.SelectStatement)
	if !ok || len(sel.Columns) != 1 {
		return false
	}
	call, ok := sel.Columns[0].Expr.(*rqlite.Call)
	return ok && strings.EqualFold(call.Name.Name, "count") && call.Star.IsValid()
}

// withoutSemicolon leaves the statement ready for a LIMIT to be appended behind
// it without breaking it.
func withoutSemicolon(stmt string) string {
	return strings.TrimRight(strings.TrimSpace(stmt), "; \t\n")
}

// translate speaks the gate's vocabulary, not SQLite's: the operator reads this,
// and "SQL logic error (1)" does not tell them what to fix.
func translate(message string) string {
	if i := strings.Index(message, "no such table: "); i >= 0 {
		table := strings.TrimSpace(strings.TrimSuffix(message[i+len("no such table: "):], " (1)"))
		return fmt.Sprintf("no such table: %q is not a table this query can read", table)
	}
	if i := strings.Index(message, "no such column: "); i >= 0 {
		column := strings.TrimSpace(strings.TrimSuffix(message[i+len("no such column: "):], " (1)"))
		return fmt.Sprintf("no such column: %q does not exist in the referenced tables", column)
	}
	if i := strings.Index(message, "no such function: "); i >= 0 {
		name := strings.TrimSpace(strings.TrimSuffix(message[i+len("no such function: "):], " (1)"))
		return fmt.Sprintf("Function %q is not allowed", name)
	}
	if strings.Contains(message, "ambiguous column name") {
		return message
	}
	if strings.Contains(message, "incomplete input") || strings.Contains(message, "syntax error") ||
		strings.Contains(message, "unrecognized token") {
		return "SQL parse error: " + message
	}
	return "the database refused this statement: " + message
}

// enforceLimit imposes the cap on the original text. When there is no
// top-level LIMIT the maximum is added; when the one there is exceeds it, the
// count is clamped. Subquery limits stay untouched because they sit inside
// parentheses. The work is textual so a grammar-subset parser is not what
// decides whether the statement the engine prepared may run.
func enforceLimit(stmt string) (string, error) {
	limitAt, ok := lastTopLevelWord(stmt, "limit")
	if !ok {
		return appendLimit(stmt), nil
	}
	i := skipSpaceAndComments(stmt, limitAt.end)
	_, start1, end1, ok := readNumberLiteral(stmt, i)
	if !ok {
		return "", fmt.Errorf("LIMIT must be a numeric literal")
	}
	i = skipSpaceAndComments(stmt, end1)
	countStart, countEnd := start1, end1
	if i < len(stmt) && stmt[i] == ',' {
		i = skipSpaceAndComments(stmt, i+1)
		_, start2, end2, ok := readNumberLiteral(stmt, i)
		if !ok {
			return "", fmt.Errorf("LIMIT must be a numeric literal")
		}
		countStart, countEnd = start2, end2
		i = end2
	} else if start, end, okWord := readWord(stmt, i); okWord &&
		strings.EqualFold(stmt[start:end], "offset") {
		i = skipSpaceAndComments(stmt, end)
		_, _, offsetEnd, ok := readNumberLiteral(stmt, i)
		if !ok {
			return "", fmt.Errorf("LIMIT must be a numeric literal")
		}
		i = offsetEnd
	} else {
		i = end1
	}
	if !onlyStatementTail(stmt, i) {
		return "", fmt.Errorf("LIMIT must be a numeric literal")
	}
	requested, err := strconv.Atoi(stmt[countStart:countEnd])
	if err != nil {
		return "", fmt.Errorf("LIMIT must be a numeric literal")
	}
	if requested >= 0 && requested <= MaxLimit {
		return stmt, nil
	}
	return stmt[:countStart] + strconv.Itoa(MaxLimit) + stmt[countEnd:], nil
}

func appendLimit(stmt string) string {
	end := trailingSQLCodeEnd(stmt)
	code := strings.TrimRight(stmt[:end], "; \t\r\n")
	tail := stmt[end:]
	return code + fmt.Sprintf(" LIMIT %d", MaxLimit) + tail
}

type wordSpan struct{ start, end int }

func leadingVerb(stmt string) string {
	i := skipSpaceAndComments(stmt, 0)
	start, end, ok := readWord(stmt, i)
	if !ok {
		return ""
	}
	return strings.ToUpper(stmt[start:end])
}

func statementCount(stmt string) int {
	count, hasCode := 0, false
	for i := 0; i < len(stmt); {
		next := skipSpaceAndComments(stmt, i)
		if next > i {
			i = next
			continue
		}
		if i >= len(stmt) {
			break
		}
		switch stmt[i] {
		case '\'', '"', '`', '[':
			hasCode = true
			i = scanQuoted(stmt, i, stmt[i])
		case ';':
			if hasCode {
				count++
				hasCode = false
			}
			i++
		default:
			hasCode = true
			i++
		}
	}
	if hasCode {
		count++
	}
	return count
}

func lastTopLevelWord(stmt, want string) (wordSpan, bool) {
	var found wordSpan
	ok := false
	depth := 0
	for i := 0; i < len(stmt); {
		next := skipSpaceAndComments(stmt, i)
		if next > i {
			i = next
			continue
		}
		if i >= len(stmt) {
			break
		}
		switch stmt[i] {
		case '(':
			depth++
			i++
		case ')':
			if depth > 0 {
				depth--
			}
			i++
		case '\'', '"', '`', '[':
			i = scanQuoted(stmt, i, stmt[i])
		default:
			if start, end, okWord := readWord(stmt, i); okWord {
				if depth == 0 && strings.EqualFold(stmt[start:end], want) {
					found, ok = wordSpan{start: start, end: end}, true
				}
				i = end
				continue
			}
			i++
		}
	}
	return found, ok
}

func readWord(stmt string, i int) (start, end int, ok bool) {
	if i >= len(stmt) {
		return 0, i, false
	}
	letter := stmt[i]
	if letter != '_' && (letter < 'A' || letter > 'Z') && (letter < 'a' || letter > 'z') {
		return 0, i, false
	}
	start = i
	for i < len(stmt) {
		letter = stmt[i]
		if letter != '_' && (letter < 'A' || letter > 'Z') && (letter < 'a' || letter > 'z') &&
			(letter < '0' || letter > '9') {
			break
		}
		i++
	}
	return start, i, true
}

func readNumberLiteral(stmt string, i int) (value string, start, end int, ok bool) {
	if i >= len(stmt) {
		return "", i, i, false
	}
	start = i
	if i >= len(stmt) || stmt[i] < '0' || stmt[i] > '9' {
		return "", start, i, false
	}
	for i < len(stmt) && stmt[i] >= '0' && stmt[i] <= '9' {
		i++
	}
	return stmt[start:i], start, i, true
}

func skipSpaceAndComments(stmt string, i int) int {
	for i < len(stmt) {
		switch {
		case strings.ContainsRune(" \t\r\n", rune(stmt[i])):
			i++
		case stmt[i] == '-' && i+1 < len(stmt) && stmt[i+1] == '-':
			if end := strings.IndexByte(stmt[i:], '\n'); end >= 0 {
				i += end + 1
			} else {
				return len(stmt)
			}
		case stmt[i] == '/' && i+1 < len(stmt) && stmt[i+1] == '*':
			if end := strings.Index(stmt[i+2:], "*/"); end >= 0 {
				i += end + 4
			} else {
				return len(stmt)
			}
		default:
			return i
		}
	}
	return i
}

func scanQuoted(stmt string, i int, quote byte) int {
	closing := quote
	if quote == '[' {
		closing = ']'
	}
	for i++; i < len(stmt); i++ {
		if stmt[i] != closing {
			continue
		}
		if quote != '[' && i+1 < len(stmt) && stmt[i+1] == closing {
			i++
			continue
		}
		return i + 1
	}
	return len(stmt)
}

func onlyStatementTail(stmt string, i int) bool {
	i = skipSpaceAndComments(stmt, i)
	if i < len(stmt) && stmt[i] == ';' {
		i = skipSpaceAndComments(stmt, i+1)
	}
	return i == len(stmt)
}

// trailingSQLCodeEnd finds the final byte of executable SQL while ignoring
// quoted text and comments. The LIMIT belongs before a trailing comment, where
// SQLite can execute it, rather than inside that comment.
func trailingSQLCodeEnd(stmt string) int {
	const (
		normal = iota
		singleQuoted
		doubleQuoted
		backtickQuoted
		bracketQuoted
		lineComment
		blockComment
	)
	state, end := normal, 0
	for i := 0; i < len(stmt); i++ {
		char := stmt[i]
		switch state {
		case lineComment:
			if char == '\n' {
				state = normal
			}
			continue
		case blockComment:
			if char == '*' && i+1 < len(stmt) && stmt[i+1] == '/' {
				state, i = normal, i+1
			}
			continue
		case singleQuoted, doubleQuoted, backtickQuoted, bracketQuoted:
			end = i + 1
			closing := byte('\'')
			if state == doubleQuoted {
				closing = '"'
			} else if state == backtickQuoted {
				closing = '`'
			} else if state == bracketQuoted {
				closing = ']'
			}
			if char == closing {
				if i+1 < len(stmt) && stmt[i+1] == closing && state != bracketQuoted {
					end, i = i+2, i+1
				} else {
					state = normal
				}
			}
			continue
		}
		if char == '-' && i+1 < len(stmt) && stmt[i+1] == '-' {
			state, i = lineComment, i+1
			continue
		}
		if char == '/' && i+1 < len(stmt) && stmt[i+1] == '*' {
			state, i = blockComment, i+1
			continue
		}
		switch char {
		case '\'':
			state = singleQuoted
		case '"':
			state = doubleQuoted
		case '`':
			state = backtickQuoted
		case '[':
			state = bracketQuoted
		}
		if !strings.ContainsRune(" \t\r\n", rune(char)) {
			end = i + 1
		}
	}
	return end
}

// allowedFunctions contains supported scalar, date and time, aggregate, JSON,
// window and maths functions.
var allowedFunctions = set(
	// scalar
	"abs", "char", "coalesce", "concat", "concat_ws", "format", "glob", "hex",
	"ifnull", "iif", "instr", "length", "like", "likelihood", "likely", "lower",
	"ltrim", "max", "min", "nullif", "octet_length", "printf", "quote",
	"replace", "round", "rtrim", "sign", "soundex", "substr", "substring",
	"trim", "typeof", "unhex", "unicode", "unlikely", "upper",
	// date and time
	"current_date", "current_time", "current_timestamp", "date", "datetime", "julianday",
	"strftime", "time", "timediff", "unixepoch",
	// aggregate
	"avg", "count", "group_concat", "sum", "total", "string_agg",
	// JSON
	"json", "json_array", "json_array_length", "json_error_position",
	"json_extract", "json_group_array", "json_group_object", "json_insert",
	"json_object", "json_patch", "json_quote", "json_remove", "json_replace",
	"json_set", "json_type", "json_valid", "->", "->>",
	// window
	"cume_dist", "dense_rank", "first_value", "lag", "last_value", "lead",
	"nth_value", "ntile", "percent_rank", "rank", "row_number",
	// maths
	"acos", "asin", "atan", "atan2", "ceil", "ceiling", "cos", "degrees",
	"exp", "floor", "ln", "log", "log10", "log2", "mod", "pi", "pow", "power",
	"radians", "sin", "sqrt", "tan", "trunc",
	// lexical index: FTS5's three auxiliary functions, plus MATCH, which the
	// authorization callback reports as a function even though SQL writes it
	// as an operator. They only read from the index.
	"bm25", "highlight", "snippet", "match",
)

func set(values ...string) map[string]bool {
	m := make(map[string]bool, len(values))
	for _, v := range values {
		m[v] = true
	}
	return m
}
