package ingest

import (
	"bufio"
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/thellmwhisperer/la-roca/pkg/parsers"
)

// OpenCode keeps its conversations in its own SQLite, normalized into four
// tables. What this reader has to get right is which turns are finished: a turn
// whose tools are still running is a turn the agent is in the middle of, and
// ingesting it would store an answer that had not been given yet.

// openCodeScope is where OpenCode's exchange map lives inside the session
// metadata. It is nested, and it stays nested, because existing databases may
// already carry it there; reading it anywhere else would
// renumber exchanges that already landed and duplicate the lot.
const openCodeScope = "opencode"

// openCodeSchema is the shape this build reads.
var openCodeSchema = []foreignTable{
	{"project", []string{"id", "worktree"}},
	{"session", []string{"id", "project_id", "parent_id", "directory", "version",
		"time_created", "time_updated", "agent"}},
	{"message", []string{"id", "session_id", "time_created", "time_updated", "data"}},
	{"part", []string{"id", "message_id", "session_id", "time_created", "time_updated", "data"}},
}

// openCodeMessage is the `data` document of a message row.
type openCodeMessage struct {
	Role     string `json:"role"`
	ParentID string `json:"parentID"`
	Time     struct {
		Created   *float64 `json:"created"`
		Completed *float64 `json:"completed"`
	} `json:"time"`
	ModelID    string                `json:"modelID"`
	ProviderID string                `json:"providerID"`
	Cost       *float64              `json:"cost"`
	Tokens     *durableMessageTokens `json:"tokens"`
}

type openCodeRow = durableMessageRow[openCodeMessage]

// ReadOpenCode projects an OpenCode database onto normalized records.
//
// The whole read happens before anything is written, and inside one transaction
// over a `query_only` connection: a snapshot, not a live tail, so a session that
// grows while this runs is read whole or not at all.
func ReadOpenCode(ctx context.Context, path string) (parsers.Records, []string, error) {
	db, err := openForeignSource(ctx, "OpenCode", path, openCodeSchema)
	if err != nil {
		return parsers.Records{}, nil, err
	}
	defer db.Close()

	projects, err := queryRows(ctx, db, `SELECT id, worktree FROM project ORDER BY id`)
	if err != nil {
		return parsers.Records{}, nil, err
	}
	worktrees := map[string]string{}
	for _, project := range projects {
		worktrees[project.text("id")] = project.text("worktree")
	}

	sessions, err := queryRows(ctx, db,
		`SELECT id, project_id, parent_id, directory, version, time_created, time_updated, agent
		 FROM session ORDER BY time_created, id`)
	if err != nil {
		return parsers.Records{}, nil, err
	}
	sessions, duplicateComplaints := uniqueOpenCodeSessions(sessions)
	messages, malformedMessages, err := readDurableMessageDocuments[openCodeMessage](ctx, db,
		`SELECT id, session_id, time_created, time_updated, data FROM message
		 ORDER BY time_created, id`, true)
	if err != nil {
		return parsers.Records{}, nil, err
	}
	parts, malformedParts, err := readDurableMessageDocuments[openCodeMessage](ctx, db,
		`SELECT id, message_id, session_id, time_created, time_updated, data FROM part
		 ORDER BY time_created, id`, false)
	if err != nil {
		return parsers.Records{}, nil, err
	}
	todosBySession, err := openCodeTodos(ctx, db)
	if err != nil {
		return parsers.Records{}, nil, err
	}

	// A session with one unreadable row is left alone whole. Ingesting the rest of
	// it would produce a conversation with a hole in it that nothing later can
	// tell from a complete one. The message's reason wins over the part's.
	store := prepareDurableMessageStore(messages, parts, malformedMessages, malformedParts)
	records := parsers.Records{MessageCoverage: &parsers.MessageCoverage{
		Seen: len(messages), Skipped: map[string]int{},
	}}
	project := func(source row, messages, parts []openCodeRow) (parsers.Session, int, []parsers.Discard) {
		native := source.text("id")
		session, deferred := openCodeSession(path, source, worktrees, messages, parts,
			todosBySession[native], records.MessageCoverage)
		return session, deferred, nil
	}
	projected, deferred, discards, readComplaints :=
		projectDurableSessions("OpenCode", sessions, store, records.MessageCoverage, project)
	records.Sessions, records.Deferred, records.Discards = projected, deferred, discards
	complaints := append(duplicateComplaints, readComplaints...)
	for _, part := range parts {
		switch part.part.Type {
		case "step-start", "step-finish":
			records.Discards = append(records.Discards,
				parsers.Excluded("OpenCode step telemetry"))
		}
	}
	return records, complaints, nil
}

