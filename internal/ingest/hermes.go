package ingest

import (
	"context"
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

// ReadHermes projects the completed Hermes sessions onto normalized records.
//
// Only the sessions Hermes has closed are read. A live one is read on the next
// run, when it has an ending: half a session is a session whose last answer is
// missing, and nothing later can tell that from an answer that was never given.
func ReadHermes(ctx context.Context, path string) (parsers.Records, []string, error) {
	db, err := openForeignSource(ctx, "Hermes", path, hermesSchema)
	if err != nil {
		return parsers.Records{}, nil, err
	}
	defer db.Close()

	sessions, err := queryRows(ctx, db,
		`SELECT * FROM sessions WHERE ended_at IS NOT NULL ORDER BY started_at ASC`)
	if err != nil {
		return parsers.Records{}, nil, err
	}

	var records parsers.Records
	var complaints []string
	for _, source := range sessions {
		id := source.text("id")
		if id == "" {
			complaints = append(complaints, "Hermes: a session with no id was skipped")
			continue
		}
		messages, err := queryRows(ctx, db,
			`SELECT * FROM messages WHERE session_id = ? AND active = 1
			 ORDER BY timestamp ASC, id ASC`, id)
		if err != nil {
			// A schema without `active` is an older Hermes, and its messages are
			// all live ones.
			messages, err = queryRows(ctx, db,
				`SELECT * FROM messages WHERE session_id = ? ORDER BY timestamp ASC`, id)
			if err != nil {
				complaints = append(complaints, fmt.Sprintf("Hermes session %s: %v", id, err))
				continue
			}
		}
		records.Sessions = append(records.Sessions, hermesSession(source, messages))
	}
	return records, complaints, nil
}

func hermesSession(source row, messages []row) parsers.Session {
	model := source.text("model")
	if model == "" {
		model = "unknown"
	}
	started, _ := source.number("started_at")
	ended, _ := source.number("ended_at")

	session := parsers.Session{
		// The source agent carries the model, which is what makes "what did each
		// model answer" a question the corpus can answer.
		ID:          source.text("id"),
		SourceAgent: "hermes-" + model,
		Project:     ProjectFromCwd(source.text("cwd")),
		StartedAt:   parsers.ISOFromEpochSeconds(started),
		EndedAt:     parsers.ISOFromEpochSeconds(ended),
		Title:       source.text("title"),
		Snapshot:    true,
		Exchanges:   hermesExchanges(source.text("id"), messages),
	}
	if started > 0 && ended > 0 {
		minutes := int((ended - started) / 60)
		session.DurationMinutes = &minutes
	}
	session.Metadata = map[string]any{"hermes": hermesMetadata(source, messages)}
	return session
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
// would double every tool use in the corpus.
func hermesExchanges(sessionID string, messages []row) []parsers.Exchange {
	var exchanges []parsers.Exchange
	var current *parsers.Exchange
	number := 0
	// pendingParams carries the arguments from the assistant message that asked
	// for a tool to the result message that answers it.
	pendingParams := map[string]string{}

	closeCurrent := func() {
		if current == nil {
			return
		}
		finalizeHermes(current)
		exchanges = append(exchanges, *current)
		current = nil
	}

	for _, message := range messages {
		switch message.text("role") {
		case "user":
			closeCurrent()
			number++
			pendingParams = map[string]string{}
			at, _ := message.number("timestamp")
			current = &parsers.Exchange{
				Number:         number,
				HumanText:      message.text("content"),
				HumanTimestamp: parsers.ISOFromEpochSeconds(at),
			}
		case "assistant":
			if current == nil {
				continue
			}
			if reasoning := strings.TrimSpace(message.text("reasoning_content")); reasoning != "" {
				current.Thinking = append(current.Thinking, parsers.Thinking{
					Text:      reasoning,
					WordCount: len(strings.Fields(reasoning)),
					Position:  float64(number),
				})
			}
			if content := strings.TrimSpace(message.text("content")); content != "" {
				// The last answer with text in it is the answer.
				current.AgentText = content
				at, _ := message.number("timestamp")
				current.AgentTimestamp = parsers.ISOFromEpochSeconds(at)
			}
			maps.Copy(pendingParams, hermesToolParams(message.text("tool_calls")))
		case "tool":
			if current == nil {
				continue
			}
			name := message.text("tool_name")
			if name == "" {
				name = "unknown"
			}
			hadError, errorMessage := hermesToolVerdict(message.text("content"))
			current.Tools = append(current.Tools, parsers.ToolUse{
				Name:          name,
				ParamsSummary: pendingParams[name],
				HadError:      hadError,
				ErrorMessage:  errorMessage,
			})
		}
	}
	closeCurrent()

	parsers.PlaceThinking(exchanges)
	return exchanges
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

// hermesToolParams summarizes the arguments of each call an assistant message
// asked for.
func hermesToolParams(payload string) map[string]string {
	if strings.TrimSpace(payload) == "" {
		return nil
	}
	var calls []struct {
		Function struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		} `json:"function"`
	}
	if err := json.Unmarshal([]byte(payload), &calls); err != nil {
		return nil
	}
	summaries := map[string]string{}
	for _, call := range calls {
		name := call.Function.Name
		if name == "" {
			continue
		}
		summaries[name] = hermesArgumentSummary(call.Function.Arguments)
	}
	return summaries
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
