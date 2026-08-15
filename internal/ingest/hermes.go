package ingest

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/thellmwhisperer/la-roca/internal/ingest/parsers"
)

// Hermes keeps its sessions in its own SQLite too, but flat: a message list per
// session, with the tool results as messages of their own. What this reader has to
// get right is where a turn ends, and that is the next human message.

// hermesMetadataBudget clips a parameter summary. It is shorter than the
// transcript sources' because Hermes writes its arguments already summarized.
const hermesMetadataBudget = 200

// hermesSchema is the shape this build reads.
var hermesSchema = []foreignTable{
	{"sessions", []string{"id", "started_at", "ended_at"}},
	{"messages", []string{"session_id", "role", "content", "timestamp"}},
}

// ReadHermes projects the Hermes sessions onto normalized records.
//
// Every session is read, closed or not. Hermes only writes `ended_at` when a
// session winds down cleanly, so a session that was killed, abandoned, or run
// through a channel that never closes it (acp, most TUI and CLI runs) has its
// messages and no ending; skipping those was what reduced six hundred sessions
// to ninety. The span of such a session is its last recorded message, and a
// human turn with no recorded answer is still in flight, so it is deferred and
// re-read on the next run rather than stored as an answer that was never given.
func ReadHermes(ctx context.Context, path string) (parsers.Records, []string, error) {
	db, err := openForeignSource(ctx, "Hermes", path, hermesSchema)
	if err != nil {
		return parsers.Records{}, nil, err
	}
	defer db.Close()

	sessions, err := queryRows(ctx, db, `SELECT * FROM sessions ORDER BY started_at ASC`)
	if err != nil {
		return parsers.Records{}, nil, err
	}
	messageColumns, err := tableColumns(ctx, db, "messages")
	if err != nil {
		return parsers.Records{}, nil, err
	}

	var records parsers.Records
	records.Seen.Sessions = len(sessions)
	var complaints []string
	for _, source := range sessions {
		id := source.text("id")
		if id == "" {
			complaints = append(complaints, "Hermes: a session with no id was skipped")
			continue
		}
		messages, err := hermesMessages(ctx, db, messageColumns, id)
		if err != nil {
			complaints = append(complaints, fmt.Sprintf("Hermes session %s: %v", id, err))
			continue
		}
		records.Seen.Messages += len(messages)
		session, orphaned, deferred := hermesSession(source, messages)
		records.Sessions = append(records.Sessions, session)
		records.Deferred += deferred
		for range orphaned {
			records.Discards = append(records.Discards, parsers.Discard{
				Reason:   fmt.Sprintf("Hermes session %s: assistant content has no open human turn", id),
				Category: "Hermes assistant content has no open human turn",
			})
		}
	}
	return records, complaints, nil
}

func hermesMessages(ctx context.Context, db *sql.DB, columns map[string]bool, sessionID string) ([]row, error) {
	filter := ""
	if columns["active"] {
		filter = " AND active = 1"
	}
	order := "rowid"
	if columns["id"] {
		order = "id"
	}
	return queryRows(ctx, db, `SELECT * FROM messages WHERE session_id = ?`+filter+
		` ORDER BY timestamp ASC, `+order+` ASC`, sessionID)
}

// hermesLastMessageTime is the newest message timestamp a session recorded, or
// zero when it has no messages. It is the end of a session Hermes never closed.
func hermesLastMessageTime(messages []row) float64 {
	var latest float64
	for _, message := range messages {
		at, _ := message.number("timestamp")
		if at > latest {
			latest = at
		}
	}
	return latest
}

func hermesSession(source row, messages []row) (parsers.Session, int, int) {
	model := source.text("model")
	if model == "" {
		model = "unknown"
	}
	started, hasStarted := source.number("started_at")
	ended, hasEnded := source.number("ended_at")
	startedAt, endedAt := "", ""
	if hasStarted {
		startedAt = parsers.ISOFromEpochSeconds(started)
	}
	if hasEnded {
		endedAt = parsers.ISOFromEpochSeconds(ended)
	} else if last := hermesLastMessageTime(messages); last > 0 {
		// Hermes records the end of a session only when it winds down cleanly. A
		// session with no recorded ending still has one: its last message.
		ended = last
		endedAt = parsers.ISOFromEpochSeconds(last)
	}

	session := parsers.Session{
		ID:          source.text("id"),
		SourceAgent: "hermes",
		Project:     ProjectFromCwd(source.text("cwd")),
		StartedAt:   startedAt,
		EndedAt:     endedAt,
		Title:       source.text("title"),
		Snapshot:    true,
	}
	orphaned := 0
	deferred := 0
	session.Exchanges, orphaned, deferred = hermesExchanges(source.text("id"), messages)
	// Hermes prices and counts a whole session and never a turn, so every turn of
	// it carries the model and the provider that answered and no invented split
	// of the totals. Those totals travel beside the session in the same
	// vocabulary the exchange columns use, so one question reads both.
	provenance := parsers.Provenance{
		// The session's own model column and not the "unknown" placeholder its
		// metadata falls back to: a column filled with a placeholder is worse than
		// an empty one, because a query cannot tell it from a real name.
		Model:    source.text("model"),
		Provider: source.text("billing_provider"),
	}
	for i := range session.Exchanges {
		session.Exchanges[i].Provenance = provenance
	}
	if hasStarted && started > 0 && ended >= started {
		minutes := int((ended - started) / 60)
		session.DurationMinutes = &minutes
	}
	session.Metadata = map[string]any{
		"model":  model,
		"hermes": hermesMetadata(source, messages),
		"usage":  hermesUsage(source),
	}
	return session, orphaned, deferred
}