func uniqueOpenCodeSessions(sessions []row) ([]row, []string) {
	unique := make([]row, 0, len(sessions))
	seen := map[string]bool{}
	var complaints []string
	for _, session := range sessions {
		id := session.text("id")
		if seen[id] {
			complaints = append(complaints,
				fmt.Sprintf("OpenCode session %s: duplicate session row", id))
			continue
		}
		seen[id] = true
		unique = append(unique, session)
	}
	return unique, complaints
}

// openCodeTodos reads the optional task-list table. Older OpenCode databases
// predate it, so absence means no task list; a present table must have the shape
// this build reads or the source is refused rather than guessed at.
func openCodeTodos(ctx context.Context, db *sql.DB) (map[string][]map[string]any, error) {
	columns, err := tableColumns(ctx, db, "todo")
	if err != nil {
		return nil, err
	}
	if len(columns) == 0 {
		return map[string][]map[string]any{}, nil
	}
	required := []string{"session_id", "content", "status", "priority", "position",
		"time_created", "time_updated"}
	if err := requireColumns(ctx, db, "todo", required); err != nil {
		return nil, fmt.Errorf("OpenCode: %w", err)
	}
	rows, err := queryRows(ctx, db, `SELECT session_id, content, status, priority, position,
		time_created, time_updated FROM todo ORDER BY session_id, position`)
	if err != nil {
		return nil, err
	}
	out := map[string][]map[string]any{}
	for _, item := range rows {
		created, _ := item.number("time_created")
		updated, _ := item.number("time_updated")
		position, _ := item.number("position")
		todo := parsers.WithoutEmpty(map[string]any{
			"content": item.text("content"), "status": item.text("status"),
			"priority": item.text("priority"), "position": int(position),
			"created_at": isoFromMS(created), "updated_at": isoFromMS(updated),
		})
		out[item.text("session_id")] = append(out[item.text("session_id")], todo)
	}
	return out, nil
}

func openCodeSession(path string, source row, worktrees map[string]string,
	messages, parts []openCodeRow, todos []map[string]any,
	coverage *parsers.MessageCoverage) (parsers.Session, int) {
	native := source.text("id")
	worktree := worktrees[source.text("project_id")]
	created, _ := source.number("time_created")
	updated, _ := source.number("time_updated")

	session := parsers.Session{
		ID:                     openCodeScope + ":" + native,
		SourceAgent:            openCodeSourceAgent(source.text("agent")),
		Project:                firstNonEmpty(baseName(worktree), baseName(source.text("directory"))),
		StartedAt:              isoFromMS(created),
		EndedAt:                isoFromMS(updated),
		SnapshotUpdatedAt:      isoFromMS(updated),
		Snapshot:               true,
		ExchangeKeyScope:       openCodeScope,
		PruneUnmappedExchanges: true,
	}
	if minutes := int((updated - created) / 60000); minutes >= 0 {
		session.DurationMinutes = &minutes
	}
	if parent := source.text("parent_id"); parent != "" {
		session.ParentID = openCodeScope + ":" + parent
	}
	session.Metadata = parsers.WithoutEmpty(map[string]any{
		"source_db":         path,
		"native_session_id": native,
		"project_id":        source.text("project_id"),
		"parent_id":         source.text("parent_id"),
		"project_worktree":  worktree,
		"session_directory": source.text("directory"),
		"version":           source.text("version"),
		"agent":             source.text("agent"),
		"updated_at":        isoFromMS(updated),
		"todos":             todos,
	})
	var sessionDeferred int
	session.Exchanges, sessionDeferred = openCodeExchanges(messages, parts, coverage)
	return session, sessionDeferred
}

