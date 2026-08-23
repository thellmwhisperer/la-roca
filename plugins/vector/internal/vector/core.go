package vector

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

const coreFieldBudget = 64 << 20

type CommandRunner func(context.Context, string, ...string) ([]byte, error)

// CoreCLI reads corpus rows only through La Roca's public, read-only process
// boundary. The vector plugin never imports core packages or opens roca.db.
type CoreCLI struct {
	Executable string
	DBPath     string
	Run        CommandRunner
}

type DatabaseScope struct {
	Databases        []string            `json:"databases"`
	Selected         []DatabaseSelection `json:"selected"`
	OmittedDatabases []string            `json:"omitted_databases,omitempty"`
	Warnings         []string            `json:"warnings,omitempty"`
}

type DatabaseSelection struct {
	Source   string `json:"source"`
	Database string `json:"database"`
}

type execResult struct {
	Rows []map[string]any `json:"rows"`
}

type corePage struct {
	kind    string
	initial string
	query   func(string) string
	decode  func(map[string]any) (sourceRow, string, error)
}

const (
	exchangeText       = `trim(COALESCE(human_text,'') || CASE WHEN human_text IS NOT NULL AND agent_text IS NOT NULL THEN char(10)||char(10) ELSE '' END || COALESCE(agent_text,''))`
	sessionProjectName = `CASE WHEN json_valid(COALESCE(metadata,'')) THEN CASE WHEN json_type(metadata,'$.project_name')='text' THEN COALESCE(json_extract(metadata,'$.project_name'),'') ELSE '' END ELSE '' END`
	corpusSchema       = "plugin_roca_corpus"
)

var (
	structuralSessionToken    = regexp.MustCompile(`(?i)(?:\b[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}\b|\b(?:ses(?:sion)?[_:-])?[0-9A-HJKMNP-TV-Z]{26}\b|\bg-p-[a-z0-9_-]+\b|\b(?:md5|sha-?(?:1|224|256|384|512))[:=_-][0-9a-f]{7,}\b)`)
	sessionJSONScalarFragment = regexp.MustCompile(`"(?:\\.|[^"\\])*"\s*:\s*(?:"(?:\\.|[^"\\])*"|true|false|null|-?[0-9]+(?:\.[0-9]+)?(?:[eE][+-]?[0-9]+)?)`)
	sessionJSONKeyFragment    = regexp.MustCompile(`"(?:\\.|[^"\\])*"\s*:`)
)

func corpusTable(name string) string { return corpusSchema + "." + name }

func (c CoreCLI) WalkSources(ctx context.Context, sourceKind string, visit func(sourceRow) error) error {
	if err := validateSourceKind(sourceKind, nil); err != nil {
		return err
	}
	iterators := []*corePageIterator{}
	for _, source := range corePages() {
		if sourceKind != "" && source.kind != sourceKind {
			continue
		}
		iterator := &corePageIterator{core: c, page: source, cursor: source.initial}
		if err := iterator.advance(ctx); err != nil {
			return err
		}
		iterators = append(iterators, iterator)
	}
	for {
		selected := -1
		for index, iterator := range iterators {
			if iterator.current == nil {
				continue
			}
			if selected < 0 || sourceNewer(*iterator.current, *iterators[selected].current) {
				selected = index
			}
		}
		if selected < 0 {
			return nil
		}
		if err := visit(*iterators[selected].current); err != nil {
			return err
		}
		if err := iterators[selected].advance(ctx); err != nil {
			return err
		}
	}
}

type corePageIterator struct {
	core    CoreCLI
	page    corePage
	cursor  string
	rows    []sourceRow
	index   int
	done    bool
	current *sourceRow
}

func (i *corePageIterator) advance(ctx context.Context) error {
	for {
		if i.index < len(i.rows) {
			row := i.rows[i.index]
			i.index++
			i.current = &row
			return nil
		}
		if i.done {
			i.current = nil
			return nil
		}
		values, err := i.core.query(ctx, i.page.query(i.cursor))
		if err != nil {
			return fmt.Errorf("read core %s: %w", i.page.kind, err)
		}
		i.done = len(values) < walkPageSize
		i.rows = i.rows[:0]
		i.index = 0
		for _, value := range values {
			row, next, err := i.page.decode(value)
			if err != nil {
				return fmt.Errorf("decode core %s: %w", i.page.kind, err)
			}
			i.cursor = next
			i.rows = append(i.rows, expandDecoded(row, value)...)
		}
	}
}

