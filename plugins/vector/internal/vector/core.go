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
	Plugins    []string
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
	activeMemory       = `COALESCE(content,'') <> '' AND lower(COALESCE(layer,'')) NOT LIKE 'rocodata\_%' ESCAPE '\'`
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
	for _, source := range corePages() {
		if sourceKind != "" && source.kind != sourceKind {
			continue
		}
		cursor := source.initial
		for {
			rows, err := c.query(ctx, source.query(cursor))
			if err != nil {
				return fmt.Errorf("read core %s: %w", source.kind, err)
			}
			for _, values := range rows {
				row, next, err := source.decode(values)
				if err != nil {
					return fmt.Errorf("decode core %s: %w", source.kind, err)
				}
				cursor = next
				if err := visit(row); err != nil {
					return err
				}
			}
			if len(rows) < walkPageSize {
				break
			}
		}
	}
	for _, rawPlugin := range c.Plugins {
		plugin, schema, err := canonicalPlugin(rawPlugin)
		if err != nil {
			return err
		}
		if sourceKind != "" && sourceKind != "plugin:"+plugin {
			continue
		}
		if err := c.walkPluginSources(ctx, plugin, schema, visit); err != nil {
			return err
		}
	}
	return nil
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

func (c CoreCLI) walkPluginSources(ctx context.Context, plugin, schema string, visit func(sourceRow) error) error {
	cursor := ""
	for {
		rows, err := c.query(ctx, pluginPageQuery(schema, cursor))
		if err != nil {
			return fmt.Errorf("read data plugin %s: %w", plugin, err)
		}
		for _, values := range rows {
			row, next, err := decodePlugin(values, plugin)
			if err != nil {
				return fmt.Errorf("decode data plugin %s: %w", plugin, err)
			}
			cursor = next
			if err := visit(row); err != nil {
				return err
			}
		}
		if len(rows) < walkPageSize {
			return nil
		}
	}
}

func pluginPageQuery(schema, cursor string) string {
	return fmt.Sprintf(`SELECT c.chunk_id,c.material_id,c.chunk_index,c.content,c.content_sha256,
			c.start_offset,c.end_offset,c.updated_at,
			m.source_ref,m.title,COALESCE(m.topic_id,'') AS topic_id,
			COALESCE(m.topic_label,'') AS topic_label,COALESCE(m.channel_id,'') AS channel_id,
			COALESCE(m.channel_label,'') AS channel_label,COALESCE(m.video_id,'') AS video_id,
			COALESCE(m.published_at,'') AS published_at
			FROM %s.text_chunks c JOIN %s.materials m ON m.material_id=c.material_id
			WHERE m.status='active' AND COALESCE(c.content,'') <> '' AND c.chunk_id > %s
			ORDER BY c.chunk_id LIMIT %d`, schema, schema, sqlLiteral(cursor), walkPageSize)
}

func decodePlugin(values map[string]any, plugin string) (sourceRow, string, error) {
	chunkID := stringValue(values["chunk_id"])
	materialID := stringValue(values["material_id"])
	if chunkID == "" || materialID == "" {
		return sourceRow{}, "", fmt.Errorf("chunk_id and material_id are required")
	}
	chunkIndex, ok := nullableInteger(values["chunk_index"])
	if !ok || chunkIndex < 0 {
		return sourceRow{}, "", fmt.Errorf("chunk_index is invalid for %s", chunkID)
	}
	where := Locator{
		Plugin: plugin, MaterialID: materialID, ChunkID: chunkID, ChunkIndex: int(chunkIndex),
		SourceRef: stringValue(values["source_ref"]), Title: stringValue(values["title"]),
		TopicID: stringValue(values["topic_id"]), TopicLabel: stringValue(values["topic_label"]),
		ChannelID: stringValue(values["channel_id"]), ChannelLabel: stringValue(values["channel_label"]),
		VideoID: stringValue(values["video_id"]), PublishedAt: stringValue(values["published_at"]),
		ContentSHA256: stringValue(values["content_sha256"]),
		DedupeKey:     plugin + ":material:" + materialID,
	}
	return sourceRow{kind: "plugin:" + plugin, text: stringValue(values["content"]),
		sourceID: chunkID, preChunked: true, plugin: where}, chunkID, nil
}