// openCodeExchanges preserves one exchange per durable message. OpenCode may
// write several assistant messages below one user parent, and grouping those
// siblings loses their independent model, content and tool/reasoning records.
func openCodeExchanges(messages, parts []openCodeRow,
	coverage *parsers.MessageCoverage) ([]parsers.Exchange, int) {
	partsByMessage := map[string][]openCodeRow{}
	var compactions []float64
	for _, part := range parts {
		partsByMessage[part.messageID] = append(partsByMessage[part.messageID], part)
		if part.part.Type == "compaction" && part.created != nil {
			compactions = append(compactions, *part.created)
		}
	}
	var exchanges []parsers.Exchange
	deferred := 0
	number := 0
	for _, message := range messages {
		messageParts := partsByMessage[message.id]
		role := message.message.Role
		if role != "user" && role != "assistant" {
			coverage.Skipped["unsupported message role: "+role]++
			continue
		}
		if role == "assistant" && (message.message.Time.Completed == nil || durableMessageHasLiveTool(messageParts)) {
			deferred++
			coverage.Skipped["assistant message still being written"]++
			continue
		}
		number++
		exchange := parsers.Exchange{
			Number:            number,
			SourceID:          message.id,
			Fingerprint:       openCodeFingerprint(message, messageParts),
			IsAfterCompaction: startedAfterACompaction(message, compactions),
		}
		populateDurableExchange(&exchange, role, durableTextOfParts(messageParts, "text"),
			assistantContent(messageParts), message.message.Time.Created,
			message.message.Time.Completed, message.created, message.updated)
		if role == "assistant" {
			exchange.Provenance = openCodeProvenance([]openCodeRow{message})
		}
		for _, part := range messageParts {
			switch part.part.Type {
			case "reasoning":
				if part.part.Text == "" {
					continue
				}
				exchange.Thinking = append(exchange.Thinking, parsers.Thinking{
					Text:              part.part.Text,
					WordCount:         len(strings.Fields(part.part.Text)),
					Position:          float64(number),
					IsAfterCompaction: exchange.IsAfterCompaction,
				})
			case "tool":
				// A tool that neither finished nor failed has no verdict to record.
				status := part.part.status()
				if status != "completed" && status != "error" {
					continue
				}
				params, failure := part.part.toolState()
				tool := parsers.ToolUse{Name: part.part.Tool, ParamsSummary: params,
					HadError: status == "error", ErrorMessage: failure}
				exchange.Tools = append(exchange.Tools, tool)
			}
		}
		exchanges = append(exchanges, exchange)
		coverage.Converted++
	}
	return exchanges, deferred
}

func assistantContent(parts []openCodeRow) string {
	var content []string
	if text := durableTextOfParts(parts, "text"); text != "" {
		content = append(content, text)
	}
	for _, part := range parts {
		if part.part.Type != "patch" {
			continue
		}
		patch := "[patch"
		if part.part.Hash != "" {
			patch += " " + part.part.Hash
		}
		patch += "]"
		if len(part.part.Files) > 0 {
			patch += "\n" + strings.Join(part.part.Files, "\n")
		}
		content = append(content, patch)
	}
	if len(content) == 0 {
		// An assistant step that only ran tools still answered with those tools.
		// Without this fallback the exchange's agent text stays empty and the
		// turn is invisible to text search, even though its tool use is recorded
		// beside it. The name is what the source recorded, so nothing is invented.
		for _, part := range parts {
			if part.part.Type != "tool" || part.part.Tool == "" {
				continue
			}
			status := part.part.status()
			if status != "completed" && status != "error" {
				continue
			}
			content = append(content, "[tool "+part.part.Tool+"]")
		}
	}
	return strings.Join(content, "\n\n")
}

// openCodeProvenance adds up what one assistant message declared. OpenCode is
// the source that states everything: the model, who served it, the tokens with
// the reasoning ones apart, and the price of the call.
func openCodeProvenance(answers []openCodeRow) parsers.Provenance {
	var tally parsers.UsageTally
	model, provider := "", ""
	for _, answer := range answers {
		message := answer.message
		if model == "" {
			model = message.ModelID
		}
		if provider == "" {
			provider = message.ProviderID
		}
		addDurableMessageUsage(&tally, message.Cost, message.Tokens)
	}
	return tally.Provenance(model, provider)
}

// roundToInt reads a counter JSON decoded as a float. A token count is a whole
// number wherever it came from.
func roundToInt(value *float64) int {
	if value == nil {
		return 0
	}
	return int(*value)
}

func startedAfterACompaction(user openCodeRow, compactions []float64) bool {
	started := 0.0
	if user.created != nil {
		started = *user.created
	}
	return slices.ContainsFunc(compactions, func(at float64) bool { return at <= started })
}