// hermesUsage restates the session totals under the names the per-exchange
// provenance columns use. Hermes is the one source that already measured what a
// conversation spent, and saying it in a private vocabulary is what kept it
// unqueryable beside every other source.
func hermesUsage(source row) map[string]any {
	usage := map[string]any{}
	for key, column := range map[string]string{
		"tokens_in":        "input_tokens",
		"tokens_out":       "output_tokens",
		"tokens_reasoning": "reasoning_tokens",
		"cost_usd":         "actual_cost_usd",
	} {
		if source.has(column) {
			usage[key] = source[column]
		}
	}
	if _, priced := usage["cost_usd"]; !priced && source.has("estimated_cost_usd") {
		usage["cost_usd"] = source["estimated_cost_usd"]
	}
	return usage
}

// hermesMetadata keeps what Hermes measured about the session, which is the one
// thing no other source has: what it cost.
func hermesMetadata(source row, messages []row) map[string]any {
	payload := map[string]any{}
	for _, key := range []string{
		"source", "model", "message_count", "tool_call_count",
		"input_tokens", "output_tokens", "cache_read_tokens", "cache_write_tokens",
		"reasoning_tokens", "estimated_cost_usd", "actual_cost_usd", "cost_status",
		"billing_provider", "billing_mode", "end_reason", "handoff_state",
		"handoff_platform", "rewind_count",
	} {
		if source.has(key) {
			payload[key] = source[key]
		}
	}
	reasons := map[string]int{}
	for _, message := range messages {
		if message.text("role") != "assistant" {
			continue
		}
		if reason := message.text("finish_reason"); reason != "" {
			reasons[reason]++
		}
	}
	if len(reasons) > 0 {
		payload["finish_reasons"] = reasons
	}
	return payload
}

// hermesExchanges groups everything between two human messages into one exchange.
//
// A tool call embedded in an assistant message is not counted: only the result
// message is, because that is the one that knows whether it worked. Counting both
// would double every tool use in the corpus. A human turn with no recorded
// answer at all is still in flight, so it is deferred instead of being stored
// with an answer this build invented.
func hermesExchanges(sessionID string, messages []row) ([]parsers.Exchange, int, int) {
	var exchanges []parsers.Exchange
	var current *parsers.Exchange
	number := 0
	orphaned := 0
	deferred := 0
	// pendingByName and pendingByCall carry the arguments from the assistant
	// messages that asked for a tool to the result messages that answer them.
	// Hermes writes the call id on both sides, so the id is the key; the name is
	// the fallback for the older payload that had no id.
	pendingByName := map[string]string{}
	pendingByCall := map[string]hermesToolCall{}
	// hasResponse records whether the open exchange has seen any assistant
	// content: text, reasoning or a tool call. A human turn with none is still
	// being answered and is deferred.
	hasResponse := false

	closeCurrent := func() {
		if current == nil {
			return
		}
		if !hasResponse {
			deferred++
			current = nil
			pendingByName = map[string]string{}
			pendingByCall = map[string]hermesToolCall{}
			return
		}
		finalizeHermes(current)
		exchanges = append(exchanges, *current)
		current = nil
		hasResponse = false
	}

	for _, message := range messages {
		switch message.text("role") {
		case "user":
			closeCurrent()
			number++
			pendingByName = map[string]string{}
			pendingByCall = map[string]hermesToolCall{}
			hasResponse = false
			at, _ := message.number("timestamp")
			current = &parsers.Exchange{
				Number:         number,
				HumanText:      message.text("content"),
				HumanTimestamp: parsers.ISOFromEpochSeconds(at),
			}
		case "assistant":
			if current == nil {
				if strings.TrimSpace(message.text("content")) != "" ||
					strings.TrimSpace(message.text("reasoning_content")) != "" ||
					strings.TrimSpace(message.text("tool_calls")) != "" {
					orphaned++
				}
				continue
			}
			reasoning := strings.TrimSpace(message.text("reasoning_content"))
			content := strings.TrimSpace(message.text("content"))
			rawCalls := strings.TrimSpace(message.text("tool_calls"))
			calls := hermesToolCalls(message.text("tool_calls"))
			if reasoning == "" && content == "" && rawCalls == "" {
				continue
			}
			hasResponse = true
			if reasoning != "" {
				current.Thinking = append(current.Thinking, parsers.Thinking{
					Text:      reasoning,
					WordCount: len(strings.Fields(reasoning)),
					Position:  float64(number),
				})
			}
			if content != "" {
				// The last answer with text in it is the answer.
				current.AgentText = content
				at, _ := message.number("timestamp")
				current.AgentTimestamp = parsers.ISOFromEpochSeconds(at)
			}
			for _, call := range calls {
				pendingByName[call.name] = call.summary
				if call.id != "" {
					pendingByCall[call.id] = call
				}
			}
		case "tool":
			if current == nil {
				continue
			}
			hasResponse = true
			name := message.text("tool_name")
			summary := pendingByName[name]
			if callID := message.text("tool_call_id"); callID != "" {
				if call, ok := pendingByCall[callID]; ok {
					name = call.name
					summary = call.summary
				}
			}
			if name == "" {
				name = "unknown"
			}
			hadError, errorMessage := hermesToolVerdict(message.text("content"))
			current.Tools = append(current.Tools, parsers.ToolUse{
				Name:          name,
				ParamsSummary: summary,
				HadError:      hadError,
				ErrorMessage:  errorMessage,
			})
		}
	}
	closeCurrent()

	parsers.PlaceThinking(exchanges)
	return exchanges, orphaned, deferred
}