func corePages() []corePage {
	return []corePage{
		{
			kind: "memories", initial: "0",
			query: func(cursor string) string {
				return fmt.Sprintf(`SELECT id,content,COALESCE(source_session,'') AS source_session,
					source_sequence,COALESCE(source_agent,'') AS source_agent,
					COALESCE(metadata,'{}') AS metadata,COALESCE(layer,'') AS layer,
					COALESCE(origin,'') AS origin,COALESCE(created_at,'') AS created_at
					FROM %s WHERE %s AND id > %s ORDER BY id LIMIT %d`,
					corpusTable("memories"), activeMemory, cursor, walkPageSize)
			},
			decode: decodeMemory,
		},
		{
			kind: "exchanges", initial: "0",
			query: func(cursor string) string {
				return fmt.Sprintf(`SELECT id,COALESCE(session_id,'') AS session_id,exchange_number,
					%s AS text FROM %s
					WHERE (COALESCE(human_text,'') <> '' OR COALESCE(agent_text,'') <> '')
					AND id > %s ORDER BY id LIMIT %d`, exchangeText, corpusTable("exchanges"), cursor, walkPageSize)
			},
			decode: decodeExchange,
		},
		{
			kind: "thinking_blocks", initial: "0",
			query: func(cursor string) string {
				return fmt.Sprintf(`SELECT id,COALESCE(session_id,'') AS session_id,exchange_number,
					position_in_session,COALESCE(full_text,'') AS text FROM %s
					WHERE COALESCE(full_text,'') <> '' AND id > %s ORDER BY id LIMIT %d`,
					corpusTable("thinking_blocks"), cursor, walkPageSize)
			},
			decode: decodeThinking,
		},
		{
			kind: "sessions", initial: "",
			query: func(cursor string) string {
				return fmt.Sprintf(`SELECT session_id,COALESCE(title,'') AS title,
					%s AS project_name FROM %s
					WHERE (COALESCE(title,'') <> '' OR %s <> '')
					AND session_id > %s ORDER BY session_id LIMIT %d`,
					sessionProjectName, corpusTable("sessions"), sessionProjectName,
					sqlLiteral(cursor), walkPageSize)
			},
			decode: decodeSession,
		},
	}
}

func decodeMemory(values map[string]any) (sourceRow, string, error) {
	id, err := integer(values, "id")
	if err != nil {
		return sourceRow{}, "", err
	}
	row := sourceRow{kind: "memories", text: stringValue(values["content"]),
		sessionID: stringValue(values["source_session"]), layer: stringValue(values["layer"]),
		origin: stringValue(values["origin"]), createdAt: stringValue(values["created_at"])}
	row.ordinal, row.hasOrdinal = nullableInteger(values["source_sequence"])
	var tags map[string]any
	if json.Unmarshal([]byte(stringValue(values["metadata"])), &tags) == nil {
		row.cronSource, _ = tags["_cron_source"].(string)
		row.filePath, _ = tags["file_path"].(string)
	}
	if row.cronSource == "" {
		row.cronSource = stringValue(values["source_agent"])
	}
	return row, strconv.FormatInt(id, 10), nil
}

func decodeExchange(values map[string]any) (sourceRow, string, error) {
	id, err := integer(values, "id")
	if err != nil {
		return sourceRow{}, "", err
	}
	row := sourceRow{kind: "exchanges", sessionID: stringValue(values["session_id"]),
		text: stringValue(values["text"])}
	row.ordinal, row.hasOrdinal = nullableInteger(values["exchange_number"])
	return row, strconv.FormatInt(id, 10), nil
}