func (c CoreCLI) CountChunks(ctx context.Context, sourceKind string) (int64, error) {
	if err := validateSourceKind(sourceKind, nil); err != nil {
		return 0, err
	}
	sessionText := fmt.Sprintf(`trim(COALESCE(title,'') || CASE WHEN COALESCE(title,'')<>'' AND %s<>'' THEN char(10) ELSE '' END || %s)`,
		sessionProjectName, sessionProjectName)
	statements := map[string]string{
		"memories": fmt.Sprintf(`SELECT COALESCE(SUM(%s),0) AS total FROM %s WHERE COALESCE(content,'')<>''`,
			chunkCountExpression("COALESCE(content,'')", defaultChunkSize, defaultOverlap), corpusTable("memories")),
		"exchanges": fmt.Sprintf(`SELECT COALESCE(SUM(%s),0) AS total FROM %s WHERE COALESCE(human_text,'')<>'' OR COALESCE(agent_text,'')<>''`,
			chunkCountExpression(exchangeText, defaultChunkSize, defaultOverlap), corpusTable("exchanges")),
		"thinking_blocks": fmt.Sprintf(`SELECT COALESCE(SUM(%s),0) AS total FROM %s WHERE COALESCE(full_text,'')<>''`,
			chunkCountExpression("COALESCE(full_text,'')", defaultChunkSize, defaultOverlap), corpusTable("thinking_blocks")),
		"sessions": fmt.Sprintf(`SELECT COALESCE(SUM(%s),0) AS total FROM %s WHERE COALESCE(title,'')<>'' OR %s<>''`,
			chunkCountExpression(sessionText, defaultChunkSize, defaultOverlap), corpusTable("sessions"), sessionProjectName),
	}
	var total int64
	for _, kind := range []string{"memories", "exchanges", "thinking_blocks", "sessions"} {
		if sourceKind != "" && sourceKind != kind {
			continue
		}
		rows, err := c.query(ctx, statements[kind])
		if err != nil {
			return 0, fmt.Errorf("count core %s: %w", kind, err)
		}
		if len(rows) != 1 {
			return 0, fmt.Errorf("count core %s returned %d rows", kind, len(rows))
		}
		count, err := integer(rows[0], "total")
		if err != nil {
			return 0, err
		}
		total += count
	}
	return total, nil
}

func (c CoreCLI) ResolveDatabaseScope(ctx context.Context, databases string) (DatabaseScope, error) {
	if strings.TrimSpace(c.Executable) == "" {
		return DatabaseScope{}, fmt.Errorf("roca executable is required")
	}
	args := []string{"--json"}
	if c.DBPath != "" {
		args = append(args, "--db-path", c.DBPath)
	}
	args = append(args, "_database-scope")
	if strings.TrimSpace(databases) != "" {
		args = append(args, "--databases", databases)
	}
	run := c.Run
	if run == nil {
		run = runCommand
	}
	raw, err := run(ctx, c.Executable, args...)
	if err != nil {
		return DatabaseScope{}, err
	}
	var result DatabaseScope
	if err := json.Unmarshal(raw, &result); err != nil {
		return DatabaseScope{}, fmt.Errorf("decode roca database scope: %w", err)
	}
	if result.Databases == nil {
		result.Databases = []string{}
	}
	if result.Selected == nil {
		result.Selected = []DatabaseSelection{}
	}
	return result, nil
}

const (
	newestTimeCursor = "9999-12-31 23:59:59"
	newestIDCursor   = "9223372036854775807"
)

