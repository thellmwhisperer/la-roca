package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"strings"
	"time"

	"github.com/thellmwhisperer/la-roca/internal/distribution/logfile"
	"github.com/thellmwhisperer/la-roca/internal/provider/plugin"
	_ "modernc.org/sqlite"
)

// ExecRequest is a SELECT the caller wants to run as it is. It is the natural
// companion of `playground --sql-only`.
type ExecRequest struct {
	SQL        string
	MaxChars   int
	Timeout    time.Duration
	TimeoutSet bool
}

// ExecResult is what that SELECT returned, with the SQL that actually ran.
type ExecResult struct {
	SQL              string           `json:"sql"`
	Columns          []string         `json:"columns,omitempty"`
	Rows             []map[string]any `json:"rows,omitempty"`
	RowCount         int              `json:"row_count"`
	MaxChars         int              `json:"-"`
	Databases        []string         `json:"databases,omitempty"`
	OmittedDatabases []string         `json:"omitted_databases,omitempty"`
	LatencyMS        int64            `json:"latency_ms"`
	Version          string           `json:"version"`
	SourceSHA        string           `json:"source_sha"`
}

// Exec validates and runs a SELECT. What does not pass the gate does not touch
// the database, and what does pass runs over a connection on which the engine
// itself rejects any write.
func (s *Service) Exec(ctx context.Context, req ExecRequest) (ExecResult, error) {
	return s.exec(ctx, req, nil)
}

func (s *Service) exec(ctx context.Context, req ExecRequest, reader *ExecReader) (ExecResult, error) {
	start := time.Now()
	maxChars := TextBudget(req.MaxChars)
	route, validated, err := s.prepareExec(ctx, req.SQL, false)
	if err != nil {
		return ExecResult{}, err
	}
	defer route.CloseOnDemand()
	var columns []string
	var rows []map[string]any
	budget := execBudget{timeout: req.Timeout, set: req.TimeoutSet}
	if reader != nil {
		columns, rows, err = reader.execute(ctx, validated, maxChars, route.Databases, budget)
	} else {
		columns, rows, err = s.executeWithPluginsBudget(ctx, validated, "", maxChars, route.Databases, budget)
	}
	if err != nil {
		return ExecResult{}, typedExecError(err)
	}
	result := ExecResult{
		SQL: validated, Columns: columns, Rows: rows, RowCount: len(rows),
		MaxChars: maxChars, LatencyMS: time.Since(start).Milliseconds(),
		Version: s.opts.Version, SourceSHA: s.opts.Commit,
	}
	if s.PluginsActive() {
		result.Databases = route.Consulted()
	}
	return result, nil
}

type opsSQLToken struct {
	text       string
	lower      string
	start      int
	end        int
	depth      int
	identifier bool
	number     bool
	string     bool
}

type opsSQLScope struct {
	start   int
	end     int
	depth   int
	ops     bool
	aliases map[string]bool
}

type opsSQLReplacement struct {
	start int
	end   int
	text  string
}

func collectOpsIDLiterals(statement string) []string {
	if !strings.Contains(strings.ToLower(statement), "plugin_roca_ops.memories") {
		return nil
	}
	tokens := tokenizeOpsSQL(statement)
	scopes := opsSQLScopes(tokens)
	var ids []string
	seen := map[string]bool{}
	for index := range tokens {
		scope := innermostOpsSQLScope(scopes, index)
		if scope == nil || !scope.ops {
			continue
		}
		idIndex, _, ok := opsSQLIDReference(tokens, index, scope)
		if !ok || idIndex+2 >= len(tokens) || tokens[idIndex+1].text != "=" {
			continue
		}
		digits, ok := opsSQLDecimal(tokens[idIndex+2])
		if !ok || seen[digits] {
			continue
		}
		seen[digits] = true
		ids = append(ids, digits)
	}
	return ids
}

