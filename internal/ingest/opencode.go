package ingest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/thellmwhisperer/la-roca/internal/ingest/parsers"
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

const (
	openCodeParamsBudget = 500
	openCodeErrorBudget  = 1000
)

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
	ModelID    string          `json:"modelID"`
	ProviderID string          `json:"providerID"`
	Cost       *float64        `json:"cost"`
	Tokens     *openCodeTokens `json:"tokens"`
}

// openCodeTokens is what OpenCode counted for one assistant message. The cache
// tiers are prompt tokens like the rest, and they are added to it.
type openCodeTokens struct {
	Input     *float64 `json:"input"`
	Output    *float64 `json:"output"`
	Reasoning *float64 `json:"reasoning"`
	Cache     *struct {
		Read  *float64 `json:"read"`
		Write *float64 `json:"write"`
	} `json:"cache"`
}

// openCodePart is the `data` document of a part row.
type openCodePart struct {
	Type  string          `json:"type"`
	Text  string          `json:"text"`
	Tool  string          `json:"tool"`
	Hash  string          `json:"hash"`
	Files []string        `json:"files"`
	State json.RawMessage `json:"state"`
}

// status is the tool's state, which OpenCode writes either as an object with a
// status or as the bare status.
func (p openCodePart) status() string {
	if len(p.State) == 0 {
		return ""
	}
	var object struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(p.State, &object); err == nil && object.Status != "" {
		return object.Status
	}
	var text string
	if err := json.Unmarshal(p.State, &text); err == nil {
		return text
	}
	return ""
}