func decodeThinking(values map[string]any) (sourceRow, string, error) {
	id, err := integer(values, "id")
	if err != nil {
		return sourceRow{}, "", err
	}
	row := sourceRow{kind: "thinking_blocks", sessionID: stringValue(values["session_id"]),
		text: stringValue(values["text"])}
	row.ordinal, row.hasOrdinal = nullableInteger(values["exchange_number"])
	if position, ok := nullableFloat(values["position_in_session"]); ok {
		row.position = strconv.FormatFloat(position, 'g', -1, 64)
	}
	return row, strconv.FormatInt(id, 10), nil
}

func decodeSession(values map[string]any) (sourceRow, string, error) {
	id := stringValue(values["session_id"])
	if id == "" {
		return sourceRow{}, "", fmt.Errorf("session_id is empty")
	}
	text := sessionEmbeddingText(stringValue(values["title"]), stringValue(values["project_name"]))
	return sourceRow{kind: "sessions", sessionID: id, text: text}, id, nil
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

func (c CoreCLI) ResolveSource(ctx context.Context, kind string, where Locator) (string, error) {
	if strings.HasPrefix(kind, "plugin:") {
		plugin := strings.TrimPrefix(kind, "plugin:")
		if plugin == "" {
			return "", fmt.Errorf("plugin source kind is empty")
		}
		return c.resolvePluginSource(ctx, plugin, where)
	}
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
			statement = fmt.Sprintf(`SELECT content AS text FROM %s WHERE %s AND source_session=%s AND source_sequence=%d`,
				corpusTable("memories"), activeMemory, sqlLiteral(where.SessionID), where.Ordinal)
		case where.FilePath != "" && where.CronSource != "":
			statement = fmt.Sprintf(`SELECT content AS text FROM %s WHERE %s AND json_extract(metadata,'$.file_path')=%s AND (json_extract(metadata,'$._cron_source')=%s OR source_agent=%s)`,
				corpusTable("memories"), activeMemory, sqlLiteral(where.FilePath), sqlLiteral(where.CronSource),
				sqlLiteral(where.CronSource))
		default:
			statement = fmt.Sprintf(`SELECT content AS text FROM %s WHERE %s AND layer=%s AND origin=%s AND COALESCE(created_at,'')=%s`,
				corpusTable("memories"), activeMemory, sqlLiteral(where.Layer), sqlLiteral(where.Origin),
				sqlLiteral(where.CreatedAt))
		}
		return c.resolveIdentity(ctx, kind, where, statement)
	default:
		return "", fmt.Errorf("unknown vector source %q", kind)
	}
}

func (c CoreCLI) resolvePluginSource(ctx context.Context, plugin string, where Locator) (string, error) {
	canonical, schema, err := canonicalPlugin(plugin)
	if err != nil {
		return "", err
	}
	if where.Plugin != "" && where.Plugin != canonical {
		return "", fmt.Errorf("plugin locator belongs to %q, not %q", where.Plugin, canonical)
	}
	if where.ChunkID == "" {
		return "", fmt.Errorf("plugin locator for %s has no chunk_id", canonical)
	}
	statement := fmt.Sprintf(`SELECT c.content AS text FROM %s.text_chunks c
		JOIN %s.materials m ON m.material_id=c.material_id
		WHERE c.chunk_id=%s AND m.status='active' LIMIT 1`, schema, schema, sqlLiteral(where.ChunkID))
	rows, err := c.query(ctx, statement)
	if err != nil || len(rows) == 0 {
		return "", err
	}
	return stringValue(rows[0]["text"]), nil
}

func canonicalPlugin(name string) (string, string, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return "", "", fmt.Errorf("data plugin name is empty")
	}
	for index, r := range name {
		valid := r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '-'
		if !valid || (index == 0 && (r == '_' || r == '-')) {
			return "", "", fmt.Errorf("invalid data plugin name %q", name)
		}
	}
	schema := "plugin_" + strings.NewReplacer("-", "_").Replace(name)
	return name, schema, nil
}

func (c CoreCLI) resolveIdentity(ctx context.Context, kind string, where Locator, statement string) (string, error) {
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
	args = append(args, "exec", "--max-chars", strconv.Itoa(coreFieldBudget), statement)
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
