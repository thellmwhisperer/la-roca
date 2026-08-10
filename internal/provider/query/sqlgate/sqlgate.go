// Package sqlgate is the read-only gate: every piece of SQL that is going to
// touch the database passes through here first, whether it comes from a
// template or from a model.
//
// The lab solves it with sqlglot plus SQLite's `set_authorizer`, and that second
// half is what gives the strong guarantee: "valid" means SQLite accepted this
// exact string, not that a validator believes it would. The pure-Go driver that
// makes the static binary possible does not expose the authorizer, so the work
// is split in two (TECH-SPEC 1.5):
//
//   - Table and column existence, and syntax: the engine says so. The statement
//     is prepared against an in-memory database that contains only the visible
//     tables. A table the query must not see does not need forbidding: it does
//     not exist there, and prepare fails. This is stronger than an AST allowlist,
//     because it also covers columns and ambiguities.
//   - Verb, functions and LIMIT: the AST says so, with a pure-Go parser of
//     SQLite's grammar. It was measured that the engine is not enough for this:
//     over a connection with query_only, `prepare` of a DELETE passes, and the
//     rejection only arrives at execution time.
//
// The verdict messages are contract surface, and the consecrated suite quotes
// them literally (F04-06).
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

// MaxLimit is the cap the gate guarantees. It is the same one as the lab's.
const MaxLimit = 1000

// invisibleTables are the ones that exist in the schema but are not queryable:
// the tool's internal state, not the fleet's memory. They are dropped from the
// validation database, so asking about them is asking about what does not exist.
var invisibleTables = []string{
	"ingest_file_state", "queryplan_teach_examples", "search_state",
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

// Gate keeps open the in-memory database statements are prepared against.
type Gate struct {
	db *sql.DB
}

// Open creates the validation database: the v1 schema minus what is invisible.
func Open() (*Gate, error) {
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
	return &Gate{db: db}, nil
}

// Close closes the validation database.
func (g *Gate) Close() error { return g.db.Close() }

// Validate returns the statement ready to run, or the reason why it is not
// going to run. The string it returns is the one to execute: it may carry the
// LIMIT that was missing.
func (g *Gate) Validate(stmt string) (string, error) {
	stmts, err := rqlite.NewParser(strings.NewReader(stmt)).ParseStatements()
	if err != nil {
		return "", fmt.Errorf("SQL parse error: %w", err)
	}
	if len(stmts) == 0 {
		// Nothing but blanks: the parser reads no statement and raises nothing.
		return "", fmt.Errorf("Empty SQL")
	}
	if len(stmts) > 1 {
		return "", fmt.Errorf("Only one statement is allowed, and this one has %d",
			len(stmts))
	}
	sel, isSelect := stmts[0].(*rqlite.SelectStatement)
	if !isSelect {
		return "", fmt.Errorf("Only SELECT statements are allowed")
	}
	if err := walkTheTree(sel); err != nil {
		return "", err
	}

	clean, err := withLimit(withoutSemicolon(stmt), sel)
	if err != nil {
		return "", err
	}
	if err := g.prepareWithEngine(clean); err != nil {
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
	}
	return v, n, nil
}

func (v *inspector) VisitEnd(n rqlite.Node) (rqlite.Node, error) { return n, nil }

func hasAnySuffix(name string, suffixes []string) bool {
	return slices.ContainsFunc(suffixes, func(s string) bool { return strings.HasSuffix(name, s) })
}

// withLimit imposes the cap, which is a guarantee and not a suggestion: when
// there is no LIMIT the maximum is added, and when the one there is exceeds it,
// it is clamped.
//
// The work happens on the original text and not on a re-serialization of the AST
// for two reasons. The first is that the validated statement is returned to the
// caller and goes to the log, so it has to keep looking like the one that was
// asked for. The second is security: the parser quotes every identifier when it
// re-serializes, and SQLite treats a double-quoted string that matches no column
// as a text literal, so re-serializing would turn "column that does not exist"
// into "constant string" and lose the engine's error.
func withLimit(stmt string, sel *rqlite.SelectStatement) (string, error) {
	if sel.LimitExpr == nil {
		return stmt + fmt.Sprintf(" LIMIT %d", MaxLimit), nil
	}
	number, isNumber := sel.LimitExpr.(*rqlite.NumberLit)
	if !isNumber {
		return "", fmt.Errorf("LIMIT must be a numeric literal")
	}
	requested, err := strconv.Atoi(number.Value)
	if err != nil {
		return "", fmt.Errorf("LIMIT must be a numeric literal")
	}
	if requested >= 0 && requested <= MaxLimit {
		return stmt, nil
	}

	start := number.ValuePos.Offset
	end := start + len(number.Value)
	if start < 0 || end > len(stmt) || stmt[start:end] != number.Value {
		return "", fmt.Errorf("LIMIT must be a numeric literal")
	}
	return stmt[:start] + strconv.Itoa(MaxLimit) + stmt[end:], nil
}

// allowedFunctions is the lab's allowlist, ported as it is: scalar, date and
// time, aggregate, JSON, window and maths.
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