// finalizeHermes gives an exchange with no prose an honest label. A turn that only
// thought or only ran a tool did happen, and leaving its answer empty would make
// it look like the agent said nothing.
func finalizeHermes(exchange *parsers.Exchange) {
	if exchange.AgentText != "" {
		return
	}
	if len(exchange.Thinking) > 0 {
		exchange.AgentText = "[thinking only]"
		return
	}
	exchange.AgentText = "[tool use]"
}

// hermesToolCall is one call an assistant message asked for, decoded from its
// tool_calls payload, with the summary the result message will carry.
type hermesToolCall struct {
	id      string
	name    string
	summary string
}

// hermesToolCalls parses the tool_calls payload of one assistant message into
// the calls it asked for. Hermes writes the OpenAI Responses shape, with the
// call id beside the function; a payload that does not parse contributes nothing.
func hermesToolCalls(payload string) []hermesToolCall {
	if strings.TrimSpace(payload) == "" {
		return nil
	}
	var calls []struct {
		ID       string `json:"id"`
		CallID   string `json:"call_id"`
		Function struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		} `json:"function"`
	}
	if err := json.Unmarshal([]byte(payload), &calls); err != nil {
		return nil
	}
	out := make([]hermesToolCall, 0, len(calls))
	for _, call := range calls {
		name := call.Function.Name
		if name == "" {
			continue
		}
		id := call.ID
		if id == "" {
			id = call.CallID
		}
		out = append(out, hermesToolCall{
			id:      id,
			name:    name,
			summary: hermesArgumentSummary(call.Function.Arguments),
		})
	}
	return out
}

// hermesArgumentSummary reads the arguments, which Hermes writes as a JSON string
// holding a JSON document, and keeps the first three keys of it.
func hermesArgumentSummary(arguments json.RawMessage) string {
	text := strings.TrimSpace(string(arguments))
	if text == "" || text == "null" {
		return ""
	}
	var nested string
	if err := json.Unmarshal(arguments, &nested); err == nil {
		text = nested
	}
	var values map[string]any
	if err := json.Unmarshal([]byte(text), &values); err != nil {
		return parsers.Clip(text, hermesMetadataBudget)
	}
	names := slices.Sorted(maps.Keys(values))
	if len(names) > 3 {
		names = names[:3]
	}
	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, fmt.Sprintf("%s=%v", name, values[name]))
	}
	return parsers.Clip(strings.Join(parts, ", "), hermesMetadataBudget)
}

// hermesToolVerdict reads whether a tool result went wrong. Three ways to say so,
// all three the tool's own words: an `error` key, Hermes's `isError` flag, or a
// non-zero exit code.
func hermesToolVerdict(content string) (bool, string) {
	if strings.TrimSpace(content) == "" {
		return false, ""
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(content), &result); err != nil {
		return false, ""
	}
	if failure, ok := result["error"]; ok && truthy(failure) {
		return true, parsers.Clip(fmt.Sprint(failure), 500)
	}
	if failure, ok := result["isError"]; ok && truthy(failure) {
		return true, parsers.Clip(content, 500)
	}
	if code, ok := result["exit_code"]; ok {
		if number, isNumber := code.(float64); isNumber && number != 0 {
			return true, parsers.Clip(content, 500)
		}
	}
	return false, ""
}

func truthy(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case bool:
		return typed
	case string:
		return typed != ""
	case float64:
		return typed != 0
	}
	return true
}