func corePages() []corePage {
	return []corePage{
		{
			kind: "memories", initial: joinCursor(newestTimeCursor, newestIDCursor),
			query: func(cursor string) string {
				ts, id := splitCursor(cursor)
				return fmt.Sprintf(`SELECT id,content,COALESCE(source_session,'') AS source_session,
					source_sequence,COALESCE(source_agent,'') AS source_agent,
					COALESCE(metadata,'{}') AS metadata,COALESCE(layer,'') AS layer,
					COALESCE(origin,'') AS origin,COALESCE(project,'') AS project,
					COALESCE(created_at,'') AS created_at
					FROM %s WHERE COALESCE(content,'') <> ''
					AND (COALESCE(created_at,'') < %s OR (COALESCE(created_at,'') = %s AND id < %s))
					ORDER BY COALESCE(created_at,'') DESC, id DESC LIMIT %d`,
					corpusTable("memories"), sqlLiteral(ts), sqlLiteral(ts), id, walkPageSize)
			},
			decode: decodeMemory,
		},
		{
			kind: "exchanges", initial: joinCursor(newestTimeCursor, newestIDCursor),
			query: func(cursor string) string {
				ts, id := splitCursor(cursor)
				return fmt.Sprintf(`SELECT e.id,COALESCE(e.session_id,'') AS session_id,e.exchange_number,
					COALESCE(e.human_text,'') AS human_text,COALESCE(e.agent_text,'') AS agent_text,
					COALESCE(e.human_timestamp, e.agent_timestamp, s.started_at, '') AS occurred_at,
					COALESCE(s.title,'') AS context_title, COALESCE(%s,'') AS context_project
					FROM %s e LEFT JOIN %s s ON s.session_id = e.session_id
					WHERE (COALESCE(e.human_text,'') <> '' OR COALESCE(e.agent_text,'') <> '')
					AND (COALESCE(e.human_timestamp, e.agent_timestamp, s.started_at, '') < %s
					OR (COALESCE(e.human_timestamp, e.agent_timestamp, s.started_at, '') = %s AND e.id < %s))
					ORDER BY occurred_at DESC, e.id DESC LIMIT %d`,
					strings.ReplaceAll(sessionProjectName, "metadata", "s.metadata"),
					corpusTable("exchanges"), corpusTable("sessions"),
					sqlLiteral(ts), sqlLiteral(ts), id, walkPageSize)
			},
			decode: decodeExchange,
		},
		{
			kind: "thinking_blocks", initial: joinCursor(newestTimeCursor, newestIDCursor),
			query: func(cursor string) string {
				ts, id := splitCursor(cursor)
				return fmt.Sprintf(`WITH ordered_thinking AS (
					SELECT t.id,COALESCE(t.session_id,'') AS session_id,t.exchange_number,
						t.position_in_session,COALESCE(t.full_text,'') AS text,
						COALESCE(s.title,'') AS context_title, COALESCE(%s,'') AS context_project,
						COALESCE(e.agent_timestamp,e.human_timestamp,s.started_at,'') AS occurred_at
					FROM %s AS t
					LEFT JOIN %s AS e ON e.session_id=t.session_id AND e.exchange_number=t.exchange_number
					LEFT JOIN %s AS s ON s.session_id=t.session_id
					WHERE COALESCE(t.full_text,'') <> ''
				) SELECT * FROM ordered_thinking
				WHERE occurred_at < %s OR (occurred_at = %s AND id < %s)
				ORDER BY occurred_at DESC,id DESC LIMIT %d`,
					strings.ReplaceAll(sessionProjectName, "metadata", "s.metadata"),
					corpusTable("thinking_blocks"), corpusTable("exchanges"), corpusTable("sessions"),
					sqlLiteral(ts), sqlLiteral(ts), id, walkPageSize)
			},
			decode: decodeThinking,
		},
		{
			kind: "sessions", initial: joinCursor(newestTimeCursor, "~"),
			query: func(cursor string) string {
				ts, id := splitCursor(cursor)
				return fmt.Sprintf(`SELECT session_id,COALESCE(title,'') AS title,
					%s AS project_name, COALESCE(started_at,'') AS occurred_at FROM %s
					WHERE (COALESCE(title,'') <> '' OR %s <> '')
					AND (COALESCE(started_at,'') < %s OR (COALESCE(started_at,'') = %s AND session_id < %s))
					ORDER BY COALESCE(started_at,'') DESC, session_id DESC LIMIT %d`,
					sessionProjectName, corpusTable("sessions"), sessionProjectName,
					sqlLiteral(ts), sqlLiteral(ts), sqlLiteral(id), walkPageSize)
			},
			decode: decodeSession,
		},
	}
}

func splitCursor(cursor string) (string, string) {
	ts, id, ok := strings.Cut(cursor, "|")
	if !ok {
		return newestTimeCursor, newestIDCursor
	}
	return ts, id
}

func joinCursor(ts, id string) string { return ts + "|" + id }

