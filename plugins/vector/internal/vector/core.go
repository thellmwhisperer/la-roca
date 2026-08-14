package vector

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
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
	sessionText  = `trim(COALESCE(title,'') || char(10) || COALESCE(project,'') || char(10) || COALESCE(metadata,''))`
)

func (c CoreCLI) WalkSources(ctx context.Context, visit func(sourceRow) error) error {
	for _, source := range corePages() {
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
					FROM memories WHERE COALESCE(content,'') <> '' AND id > %s ORDER BY id LIMIT %d`,
					cursor, walkPageSize)
			},
			decode: decodeMemory,
		},
		{
			kind: "exchanges", initial: "0",
			query: func(cursor string) string {
				return fmt.Sprintf(`SELECT id,COALESCE(session_id,'') AS session_id,exchange_number,
					%s AS text FROM exchanges
					WHERE (COALESCE(human_text,'') <> '' OR COALESCE(agent_text,'') <> '')
					AND id > %s ORDER BY id LIMIT %d`, exchangeText, cursor, walkPageSize)
			},
			decode: decodeExchange,
		},
		{
			kind: "thinking_blocks", initial: "0",
			query: func(cursor string) string {
				return fmt.Sprintf(`SELECT id,COALESCE(session_id,'') AS session_id,exchange_number,
					position_in_session,COALESCE(full_text,'') AS text FROM thinking_blocks
					WHERE COALESCE(full_text,'') <> '' AND id > %s ORDER BY id LIMIT %d`,
					cursor, walkPageSize)
			},
			decode: decodeThinking,
		},
		{
			kind: "sessions", initial: "",
			query: func(cursor string) string {
				return fmt.Sprintf(`SELECT session_id,%s AS text FROM sessions
					WHERE (COALESCE(title,'') <> '' OR COALESCE(project,'') <> '' OR COALESCE(metadata,'') NOT IN ('','{}'))
					AND session_id > %s ORDER BY session_id LIMIT %d`, sessionText,
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
	return sourceRow{kind: "sessions", sessionID: id, text: stringValue(values["text"])}, id, nil
}

func (c CoreCLI) ResolveSource(ctx context.Context, kind string, where locator) (string, error) {
	var statement string
	switch kind {
	case "sessions":
		statement = `SELECT ` + sessionText + ` AS text FROM sessions WHERE session_id=` + sqlLiteral(where.SessionID)
	case "exchanges":
		if !where.HasOrdinal {
			statement = `SELECT ` + exchangeText + ` AS text FROM exchanges WHERE session_id=` +
				sqlLiteral(where.SessionID) + ` AND exchange_number IS NULL`
			return c.resolveIdentity(ctx, kind, where, statement)
		}
		statement = fmt.Sprintf(`SELECT %s AS text FROM exchanges WHERE session_id=%s AND exchange_number=%d ORDER BY id DESC LIMIT 1`,
			exchangeText, sqlLiteral(where.SessionID), where.Ordinal)
	case "thinking_blocks":
		if !where.HasOrdinal || where.Position == "" {
			statement = `SELECT COALESCE(full_text,'') AS text FROM thinking_blocks WHERE session_id=` +
				sqlLiteral(where.SessionID) + ` AND (exchange_number IS NULL OR position_in_session IS NULL)`
			return c.resolveIdentity(ctx, kind, where, statement)
		}
		position, err := strconv.ParseFloat(where.Position, 64)
		if err != nil {
			return "", fmt.Errorf("decode thinking block position %q: %w", where.Position, err)
		}
		statement = fmt.Sprintf(`SELECT COALESCE(full_text,'') AS text FROM thinking_blocks WHERE session_id=%s AND exchange_number=%d AND position_in_session=%s ORDER BY id DESC LIMIT 1`,
			sqlLiteral(where.SessionID), where.Ordinal, strconv.FormatFloat(position, 'g', -1, 64))
	case "memories":
		switch {
		case where.SessionID != "" && where.HasOrdinal:
			statement = fmt.Sprintf(`SELECT content AS text FROM memories WHERE source_session=%s AND source_sequence=%d ORDER BY id DESC LIMIT 1`,
				sqlLiteral(where.SessionID), where.Ordinal)
		case where.FilePath != "" && where.CronSource != "":
			statement = fmt.Sprintf(`SELECT content AS text FROM memories WHERE json_extract(metadata,'$.file_path')=%s AND (json_extract(metadata,'$._cron_source')=%s OR source_agent=%s) ORDER BY id DESC LIMIT 1`,
				sqlLiteral(where.FilePath), sqlLiteral(where.CronSource), sqlLiteral(where.CronSource))
		default:
			statement = fmt.Sprintf(`SELECT content AS text FROM memories WHERE layer=%s AND origin=%s AND COALESCE(created_at,'')=%s`,
				sqlLiteral(where.Layer), sqlLiteral(where.Origin), sqlLiteral(where.CreatedAt))
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
