// Package sqlgate is the read-only gate: every piece of SQL that is going to
// touch the database passes through here first, whether it comes from a
// template or from a model.
//
// "Valid" means SQLite accepted the exact statement, not merely that a parser
// believes it would. modernc.org/sqlite does not expose an authorization
// callback, so the shipped shape is: prepare is the correctness verdict
// (syntax, tables, columns); the AST remains the permission surface (verb,
// functions, hidden names) when it accepts the same string; LIMIT is applied
// on the text so a grammar-subset parse cannot be the thing that rejects a
// statement the engine already prepared.
//
//   - Table and column existence, and syntax: the engine says so. The statement
//     is prepared against an in-memory database that contains only the visible
//     tables. A table the query must not see does not need forbidding: it does
//     not exist there, and prepare fails. This is stronger than an AST allowlist,
//     because it also covers columns and ambiguities.
//   - Verb and functions: the AST says so when the parser accepts the statement.
//     It was measured that the engine is not enough for this: over a connection
//     with query_only, `prepare` of a DELETE passes, and the rejection only
//     arrives at execution time. A leading verb check refuses writes before
//     prepare so the contract message stays "Only SELECT statements are allowed".
//   - LIMIT: imposed on the original text with the same numeric-literal
//     guarantee as before, including both SQLite forms and a trailing comment.
//
// The verdict messages are contract surface and the acceptance suite quotes
// them literally.
package sqlgate

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strconv"
	"strings"

	rqlite "github.com/rqlite/sql"
	"github.com/thellmwhisperer/la-roca/data"
	_ "modernc.org/sqlite"
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

// ftsShadowSuffixes name the shadow tables FTS5 creates behind each virtual
// table. They keep the index in binary blocks and are nobody's memory.
//
// These cannot be hidden by dropping them the way the others are: dropping a
// shadow table leaves the virtual table broken, and the validation database
// would no longer be able to prepare the legitimate query. They are denied by
// name in the AST, just like the `sqlite_` and `pragma_` ones.
var ftsShadowSuffixes = []string{
	"_fts_data", "_fts_idx", "_fts_content", "_fts_docsize", "_fts_config",
}

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
	lower := strings.ToLower(name)
	return slices.Contains(invisibleTables, lower) || strings.HasPrefix(lower, "sqlite_") ||
		strings.HasPrefix(lower, "pragma_") || hasAnySuffix(lower, ftsShadowSuffixes)
}

// Gate keeps open the in-memory database statements are prepared against.
type Gate struct {
	db *sql.DB
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
}

// Open creates the validation database: the v1 schema minus what is invisible.
func Open() (*Gate, error) {
	return OpenWithSchemas(nil)
}

// OpenWithSchemas creates the core validation database and the qualified
// schemas selected by the plugin semantic router.
func OpenWithSchemas(schemas []Schema) (*Gate, error) {
	db, err := sql.Open("sqlite", "file::memory:?_pragma=query_only(0)")
	if err != nil {
		return nil, fmt.Errorf("open the validation database: %w", err)
	}
	// A single connection: the in-memory database lives in the connection,
	// and a pool would hand out an empty one where everything would fail with
	// "no such table".
	db.SetMaxOpenConns(1)

	if _, err := db.Exec(data.Schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply the schema to the validation database: %w", err)
	}
	// The lexical index goes in too: the FTS route emits SQL that queries it,
	// and a gate that did not know those tables would force skipping it in order
	// to search, which is exactly what the gate exists to prevent.
	if _, err := db.Exec(data.SearchSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply the search schema to the validation database: %w", err)
	}
	for _, table := range invisibleTables {
		if _, err := db.Exec("DROP TABLE IF EXISTS " + table); err != nil {
			db.Close()
			return nil, fmt.Errorf("hide table %q: %w", table, err)
		}
	}
	for _, schema := range schemas {
		if err := addSchema(db, schema); err != nil {
			db.Close()
			return nil, err
		}
	}
	return &Gate{db: db}, nil
}

func addSchema(db *sql.DB, schema Schema) error {
	if schema.Name == "" || strings.EqualFold(schema.Name, "main") || strings.EqualFold(schema.Name, "temp") {
		return fmt.Errorf("invalid attached schema name %q", schema.Name)
	}
	if _, err := db.Exec("ATTACH DATABASE ':memory:' AS " + quoteIdentifier(schema.Name)); err != nil {
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
			columns[index] = quoteIdentifier(column) + " BLOB"
		}
		statement := "CREATE TABLE " + quoteIdentifier(schema.Name) + "." +
			quoteIdentifier(table.Name) + " (" + strings.Join(columns, ", ") + ")"
		if _, err := db.Exec(statement); err != nil {
			return fmt.Errorf("create validation table %s.%s: %w", schema.Name, table.Name, err)
		}
	}
	return nil
}