func opsRemapCanonicalIDs(databases []plugin.Database, ids []string) (map[string]string, error) {
	out := make(map[string]string)
	if len(ids) == 0 {
		return out, nil
	}
	var opsDatabase plugin.Database
	for _, database := range databases {
		if database.Schema == "plugin_roca_ops" {
			opsDatabase = database
			break
		}
	}
	if opsDatabase.Database == "" {
		return out, nil
	}
	conn, err := sql.Open("sqlite", opsDatabase.ReadOnlyURI())
	if err != nil {
		return nil, fmt.Errorf("open roca-ops remaps read-only: %w", err)
	}
	defer conn.Close()
	if err := conn.Ping(); err != nil {
		return nil, fmt.Errorf("open roca-ops remaps read-only: %w", err)
	}
	var present bool
	if err := conn.QueryRow(`SELECT EXISTS(
		SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = 'memory_id_remaps')`).Scan(&present); err != nil {
		return nil, fmt.Errorf("inspect roca-ops remaps: %w", err)
	}
	if !present {
		return out, nil
	}
	placeholders := strings.Repeat("?,", len(ids))
	placeholders = placeholders[:len(placeholders)-1]
	args := make([]any, len(ids))
	for index, id := range ids {
		args[index] = id
	}
	rows, err := conn.Query(`SELECT CAST(old_id AS TEXT), CAST(canonical_id AS TEXT) FROM memory_id_remaps WHERE old_id IN (`+placeholders+`)`, args...)
	if err != nil {
		return nil, fmt.Errorf("read roca-ops remaps: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var oldID, canonical string
		if err := rows.Scan(&oldID, &canonical); err != nil {
			return nil, fmt.Errorf("scan roca-ops remap: %w", err)
		}
		if canonical == "" {
			continue
		}
		out[oldID] = canonical
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read roca-ops remaps: %w", err)
	}
	return out, nil
}

func opsIDPredicate(column, legacy, digits string, remaps map[string]string) string {
	text := "(" + column + " = " + digits + " OR " + legacy + " = " + digits
	if canonical := remaps[digits]; canonical != "" && canonical != digits {
		text += " OR " + column + " = " + canonical + " OR " + legacy + " = " + canonical
	}
	return text + ")"
}

func expandOpsLegacyIDs(statement string, remaps map[string]string) string {
	if !strings.Contains(strings.ToLower(statement), "plugin_roca_ops.memories") {
		return statement
	}
	tokens := tokenizeOpsSQL(statement)
	if len(tokens) == 0 {
		return statement
	}
	scopes := opsSQLScopes(tokens)
	if len(scopes) == 0 {
		return statement
	}
	replacements := make([]opsSQLReplacement, 0)
	for index := range tokens {
		scope := innermostOpsSQLScope(scopes, index)
		if scope == nil || !scope.ops {
			continue
		}
		idIndex, qualifier, ok := opsSQLIDReference(tokens, index, scope)
		if !ok || idIndex+2 >= len(tokens) || tokens[idIndex+1].text != "=" {
			continue
		}
		digits, ok := opsSQLDecimal(tokens[idIndex+2])
		if !ok {
			continue
		}
		column := "id"
		legacy := "legacy_id"
		start := tokens[idIndex].start
		if qualifier != "" {
			column = qualifier + ".id"
			legacy = qualifier + ".legacy_id"
			start = tokens[idIndex-2].start
		}
		replacements = append(replacements, opsSQLReplacement{
			start: start,
			end:   tokens[idIndex+2].end,
			text:  opsIDPredicate(column, legacy, digits, remaps),
		})
	}
	if len(replacements) == 0 {
		return statement
	}
	var result strings.Builder
	last := 0
	for _, replacement := range replacements {
		if replacement.start < last {
			continue
		}
		result.WriteString(statement[last:replacement.start])
		result.WriteString(replacement.text)
		last = replacement.end
	}
	result.WriteString(statement[last:])
	return result.String()
}

func opsRouteHasLegacyID(databases []plugin.Database) bool {
	for _, database := range databases {
		if database.Schema != "plugin_roca_ops" {
			continue
		}
		for _, table := range database.Tables {
			if table.Name != "memories" {
				continue
			}
			for _, column := range table.Columns {
				if column == "legacy_id" {
					return true
				}
			}
		}
	}
	return false
}

func tokenizeOpsSQL(statement string) []opsSQLToken {
	var tokens []opsSQLToken
	depth := 0
	for index := 0; index < len(statement); {
		if statement[index] == '-' && index+1 < len(statement) && statement[index+1] == '-' {
			index += 2
			for index < len(statement) && statement[index] != '\n' {
				index++
			}
			continue
		}
		if statement[index] == '/' && index+1 < len(statement) && statement[index+1] == '*' {
			index += 2
			for index+1 < len(statement) && (statement[index] != '*' || statement[index+1] != '/') {
				index++
			}
			if index+1 < len(statement) {
				index += 2
			}
			continue
		}
		if strings.ContainsRune(" \t\r\n", rune(statement[index])) {
			index++
			continue
		}
		start := index
		switch statement[index] {
		case '\'', '"', '`', '[':
			quote := statement[index]
			close := quote
			if quote == '[' {
				close = ']'
			}
			index++
			for index < len(statement) {
				if statement[index] == close {
					if quote != '[' && index+1 < len(statement) && statement[index+1] == close {
						index += 2
						continue
					}
					index++
					break
				}
				index++
			}
			text := statement[start:index]
			token := opsSQLToken{text: text, lower: strings.ToLower(opsSQLQuotedValue(text)), start: start, end: index, depth: depth}
			if quote == '\'' {
				token.string = true
			} else {
				token.identifier = true
			}
			tokens = append(tokens, token)
		case '(':
			tokens = append(tokens, opsSQLToken{text: "(", start: start, end: start + 1, depth: depth})
			depth++
			index++
		case ')':
			if depth > 0 {
				depth--
			}
			tokens = append(tokens, opsSQLToken{text: ")", start: start, end: start + 1, depth: depth})
			index++
		default:
			if opsSQLIdentifierStartByte(statement[index]) {
				index++
				for index < len(statement) && opsSQLIdentifierByte(statement[index]) {
					index++
				}
				text := statement[start:index]
				tokens = append(tokens, opsSQLToken{text: text, lower: strings.ToLower(text), start: start, end: index, depth: depth, identifier: true})
			} else if statement[index] >= '0' && statement[index] <= '9' {
				index++
				for index < len(statement) && statement[index] >= '0' && statement[index] <= '9' {
					index++
				}
				text := statement[start:index]
				tokens = append(tokens, opsSQLToken{text: text, lower: text, start: start, end: index, depth: depth, number: true})
			} else {
				tokens = append(tokens, opsSQLToken{text: statement[index : index+1], start: start, end: start + 1, depth: depth})
				index++
			}
		}
	}
	return tokens
}

func opsSQLIdentifierByte(value byte) bool {
	return value == '_' || value == '$' || value >= 'a' && value <= 'z' ||
		value >= 'A' && value <= 'Z' || value >= '0' && value <= '9'
}

func opsSQLIdentifierStartByte(value byte) bool {
	return value == '_' || value == '$' || value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z'
}

func opsSQLQuotedValue(text string) string {
	if len(text) < 2 {
		return text
	}
	value := text[1 : len(text)-1]
	if text[0] == '[' {
		return value
	}
	return strings.ReplaceAll(value, string(text[0])+string(text[0]), string(text[0]))
}

func opsSQLScopes(tokens []opsSQLToken) []opsSQLScope {
	scopes := make([]opsSQLScope, 0)
	cteOps := make(map[string]bool)
	for index, token := range tokens {
		if token.lower != "select" || !token.identifier {
			continue
		}
		end := len(tokens)
		for candidate := index + 1; candidate < len(tokens); candidate++ {
			if tokens[candidate].depth < token.depth {
				end = candidate
				break
			}
		}
		aliases := make(map[string]bool)
		baseDepth := token.depth
		for candidate := index + 1; candidate < end; candidate++ {
			if tokens[candidate].depth != baseDepth || (tokens[candidate].lower != "from" && tokens[candidate].lower != "join") {
				continue
			}
			table, next, ok := opsSQLTable(tokens, candidate+1, end)
			if !ok || (table != "plugin_roca_ops.memories" && !cteOps[table]) {
				continue
			}
			aliases[""] = true
			if next < end && tokens[next].lower == "as" {
				next++
			}
			if next < end && tokens[next].identifier && !opsSQLClause(tokens[next].lower) {
				aliases[tokens[next].lower] = true
			}
		}
		scope := opsSQLScope{start: index, end: end, depth: token.depth, ops: len(aliases) > 0, aliases: aliases}
		scopes = append(scopes, scope)
		if index >= 3 && tokens[index-3].identifier && tokens[index-2].lower == "as" && tokens[index-1].text == "(" {
			cteOps[tokens[index-3].lower] = scope.ops
		}
	}
	return scopes
}

func opsSQLTable(tokens []opsSQLToken, index, end int) (string, int, bool) {
	if index >= end || !tokens[index].identifier {
		return "", index, false
	}
	parts := []string{tokens[index].lower}
	index++
	for index+1 < end && tokens[index].text == "." && tokens[index+1].identifier {
		parts = append(parts, tokens[index+1].lower)
		index += 2
	}
	return strings.Join(parts, "."), index, true
}

func opsSQLClause(value string) bool {
	switch value {
	case "where", "join", "left", "right", "inner", "outer", "cross", "on", "group", "order", "limit", "offset", "having", "union", "except", "intersect", "window", "returning":
		return true
	default:
		return false
	}
}

func innermostOpsSQLScope(scopes []opsSQLScope, index int) *opsSQLScope {
	var match *opsSQLScope
	for scopeIndex := range scopes {
		scope := &scopes[scopeIndex]
		if index >= scope.start && index < scope.end && (match == nil || scope.depth > match.depth) {
			match = scope
		}
	}
	return match
}

func opsSQLIDReference(tokens []opsSQLToken, index int, scope *opsSQLScope) (int, string, bool) {
	if tokens[index].lower == "id" && tokens[index].identifier {
		if index == 0 || tokens[index-1].text != "." {
			return index, "", scope.aliases[""]
		}
		if index >= 2 && tokens[index-2].identifier {
			qualifier := tokens[index-2].lower
			return index, qualifier, scope.aliases[qualifier]
		}
	}
	return 0, "", false
}

func opsSQLDecimal(token opsSQLToken) (string, bool) {
	if !token.number && !token.string {
		return "", false
	}
	value := token.text
	if token.string && len(value) >= 2 {
		value = value[1 : len(value)-1]
	}
	if value == "" {
		return "", false
	}
	for index := range value {
		if value[index] < '0' || value[index] > '9' {
			return "", false
		}
	}
	return value, true
}

func (s *Service) prepareExec(ctx context.Context, statement string, cursor bool) (PluginRoute, string, error) {
	if _, err := s.EnsureSchema(ctx); err != nil {
		return PluginRoute{}, "", err
	}
	route := s.pluginsForSQL(ctx, statement)
	if opsRouteHasLegacyID(route.Databases) {
		remaps, err := opsRemapCanonicalIDs(route.Databases, collectOpsIDLiterals(statement))
		if err != nil {
			return PluginRoute{}, "", err
		}
		statement = expandOpsLegacyIDs(statement, remaps)
	}
	ok := false
	defer func() {
		if !ok {
			route.CloseOnDemand()
		}
	}()
	if len(route.Omitted) > 0 {
		return PluginRoute{}, "", logfile.Typed(fmt.Errorf(
			"the SELECT references more than SQLite's %d attached databases; split the query (omitted: %s)",
			plugin.MaxAttached, strings.Join(route.OmittedSources(), ", ")), DegradedInvalidSQL)
	}
	gate, closeGate, err := s.GateFor(route.IncludeCore, route.Databases)
	if err != nil {
		return PluginRoute{}, "", err
	}
	defer closeGate()
	// The stage that failed is only knowable here, and it is the same
	// distinction the degraded answers already declare: what the gate refused
	// and what the engine could not run are two different fixes.
	if err := gate.RejectUnqualified(statement); err != nil {
		return PluginRoute{}, "", logfile.Typed(err, DegradedInvalidSQL)
	}
	validate := gate.Validate
	if cursor {
		validate = gate.ValidateCursor
	}
	validated, err := validate(statement)
	if err != nil {
		return PluginRoute{}, "", logfile.Typed(err, DegradedInvalidSQL)
	}
	ok = true
	return route, validated, nil
}

func typedExecError(err error) error {
	degraded := DegradedExecution
	if errors.Is(err, ErrQueryTimeout) {
		degraded = DegradedTimeout
	}
	return logfile.Typed(err, degraded)
}

// execute runs the validated SELECT and normalizes the rows into maps keyed by
// column name, which is what both surfaces render.
func (s *Service) execute(ctx context.Context, stmt, term string, maxChars int) ([]string, []map[string]any, error) {
	return s.ExecuteWithPlugins(ctx, stmt, term, maxChars, nil)
}

// queryExecutionBudget is how long validated SQL may run. A positive budget is
// the one that was asked for. Zero, a negative value, or an absent setting all
// use DefaultQueryTimeout. Exec is always bounded: a bad SELECT cannot hold
// the database past the limit.
func (s *Service) queryExecutionBudget() (time.Duration, bool) {
	if s.opts.QueryTimeout > 0 {
		return s.opts.QueryTimeout, true
	}
	return DefaultQueryTimeout, true
}

func (s *Service) boundedExecTimeout(budget execBudget) time.Duration {
	if budget.set {
		if budget.timeout > 0 {
			return budget.timeout
		}
		return DefaultQueryTimeout
	}
	timeout, _ := s.queryExecutionBudget()
	return timeout
}

func executionError(parent, queryCtx context.Context, timeout time.Duration, err error) error {
	if parent.Err() == nil && errors.Is(queryCtx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("%w after %s", ErrQueryTimeout, timeout)
	}
	return fmt.Errorf("run the validated query: %w", err)
}

func finishedWithinBudget(parent, queryCtx context.Context, timeout time.Duration, err error) error {
	if err != nil {
		return executionError(parent, queryCtx, timeout, err)
	}
	if queryCtx.Err() != nil {
		return executionError(parent, queryCtx, timeout, queryCtx.Err())
	}
	return nil
}

// ScanRows turns any result set into its column names and its rows of named
// values under the text budget. The query cascade, health diagnosis and
// SQL execution share it, so unexpected column types are handled in
// one place.
func ScanRows(rows *sql.Rows, maxChars int, term string) ([]string, []map[string]any, error) {
	return scanRowsPage(rows, maxChars, term, 0)
}

func scanRowsPage(rows *sql.Rows, maxChars int, term string, limit int) ([]string, []map[string]any, error) {
	columns, err := rows.Columns()
	if err != nil {
		return nil, nil, err
	}
	var result []map[string]any
	for (limit == 0 || len(result) < limit) && rows.Next() {
		values := make([]any, len(columns))
		pointers := make([]any, len(columns))
		for i := range values {
			pointers[i] = &values[i]
		}
		if err := rows.Scan(pointers...); err != nil {
			return nil, nil, err
		}
		row := make(map[string]any, len(columns))
		for i, column := range columns {
			row[column] = values[i]
			switch text := values[i].(type) {
			case []byte:
				row[column] = Truncate(string(text), maxChars, term)
			case string:
				row[column] = Truncate(text, maxChars, term)
			}
		}
		result = append(result, row)
	}
	return columns, result, rows.Err()
}