func decodeMemory(values map[string]any) (sourceRow, string, error) {
	id, err := integer(values, "id")
	if err != nil {
		return sourceRow{}, "", err
	}
	row := sourceRow{kind: "memories", text: stringValue(values["content"]),
		sessionID: stringValue(values["source_session"]), layer: stringValue(values["layer"]),
		origin: stringValue(values["origin"]), createdAt: stringValue(values["created_at"]),
		occurredAt: stringValue(values["created_at"]), project: stringValue(values["project"])}
	row.ordinal, row.hasOrdinal = nullableInteger(values["source_sequence"])
	var tags map[string]any
	if json.Unmarshal([]byte(stringValue(values["metadata"])), &tags) == nil {
		row.cronSource, _ = tags["_cron_source"].(string)
		row.filePath, _ = tags["file_path"].(string)
	}
	if row.cronSource == "" {
		row.cronSource = stringValue(values["source_agent"])
	}
	return row, joinCursor(row.occurredAt, strconv.FormatInt(id, 10)), nil
}

func decodeExchange(values map[string]any) (sourceRow, string, error) {
	id, err := integer(values, "id")
	if err != nil {
		return sourceRow{}, "", err
	}
	row := sourceRow{kind: "exchanges", sessionID: stringValue(values["session_id"]),
		text: stringValue(values["text"]), title: stringValue(values["context_title"]),
		project: stringValue(values["context_project"]), occurredAt: stringValue(values["occurred_at"])}
	row.ordinal, row.hasOrdinal = nullableInteger(values["exchange_number"])
	return row, joinCursor(row.occurredAt, strconv.FormatInt(id, 10)), nil
}

func decodeThinking(values map[string]any) (sourceRow, string, error) {
	id, err := integer(values, "id")
	if err != nil {
		return sourceRow{}, "", err
	}
	row := sourceRow{kind: "thinking_blocks", sessionID: stringValue(values["session_id"]),
		text: stringValue(values["text"]), title: stringValue(values["context_title"]),
		project: stringValue(values["context_project"]), occurredAt: stringValue(values["occurred_at"])}
	row.ordinal, row.hasOrdinal = nullableInteger(values["exchange_number"])
	if position, ok := nullableFloat(values["position_in_session"]); ok {
		row.position = strconv.FormatFloat(position, 'g', -1, 64)
	}
	return row, joinCursor(row.occurredAt, strconv.FormatInt(id, 10)), nil
}

func decodeSession(values map[string]any) (sourceRow, string, error) {
	id := stringValue(values["session_id"])
	if id == "" {
		return sourceRow{}, "", fmt.Errorf("session_id is empty")
	}
	title := stringValue(values["title"])
	project := stringValue(values["project_name"])
	text := sessionEmbeddingText(title, project)
	occurred := stringValue(values["occurred_at"])
	return sourceRow{kind: "sessions", sessionID: id, text: text, title: cleanSessionField(title),
		project: cleanSessionField(project), occurredAt: occurred}, joinCursor(occurred, id), nil
}

func expandDecoded(row sourceRow, values map[string]any) []sourceRow {
	switch row.kind {
	case "exchanges":
		return expandColumnRows(row, []string{"human_text", "agent_text"}, values)
	case "sessions":
		cleaned := map[string]any{
			"title":   cleanSessionField(stringValue(values["title"])),
			"project": cleanSessionField(stringValue(values["project_name"])),
		}
		return expandColumnRows(row, []string{"title", "project"}, cleaned)
	default:
		if strings.TrimSpace(row.text) == "" {
			return nil
		}
		return []sourceRow{row}
	}
}

func sessionEmbeddingText(title, projectName string) string {
	values := [2]string{title, projectName}
	fields := make([]string, 0, len(values))
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		value = cleanSessionField(value)
		if value != "" && !seen[value] {
			fields = append(fields, value)
			seen[value] = true
		}
	}
	return strings.Join(fields, "\n")
}

func cleanSessionField(value string) string {
	value = strings.TrimSpace(stripSessionJSON(value))
	if value == "" {
		return ""
	}
	value = sessionJSONScalarFragment.ReplaceAllString(value, " ")
	value = sessionJSONKeyFragment.ReplaceAllString(value, " ")
	value = structuralSessionToken.ReplaceAllString(value, " ")
	fields := strings.Fields(value)
	clean := fields[:0]
	for _, field := range fields {
		candidate := strings.Trim(field, `"'()[]{}<>,;:!?.`)
		if sessionPathToken(candidate) {
			break
		}
		if candidate == "" || sessionHexToken(candidate) {
			continue
		}
		clean = append(clean, field)
	}
	return strings.Join(clean, " ")
}