func completionOf(nested *float64, fallback *float64) float64 {
	if nested != nil {
		return *nested
	}
	if fallback != nil {
		return *fallback
	}
	return 0
}

// openCodeFingerprint hashes a projection of the message and never its payload as
// stored: the hash lives in the session metadata, and metadata is not where a
// conversation's text belongs.
func openCodeFingerprint(message openCodeRow, parts []openCodeRow) string {
	projection := durablePartProjection(parts, func(part durableMessagePart) string {
		switch part.Type {
		case "text", "reasoning":
			return part.Text
		case "patch":
			return part.Hash + "\x00" + strings.Join(part.Files, "\x00")
		default:
			return ""
		}
	})
	return durableMessageFingerprint(message.id, message.message, projection)
}

func openCodeSourceAgent(_ string) string {
	return openCodeScope
}

// isoFromMS is the millisecond epoch OpenCode counts in, as ISO 8601 UTC.
func isoFromMS(value float64) string {
	if value == 0 {
		return ""
	}
	return parsers.ISOFromEpochMS(value)
}

var (
	openCodeTelegramSessionID = regexp.MustCompile(`\bid=(ses_[A-Za-z0-9_-]+)`)
	openCodeTelegramLineDate  = regexp.MustCompile(
		`\d{4}-\d{2}-\d{2}(?:[T ][0-9]{2}:[0-9]{2}:[0-9]{2}(?:\.[0-9]+)?(?:Z|[+-][0-9]{2}:?[0-9]{2})?)?`)
	openCodeTelegramFileDate = regexp.MustCompile(`\d{4}-\d{2}-\d{2}`)
)

// enrichOpenCodeTelegram marks only sessions that came from the same OpenCode
// snapshot. The bot logs are supporting evidence, not a second source of
// sessions, so an id that is absent from the store cannot create an empty row.
func enrichOpenCodeTelegram(records *parsers.Records, paths []string) []string {
	evidence, warnings := readOpenCodeTelegramEvidence(paths)
	for i := range records.Sessions {
		session := &records.Sessions[i]
		native, _ := session.Metadata["native_session_id"].(string)
		entries := evidence[native]
		if len(entries) == 0 {
			continue
		}
		if session.Metadata == nil {
			session.Metadata = map[string]any{}
		}
		session.Metadata["channel"] = "telegram"
		session.Metadata["channel_provenance"] = map[string]any{
			"opencode_telegram_bot": map[string]any{"evidence": entries},
		}
	}
	return warnings
}

func readOpenCodeTelegramEvidence(paths []string) (map[string]map[string]any, []string) {
	evidence := map[string]map[string]any{}
	var warnings []string
	for _, path := range paths {
		file, err := os.Open(path)
		if err != nil {
			if !os.IsNotExist(err) {
				warnings = append(warnings,
					fmt.Sprintf("OpenCode Telegram bot log %s could not be read: %v", path, err))
			}
			continue
		}
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 64*1024), 1024*1024)
		line := 0
		for scanner.Scan() {
			line++
			text := scanner.Text()
			date := openCodeTelegramDate(text, path)
			for occurrence, match := range openCodeTelegramSessionID.FindAllStringSubmatch(text, -1) {
				sessionID := match[1]
				if evidence[sessionID] == nil {
					evidence[sessionID] = map[string]any{}
				}
				sum := sha256.Sum256([]byte(fmt.Sprintf(
					"%s\x00%d\x00%d\x00%s", path, line, occurrence, sessionID)))
				evidence[sessionID][fmt.Sprintf("%x", sum[:])] = map[string]any{
					"log_file":  path,
					"line_date": date,
				}
			}
		}
		if err := scanner.Err(); err != nil {
			warnings = append(warnings,
				fmt.Sprintf("OpenCode Telegram bot log %s could not be read completely: %v", path, err))
		}
		if err := file.Close(); err != nil {
			warnings = append(warnings,
				fmt.Sprintf("OpenCode Telegram bot log %s could not be closed: %v", path, err))
		}
	}
	return evidence, warnings
}

func openCodeTelegramDate(line, path string) string {
	if date := openCodeTelegramLineDate.FindString(line); date != "" {
		return date
	}
	return openCodeTelegramFileDate.FindString(filepath.Base(path))
}
