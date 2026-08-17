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
	exchangeText = `trim(COALESCE(human_text,'') || CASE WHEN human_text IS NOT NULL AND agent_text IS NOT NULL THEN char(10)||char(10) ELSE '' END || COALESCE(agent_text,''))`
	corpusSchema = "plugin_roca_corpus"
)

var structuralSessionToken = regexp.MustCompile(`(?i)(?:\b[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}\b|\b[0-9a-f]{16,}\b|\b[0-9A-HJKMNP-TV-Z]{26}\b)`)

func corpusTable(name string) string { return corpusSchema + "." + name }

func (c CoreCLI) WalkSources(ctx context.Context, sourceKind string, visit func(sourceRow) error) error {
	if err := validateSourceKind(sourceKind); err != nil {
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
	return nil
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
					FROM %s WHERE COALESCE(content,'') <> '' AND id > %s ORDER BY id LIMIT %d`,
					corpusTable("memories"), cursor, walkPageSize)
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
					COALESCE(project,'') AS project FROM %s
					WHERE (COALESCE(title,'') <> '' OR COALESCE(project,'') <> '')
					AND session_id > %s ORDER BY session_id LIMIT %d`,
					corpusTable("sessions"), sqlLiteral(cursor), walkPageSize)
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
	text := sessionEmbeddingText(stringValue(values["title"]), stringValue(values["project"]))
	return sourceRow{kind: "sessions", sessionID: id, text: text}, id, nil
}

func sessionEmbeddingText(title, project string) string {
	fields := make([]string, 0, 2)
	if title = cleanSessionField(title); title != "" {
		fields = append(fields, title)
	}
	if project = cleanSessionProject(project); project != "" {
		fields = append(fields, project)
	}
	return strings.Join(fields, "\n")
}

func cleanSessionField(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if (strings.HasPrefix(value, "{") || strings.HasPrefix(value, "[")) && json.Valid([]byte(value)) {
		return ""
	}
	return strings.Join(strings.Fields(structuralSessionToken.ReplaceAllString(value, " ")), " ")
}

func cleanSessionProject(value string) string {
	if strings.ContainsAny(value, `/\`) || structuralSessionToken.MatchString(value) {
		return ""
	}
	return cleanSessionField(value)
}

func (c CoreCLI) ResolveSource(ctx context.Context, kind string, where locator) (string, error) {
	var statement string
	switch kind {
	case "sessions":
		statement = `SELECT COALESCE(title,'') AS title,COALESCE(project,'') AS project FROM ` +
			corpusTable("sessions") + ` WHERE session_id=` + sqlLiteral(where.SessionID)
		rows, err := c.query(ctx, statement)
		if err != nil || len(rows) == 0 {
			return "", err
		}
		return sessionEmbeddingText(stringValue(rows[0]["title"]), stringValue(rows[0]["project"])), nil
	case "exchanges":
		if !where.HasOrdinal {
			statement = `SELECT ` + exchangeText + ` AS text FROM ` + corpusTable("exchanges") +
				` WHERE session_id=` + sqlLiteral(where.SessionID) + ` AND exchange_number IS NULL`
			return c.resolveIdentity(ctx, kind, where, statement)
		}
		statement = fmt.Sprintf(`SELECT %s AS text FROM %s WHERE session_id=%s AND exchange_number=%d ORDER BY id DESC LIMIT 1`,
			exchangeText, corpusTable("exchanges"), sqlLiteral(where.SessionID), where.Ordinal)
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
		statement = fmt.Sprintf(`SELECT COALESCE(full_text,'') AS text FROM %s WHERE session_id=%s AND exchange_number=%d AND position_in_session=%s ORDER BY id DESC LIMIT 1`,
			corpusTable("thinking_blocks"), sqlLiteral(where.SessionID), where.Ordinal,
			strconv.FormatFloat(position, 'g', -1, 64))
	case "memories":
		switch {
		case where.SessionID != "" && where.HasOrdinal:
			statement = fmt.Sprintf(`SELECT content AS text FROM %s WHERE source_session=%s AND source_sequence=%d ORDER BY id DESC LIMIT 1`,
				corpusTable("memories"), sqlLiteral(where.SessionID), where.Ordinal)
		case where.FilePath != "" && where.CronSource != "":
			statement = fmt.Sprintf(`SELECT content AS text FROM %s WHERE json_extract(metadata,'$.file_path')=%s AND (json_extract(metadata,'$._cron_source')=%s OR source_agent=%s) ORDER BY id DESC LIMIT 1`,
				corpusTable("memories"), sqlLiteral(where.FilePath), sqlLiteral(where.CronSource),
				sqlLiteral(where.CronSource))
		default:
			statement = fmt.Sprintf(`SELECT content AS text FROM %s WHERE layer=%s AND origin=%s AND COALESCE(created_at,'')=%s`,
				corpusTable("memories"), sqlLiteral(where.Layer), sqlLiteral(where.Origin),
				sqlLiteral(where.CreatedAt))
			return c.resolveIdentity(ctx, kind, where, statement)
		}
	default:
		return "", fmt.Errorf("unknown vector source %q", kind)
	}
	rows, err := c.query(ctx, statement)
	if err != nil || len(rows) == 0 {
		return "", err
	}
	return stringValue(rows[0]["text"]), nil
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