func quoteIdentifier(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// Close closes the validation database.
func (g *Gate) Close() error { return g.db.Close() }

// Validate returns the statement ready to run, or the reason why it is not
// going to run. The string it returns is the one to execute: it may carry the
// LIMIT that was missing.
func (g *Gate) Validate(stmt string) (string, error) {
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
	sel, parseErr := parseSelectErr(clean)
	if parseErr == nil && sel != nil {
		if err := walkTheTree(sel); err != nil {
			return "", err
		}
	}
	if err := g.prepareWithEngine(clean); err != nil {
		if parseErr != nil {
			return "", fmt.Errorf("SQL parse error: %w", parseErr)
		}
		return "", err
	}
	return clean, nil
}

func parseSelectErr(stmt string) (*rqlite.SelectStatement, error) {
	stmts, err := rqlite.NewParser(strings.NewReader(stmt)).ParseStatements()
	if err != nil {
		return nil, err
	}
	if len(stmts) != 1 {
		return nil, fmt.Errorf("Only one statement is allowed, and this one has %d", len(stmts))
	}
	sel, ok := stmts[0].(*rqlite.SelectStatement)
	if !ok {
		return nil, fmt.Errorf("Only SELECT statements are allowed")
	}
	return sel, nil
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

// prepareWithEngine is the half that cannot be faked: what SQLite will not
// prepare against the visible schema does not run.
func (g *Gate) prepareWithEngine(stmt string) error {
	prepared, err := g.db.PrepareContext(context.Background(), stmt)
	if err != nil {
		return fmt.Errorf("%s", translate(err))
	}
	return prepared.Close()
}

// translate speaks the gate's vocabulary, not SQLite's: the operator reads this,
// and "SQL logic error (1)" does not tell them what to fix.
func translate(err error) string {
	message := err.Error()
	if i := strings.Index(message, "no such table: "); i >= 0 {
		table := strings.TrimSpace(strings.TrimSuffix(message[i+len("no such table: "):], " (1)"))
		return fmt.Sprintf("no such table: %q is not a table this query can read", table)
	}
	if i := strings.Index(message, "no such column: "); i >= 0 {
		column := strings.TrimSpace(strings.TrimSuffix(message[i+len("no such column: "):], " (1)"))
		return fmt.Sprintf("no such column: %q does not exist in the referenced tables", column)
	}
	if strings.Contains(message, "ambiguous column name") {
		return message
	}
	return "the database refused this statement: " + message
}

// walkTheTree denies what the engine cannot deny on its own: the functions
// outside the list, and SQLite's internal tables, which exist in any database
// and cannot be hidden by dropping them.
//
// Preparing a call to load_extension does not fail; it fails on execution, and
// by then it is too late.
func walkTheTree(sel *rqlite.SelectStatement) error {
	visitor := &inspector{}
	if _, err := rqlite.Walk(visitor, sel); err != nil {
		return err
	}
	return visitor.reason
}

type inspector struct{ reason error }

func (v *inspector) Visit(n rqlite.Node) (rqlite.Visitor, rqlite.Node, error) {
	if v.reason != nil {
		return v, n, nil
	}
	switch node := n.(type) {
	case *rqlite.Call:
		if node.Name != nil && !allowedFunctions[strings.ToLower(node.Name.Name)] {
			v.reason = fmt.Errorf("Function %q is not allowed", node.Name.Name)
		}
	case *rqlite.QualifiedTableName:
		if node.Name == nil {
			break
		}
		name := strings.ToLower(node.Name.Name)
		if strings.HasPrefix(name, "sqlite_") || strings.HasPrefix(name, "pragma_") ||
			hasAnySuffix(name, ftsShadowSuffixes) {
			v.reason = fmt.Errorf("no such table: %q is not a table this query can read",
				node.Name.Name)
		}
	case *rqlite.QualifiedTableFunctionName:
		if node.Name == nil {
			break
		}
		name := strings.ToLower(node.Name.Name)
		if strings.HasPrefix(name, "sqlite_") || strings.HasPrefix(name, "pragma_") {
			v.reason = fmt.Errorf("no such table: %q is not a table this query can read",
				node.Name.Name)
		}
	case *rqlite.WithClause:
		for _, cte := range node.CTEs {
			if cte != nil && cte.Select != nil {
				if err := walkTheTree(cte.Select); err != nil {
					v.reason = err
					break
				}
			}
		}
	case rqlite.SelectExpr:
		if node.SelectStatement != nil {
			v.reason = walkTheTree(node.SelectStatement)
		}
	}
	return v, n, nil
}

func (v *inspector) VisitEnd(n rqlite.Node) (rqlite.Node, error) { return n, nil }

func hasAnySuffix(name string, suffixes []string) bool {
	return slices.ContainsFunc(suffixes, func(s string) bool { return strings.HasSuffix(name, s) })
}

// enforceLimit imposes the cap on the original text. When there is no
// top-level LIMIT the maximum is added; when the one there is exceeds it, the
// count is clamped. Subquery limits stay untouched because they sit inside
// parentheses. The work is textual so a grammar-subset parser cannot be what
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
		case '\'', '"', '`':
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
		case '\'', '"', '`':
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
	if stmt[i] == '+' || stmt[i] == '-' {
		i++
	}
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
	for i++; i < len(stmt); i++ {
		if stmt[i] != quote {
			continue
		}
		if i+1 < len(stmt) && stmt[i+1] == quote {
			i++
			continue
		}
		return i + 1
	}
	return len(stmt)
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
	"date", "datetime", "julianday", "strftime", "time", "timediff", "unixepoch",
	// aggregate
	"avg", "count", "group_concat", "sum", "total", "string_agg",
	// JSON
	"json", "json_array", "json_array_length", "json_error_position",
	"json_extract", "json_group_array", "json_group_object", "json_insert",
	"json_object", "json_patch", "json_quote", "json_remove", "json_replace",
	"json_set", "json_type", "json_valid",
	// window
	"cume_dist", "dense_rank", "first_value", "lag", "last_value", "lead",
	"nth_value", "ntile", "percent_rank", "rank", "row_number",
	// maths
	"acos", "asin", "atan", "atan2", "ceil", "ceiling", "cos", "degrees",
	"exp", "floor", "ln", "log", "log10", "log2", "mod", "pi", "pow", "power",
	"radians", "sin", "sqrt", "tan", "trunc",
	// lexical index: FTS5's three auxiliary functions. They only read from
	// the index and only make sense inside a query that already matched.
	"bm25", "highlight", "snippet",
)

func set(values ...string) map[string]bool {
	m := make(map[string]bool, len(values))
	for _, v := range values {
		m[v] = true
	}
	return m
}
