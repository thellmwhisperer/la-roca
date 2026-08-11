package ingest

import (
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

	var records parsers.Records
	var complaints []string
	for _, id := range slices.Sorted(maps.Keys(malformed)) {
		complaints = append(complaints, fmt.Sprintf("OpenCode session %s: %s", id, malformed[id]))
	}

	for _, source := range sessions {
		native := source.text("id")
		if _, broken := malformed[native]; broken {
			continue
		}
		if !source.has("time_created") || !source.has("time_updated") {
			complaints = append(complaints,
				fmt.Sprintf("OpenCode session %s: it declares no timestamps", native))
			continue
		}
		session, deferred := openCodeSession(path, source, worktrees,
			messagesBySession[native], partsBySession[native])
		records.Sessions = append(records.Sessions, session)
		records.Deferred += deferred
	}
	return records, complaints, nil
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
			continue
		}
		out = append(out, item)
	}
	return out, malformed, nil
}

func openCodeSession(path string, source row, worktrees map[string]string,
	messages, parts []openCodeRow) (parsers.Session, int) {
	native := source.text("id")
	worktree := worktrees[source.text("project_id")]
	created, _ := source.number("time_created")
	updated, _ := source.number("time_updated")

	session := parsers.Session{
		ID:               openCodeScope + ":" + native,
		SourceAgent:      openCodeSourceAgent(source.text("agent")),
		Project:          firstNonEmpty(baseName(worktree), baseName(source.text("directory"))),
		StartedAt:        isoFromMS(created),
		EndedAt:          isoFromMS(updated),
		Snapshot:         true,
		ExchangeKeyScope: openCodeScope,
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
	})
	var sessionDeferred int
	session.Exchanges, sessionDeferred = openCodeExchanges(messages, parts)
	return session, sessionDeferred
}

// openCodeExchanges groups each human message with the assistant messages that
// answered it, and admits only the groups that are finished.
func openCodeExchanges(messages, parts []openCodeRow) ([]parsers.Exchange, int) {
	partsByMessage := map[string][]openCodeRow{}
	var compactions []float64
	for _, part := range parts {
		partsByMessage[part.messageID] = append(partsByMessage[part.messageID], part)
		if part.part.Type == "compaction" && part.created != nil {
			compactions = append(compactions, *part.created)
		}
	}
	assistantsByParent := map[string][]openCodeRow{}
	var users []openCodeRow
	for _, message := range messages {
		switch message.message.Role {
		case "user":
			users = append(users, message)
		case "assistant":
			parent := message.message.ParentID
			assistantsByParent[parent] = append(assistantsByParent[parent], message)
		}
	}

	var exchanges []parsers.Exchange
	deferred := 0
	number := 0
	for _, user := range users {
		answers := assistantsByParent[user.id]
		if len(answers) == 0 || !allCompleted(answers) {
			deferred++
			continue // the agent has not finished answering
		}
		var answerParts []openCodeRow
		for _, answer := range answers {
			answerParts = append(answerParts, partsByMessage[answer.id]...)
		}
		if anyLiveTool(answerParts) {
			deferred++
			continue
		}
		number++
		exchange := parsers.Exchange{
			Number:            number,
			SourceID:          user.id,
			Fingerprint:       openCodeFingerprint(user, answers, answerParts),
			IsAfterCompaction: startedAfterACompaction(user, compactions),
			HumanText:         textOfParts(partsByMessage[user.id], "text"),
			AgentText:         textOfParts(answerParts, "text"),
		}
		exchange.Provenance = openCodeProvenance(answers)
		human := completionOf(user.message.Time.Created, user.created)
		agent := lastCompletion(answers)
		exchange.HumanTimestamp = isoFromMS(human)
		exchange.AgentTimestamp = isoFromMS(agent)
		if elapsed := int(agent - human); human > 0 && agent > 0 && elapsed >= 0 {
			exchange.LatencyMS = &elapsed
		}
		for _, part := range answerParts {
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
				tool := parsers.ToolUse{Name: part.part.Tool, HadError: status == "error"}
				if tool.HadError {
					tool.ErrorMessage = "tool_error"
				}
				exchange.Tools = append(exchange.Tools, tool)
			}
		}
		exchanges = append(exchanges, exchange)
	}
	return exchanges, deferred
}

// openCodeProvenance adds up what the assistant messages of one turn declared.
// OpenCode is the source that states everything: the model, who served it, the
// tokens with the reasoning ones apart, and the price of the call.
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

func allCompleted(answers []openCodeRow) bool {
	return !slices.ContainsFunc(answers, func(answer openCodeRow) bool {
		return answer.message.Time.Completed == nil
	})
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

func lastCompletion(answers []openCodeRow) float64 {
	latest := 0.0
	for _, answer := range answers {
		latest = max(latest, completionOf(answer.message.Time.Completed, answer.updated))
	}
	return latest
}

// openCodeFingerprint hashes a projection of the turn and never its payload as
// stored: the hash lives in the session metadata, and metadata is not where a
// conversation's text belongs.
func openCodeFingerprint(user openCodeRow, answers, parts []openCodeRow) string {
	projection := struct {
		User       string     `json:"user"`
		Assistants []string   `json:"assistants"`
		Parts      [][]string `json:"parts"`
	}{User: user.id}
	for _, answer := range answers {
		projection.Assistants = append(projection.Assistants, answer.id)
	}
	for _, part := range parts {
		text := ""
		if part.part.Type == "text" || part.part.Type == "reasoning" {
			text = part.part.Text
		}
		projection.Parts = append(projection.Parts,
			[]string{part.id, part.part.Type, text, part.part.Tool, part.part.status()})
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