func sessionPathToken(value string) bool {
	if value == "" {
		return false
	}
	if strings.HasPrefix(value, "/") || strings.HasPrefix(value, `\`) ||
		strings.HasPrefix(value, "./") || strings.HasPrefix(value, "../") ||
		strings.HasPrefix(value, "~/") || strings.Contains(value, `\`) {
		return true
	}
	if len(value) >= 3 && value[1] == ':' && strings.ContainsAny(value[2:3], `/\`) {
		return true
	}
	if strings.Count(value, "/") >= 2 {
		return true
	}
	if before, after, ok := strings.Cut(value, "/"); ok {
		return before == "" || after == "" || !sessionSlashLanguage(before, after)
	}
	return false
}

func sessionSlashLanguage(before, after string) bool {
	if before == "" || after == "" {
		return false
	}
	pair := strings.ToLower(before + "/" + after)
	switch pair {
	case "and/or", "before/after", "client/server", "human/agent", "input/output",
		"left/right", "on/off", "parent/child", "pass/fail", "producer/consumer",
		"read/write", "request/response", "source/target", "up/down", "yes/no":
		return true
	}
	if before == strings.ToUpper(before) {
		if len(before) > 4 || len(after) > 4 {
			return false
		}
		for _, character := range before {
			if character < 'A' || character > 'Z' {
				return false
			}
		}
		for _, character := range after {
			if (character < 'A' || character > 'Z') && (character < '0' || character > '9') {
				return false
			}
		}
		return true
	}
	return false
}

func sessionHexToken(value string) bool {
	raw := value
	if strings.HasPrefix(raw, "0x") || strings.HasPrefix(raw, "0X") {
		raw = raw[2:]
	}
	if len(raw) < 7 {
		return false
	}
	hasDigit := false
	for _, character := range raw {
		switch {
		case character >= '0' && character <= '9':
			hasDigit = true
		case character >= 'a' && character <= 'f', character >= 'A' && character <= 'F':
		default:
			return false
		}
	}
	return hasDigit || len(raw) >= 8
}

func stripSessionJSON(value string) string {
	var clean strings.Builder
	for len(value) > 0 {
		start := strings.IndexAny(value, "{[")
		if start < 0 {
			clean.WriteString(value)
			break
		}
		clean.WriteString(value[:start])
		decoder := json.NewDecoder(strings.NewReader(value[start:]))
		var decoded any
		if err := decoder.Decode(&decoded); err != nil {
			clean.WriteByte(value[start])
			value = value[start+1:]
			continue
		}
		consumed := int(decoder.InputOffset())
		if consumed == 0 {
			clean.WriteByte(value[start])
			value = value[start+1:]
			continue
		}
		clean.WriteByte(' ')
		value = value[start+consumed:]
	}
	return clean.String()
}

func (c CoreCLI) ResolveSource(ctx context.Context, kind string, where locator) (string, error) {
	var statement string
	switch kind {
	case "sessions":
		statement = `SELECT COALESCE(title,'') AS title,` +
			sessionProjectName + ` AS project_name FROM ` + corpusTable("sessions") +
			` WHERE session_id=` + sqlLiteral(where.SessionID)
		rows, err := c.query(ctx, statement)
		if err != nil {
			return "", err
		}
		for _, values := range rows {
			text := sessionEmbeddingText(stringValue(values["title"]), stringValue(values["project_name"]))
			if (sourceRow{kind: kind, text: text}).identity() == where.Identity {
				return text, nil
			}
		}
		return "", nil
	case "exchanges":
		statement = `SELECT ` + exchangeText + ` AS text FROM ` + corpusTable("exchanges") +
			` WHERE session_id=` + sqlLiteral(where.SessionID)
		if where.HasOrdinal {
			statement += fmt.Sprintf(` AND exchange_number=%d`, where.Ordinal)
		} else {
			statement += ` AND exchange_number IS NULL`
		}
		return c.resolveIdentity(ctx, kind, where, statement)
	case "thinking_blocks":
		if !where.HasOrdinal || where.Position == "" {
			statement = `SELECT COALESCE(full_text,'') AS text FROM ` + corpusTable("thinking_blocks") +
				` WHERE session_id=` + sqlLiteral(where.SessionID) +
				` AND (exchange_number IS NULL OR position_in_session IS NULL)`
			return c.resolveIdentity(ctx, kind, where, statement)
		}
		position, err := strconv.ParseFloat(where.Position, 64)
		if err != nil {
			return "", fmt.Errorf("decode thinking block position %q: %w", where.Position, err)
		}
		statement = fmt.Sprintf(`SELECT COALESCE(full_text,'') AS text FROM %s WHERE session_id=%s AND exchange_number=%d AND position_in_session=%s`,
			corpusTable("thinking_blocks"), sqlLiteral(where.SessionID), where.Ordinal,
			strconv.FormatFloat(position, 'g', -1, 64))
		return c.resolveIdentity(ctx, kind, where, statement)
	case "memories":
		switch {
		case where.SessionID != "" && where.HasOrdinal:
			statement = fmt.Sprintf(`SELECT content AS text FROM %s WHERE source_session=%s AND source_sequence=%d`,
				corpusTable("memories"), sqlLiteral(where.SessionID), where.Ordinal)
		case where.FilePath != "" && where.CronSource != "":
			statement = fmt.Sprintf(`SELECT content AS text FROM %s WHERE json_extract(metadata,'$.file_path')=%s AND (json_extract(metadata,'$._cron_source')=%s OR source_agent=%s)`,
				corpusTable("memories"), sqlLiteral(where.FilePath), sqlLiteral(where.CronSource),
				sqlLiteral(where.CronSource))
		default:
			statement = fmt.Sprintf(`SELECT content AS text FROM %s WHERE layer=%s AND origin=%s AND COALESCE(created_at,'')=%s`,
				corpusTable("memories"), sqlLiteral(where.Layer), sqlLiteral(where.Origin),
				sqlLiteral(where.CreatedAt))
		}
		return c.resolveIdentity(ctx, kind, where, statement)
	default:
		return "", fmt.Errorf("unknown vector source %q", kind)
	}
}

func (c CoreCLI) resolveIdentity(ctx context.Context, kind string, where locator, statement string) (string, error) {
	rows, err := c.query(ctx, statement)
	if err != nil {
		return "", err
	}
	for _, values := range rows {
		text := stringValue(values["text"])
		candidate := sourceRow{kind: kind, text: text, layer: where.Layer,
			origin: where.Origin, createdAt: where.CreatedAt}
		if candidate.identity() == where.Identity {
			return text, nil
		}
	}
	return "", nil
}

func (c CoreCLI) query(ctx context.Context, statement string) ([]map[string]any, error) {
	if strings.TrimSpace(c.Executable) == "" {
		return nil, fmt.Errorf("roca executable is required")
	}
	args := []string{"--json"}
	if c.DBPath != "" {
		args = append(args, "--db-path", c.DBPath)
	}
	args = append(args, "exec", "--timeout-ms", "0", "--max-chars", strconv.Itoa(coreFieldBudget), statement)
	run := c.Run
	if run == nil {
		run = runCommand
	}
	raw, err := run(ctx, c.Executable, args...)
	if err != nil {
		return nil, err
	}
	var result execResult
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&result); err != nil {
		return nil, fmt.Errorf("decode roca exec response: %w", err)
	}
	return result.Rows, nil
}

func runCommand(ctx context.Context, executable string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, executable, args...)
	raw, err := command.Output()
	if err == nil {
		return raw, nil
	}
	message := ""
	if exit, ok := err.(*exec.ExitError); ok {
		message = strings.TrimSpace(string(exit.Stderr))
	}
	if len(message) > 4096 {
		message = message[:4096] + "…"
	}
	if message != "" {
		return nil, fmt.Errorf("roca exec: %w: %s", err, message)
	}
	return nil, fmt.Errorf("roca exec: %w", err)
}

func sqlLiteral(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func stringValue(value any) string {
	if value == nil {
		return ""
	}
	return fmt.Sprint(value)
}

func integer(values map[string]any, key string) (int64, error) {
	value, ok := nullableInteger(values[key])
	if !ok {
		return 0, fmt.Errorf("%s is not an integer", key)
	}
	return value, nil
}

func nullableInteger(value any) (int64, bool) {
	switch typed := value.(type) {
	case float64:
		return int64(typed), typed == float64(int64(typed))
	case json.Number:
		parsed, err := typed.Int64()
		return parsed, err == nil
	case int64:
		return typed, true
	case int:
		return int64(typed), true
	default:
		return 0, false
	}
}

func nullableFloat(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case json.Number:
		parsed, err := typed.Float64()
		return parsed, err == nil
	case int64:
		return float64(typed), true
	case int:
		return float64(typed), true
	default:
		return 0, false
	}
}