func (p openCodePart) toolState() (input, failure string) {
	var state struct {
		Input json.RawMessage `json:"input"`
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(p.State, &state) != nil {
		return "", ""
	}
	if len(state.Input) > 0 && string(state.Input) != "null" {
		var compact bytes.Buffer
		if json.Compact(&compact, state.Input) == nil {
			input = compact.String()
		}
	}
	if len(state.Error) > 0 && string(state.Error) != "null" {
		if json.Unmarshal(state.Error, &failure) != nil {
			var compact bytes.Buffer
			if json.Compact(&compact, state.Error) == nil {
				failure = compact.String()
			}
		}
	}
	return parsers.Clip(input, openCodeParamsBudget), parsers.Clip(failure, openCodeErrorBudget)
}

type openCodeRow struct {
	id        string
	sessionID string
	messageID string
	created   *float64
	updated   *float64
	message   openCodeMessage
	part      openCodePart
}

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
	messages, malformedMessages, err := openCodeDocuments(ctx, db,
		`SELECT id, session_id, time_created, time_updated, data FROM message
		 ORDER BY time_created, id`, true)
	if err != nil {
		return parsers.Records{}, nil, err
	}
	parts, malformedParts, err := openCodeDocuments(ctx, db,
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
	malformed := malformedParts
	maps.Copy(malformed, malformedMessages)

	messagesBySession := map[string][]openCodeRow{}
	for _, message := range messages {
		messagesBySession[message.sessionID] = append(messagesBySession[message.sessionID], message)
	}
	partsBySession := map[string][]openCodeRow{}
	for _, part := range parts {
		partsBySession[part.sessionID] = append(partsBySession[part.sessionID], part)
	}

	records := parsers.Records{MessageCoverage: &parsers.MessageCoverage{
		Seen: len(messages), Skipped: map[string]int{},
	}}
	complaints := duplicateComplaints
	for _, id := range slices.Sorted(maps.Keys(malformed)) {
		complaints = append(complaints, fmt.Sprintf("OpenCode session %s: %s", id, malformed[id]))
	}

	seenSessions := map[string]bool{}
	for _, source := range sessions {
		native := source.text("id")
		seenSessions[native] = true
		if _, broken := malformed[native]; broken {
			records.MessageCoverage.Skipped["session contains malformed JSON"] +=
				len(messagesBySession[native])
			continue
		}
		if !source.has("time_created") || !source.has("time_updated") {
			complaints = append(complaints,
				fmt.Sprintf("OpenCode session %s: it declares no timestamps", native))
			records.MessageCoverage.Skipped["session declares no timestamps"] +=
				len(messagesBySession[native])
			continue
		}
		session, deferred := openCodeSession(path, source, worktrees,
			messagesBySession[native], partsBySession[native], todosBySession[native],
			records.MessageCoverage)
		records.Sessions = append(records.Sessions, session)
		records.Deferred += deferred
	}
	for sessionID, unseen := range messagesBySession {
		if !seenSessions[sessionID] {
			records.MessageCoverage.Skipped["message references a missing session"] += len(unseen)
		}
	}
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

// openCodeDocuments reads one table and decodes its JSON payload. A row whose
// document does not parse marks its whole session.
func openCodeDocuments(ctx context.Context, db *sql.DB, statement string, isMessage bool) (
	[]openCodeRow, map[string]string, error) {
	rows, err := queryRows(ctx, db, statement)
	if err != nil {
		return nil, nil, err
	}
	malformed := map[string]string{}
	out := make([]openCodeRow, 0, len(rows))
	for _, record := range rows {
		item := openCodeRow{
			id:        record.text("id"),
			sessionID: record.text("session_id"),
			messageID: record.text("message_id"),
		}
		if value, ok := record.number("time_created"); ok {
			item.created = &value
		}
		if value, ok := record.number("time_updated"); ok {
			item.updated = &value
		}
		document := any(&item.part)
		if isMessage {
			document = &item.message
		}
		if err := json.Unmarshal([]byte(record.text("data")), document); err != nil {
			malformed[item.sessionID] = "malformed_json"
			out = append(out, item)
			continue
		}
		out = append(out, item)
	}
	return out, malformed, nil
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
		if role == "assistant" && (message.message.Time.Completed == nil || anyLiveTool(messageParts)) {
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
		if role == "user" {
			exchange.HumanText = textOfParts(messageParts, "text")
			exchange.HumanTimestamp = isoFromMS(completionOf(message.message.Time.Created, message.created))
			exchange.RewriteOnIdentityChange = true
		} else {
			exchange.AgentText = assistantContent(messageParts)
			exchange.AgentTimestamp = isoFromMS(completionOf(message.message.Time.Completed, message.updated))
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
	if text := textOfParts(parts, "text"); text != "" {
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
		if message.Cost != nil {
			tally.AddCost(*message.Cost)
		}
		tokens := message.Tokens
		if tokens == nil {
			continue
		}
		if tokens.Input != nil || tokens.Cache != nil &&
			(tokens.Cache.Read != nil || tokens.Cache.Write != nil) {
			prompt := roundToInt(tokens.Input)
			if tokens.Cache != nil {
				prompt += roundToInt(tokens.Cache.Read) + roundToInt(tokens.Cache.Write)
			}
			tally.AddInputTokens(prompt)
		}
		if tokens.Output != nil {
			tally.AddOutputTokens(roundToInt(tokens.Output))
		}
		if tokens.Reasoning != nil {
			tally.AddReasoningTokens(roundToInt(tokens.Reasoning))
		}
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

func anyLiveTool(parts []openCodeRow) bool {
	return slices.ContainsFunc(parts, func(part openCodeRow) bool {
		status := part.part.status()
		return part.part.Type == "tool" && (status == "pending" || status == "running")
	})
}

func startedAfterACompaction(user openCodeRow, compactions []float64) bool {
	started := 0.0
	if user.created != nil {
		started = *user.created
	}
	return slices.ContainsFunc(compactions, func(at float64) bool { return at <= started })
}

func textOfParts(parts []openCodeRow, kind string) string {
	var texts []string
	for _, part := range parts {
		if part.part.Type == kind && part.part.Text != "" {
			texts = append(texts, part.part.Text)
		}
	}
	return strings.Join(texts, "\n")
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
	projection := struct {
		ID      string          `json:"id"`
		Message openCodeMessage `json:"message"`
		Parts   [][]string      `json:"parts"`
	}{ID: message.id, Message: message.message}
	for _, part := range parts {
		text := ""
		if part.part.Type == "text" || part.part.Type == "reasoning" || part.part.Type == "patch" {
			text = part.part.Text
			if part.part.Type == "patch" {
				text = part.part.Hash + "\x00" + strings.Join(part.part.Files, "\x00")
			}
		}
		params, failure := part.part.toolState()
		projection.Parts = append(projection.Parts,
			[]string{part.id, part.part.Type, text, part.part.Tool, part.part.status(), params, failure})
	}
	encoded, err := json.Marshal(projection)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
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
