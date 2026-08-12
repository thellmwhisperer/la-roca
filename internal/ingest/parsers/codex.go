package parsers

import (
	"encoding/json"
	"strings"
)

// codexLine is one event of a Codex rollout. The rollout is an event log, not a
// message list: the turn's boundary is `task_complete`, not the next human line.
type codexLine struct {
	Type      string          `json:"type"`
	Timestamp string          `json:"timestamp"`
	Payload   json.RawMessage `json:"payload"`
}

type codexPayload struct {
	// session_meta
	ID            string `json:"id"`
	Cwd           string `json:"cwd"`
	Timestamp     string `json:"timestamp"`
	CLIVersion    string `json:"cli_version"`
	ModelProvider string `json:"model_provider"`

	// turn_context / event_msg
	TurnID           string          `json:"turn_id"`
	Model            string          `json:"model"`
	Effort           string          `json:"effort"`
	InnerType        string          `json:"type"`
	Message          string          `json:"message"`
	Text             string          `json:"text"`
	LastAgentMessage string          `json:"last_agent_message"`
	Summary          codexSummary    `json:"summary"`
	Info             *codexTokenInfo `json:"info"`
	CallID           string          `json:"call_id"`
	Name             string          `json:"name"`
	Arguments        json.RawMessage `json:"arguments"`
	Input            string          `json:"input"`
	Output           json.RawMessage `json:"output"`

	// response_item/message
	Role    string         `json:"role"`
	Content []codexContent `json:"content"`
}

type codexContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// codexSummary reads the `summary` key in every shape a rollout writes it: the
// reasoning blocks of a response item, the same blocks written as bare strings,
// or the single word a turn context uses to name its summary policy. Only block
// text is readable content, and an unreadable summary costs its own text and
// never the record it travelled in: decoding it strictly is what made a turn
// context unreadable and left newer sessions with no model at all.
type codexSummary struct {
	texts []string
}

func (s *codexSummary) UnmarshalJSON(data []byte) error {
	var blocks []struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(data, &blocks); err == nil {
		for _, block := range blocks {
			if block.Text != "" {
				s.texts = append(s.texts, block.Text)
			}
		}
		return nil
	}
	var texts []string
	if err := json.Unmarshal(data, &texts); err == nil {
		s.texts = append(s.texts, texts...)
	}
	return nil
}

func (s codexSummary) text() string { return strings.Join(s.texts, " ") }

// codexTokenInfo is what a `token_count` event measured. `last_token_usage` is
// the request that just finished, and summing those over a turn is the turn's
// own spend; `total_token_usage` is the running total of the same numbers.
type codexTokenInfo struct {
	Last *codexTokenUsage `json:"last_token_usage"`
}

type codexTokenUsage struct {
	Input     *int `json:"input_tokens"`
	Output    *int `json:"output_tokens"`
	Reasoning *int `json:"reasoning_output_tokens"`
}

func intOrZero(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}

// codexSignal is one thing the rollout produced at one record: a reasoning
// block, a tool call, or a measurement. They are collected flat, with the record
// they arrived at, so a turn claims the ones that fall inside its own span
// whichever way the turns themselves were recognized.
type codexSignal struct {
	record   int
	thinking string
	tool     *ToolUse
	usage    *codexTokenUsage
}

// codexTurn is one recognized turn: the span of records it owns and the text at
// both ends of it.
type codexTurn struct {
	opened, closed                         int
	humanText, humanTS, agentText, agentTS string
	model, effort                          string
}

// ParseCodexSession turns a Codex rollout into one session.
//
// A turn is closed by `task_complete`, and one aborted by `turn_aborted` is
// discarded whole: half a turn in the corpus is worse than no turn, because it
// reads as an answer the agent never gave.
//
// When that event stream recognizes no turn at all the response items are read
// instead: a rollout whose process died before writing a `task_complete` still
// holds the question and the answer it got, and refusing to read them is what
// left whole sessions ingested empty.
func ParseCodexSession(content []byte, meta FileMeta) (Records, error) {
	reader := &codexReader{
		session: Session{
			ID:          meta.SessionID,
			SourceAgent: firstNonEmpty(meta.SourceAgent, "codex"),
			Project:     meta.Project,
			Metadata:    map[string]any{},
		},
		pending: map[string]*ToolUse{},
	}
	reader.read(content)

	turns := reader.turns
	if len(turns) == 0 {
		turns = reader.recovered
	}
	exchanges := reader.exchanges(turns)
	PlaceThinking(exchanges)

	session := reader.session
	for _, exchange := range exchanges {
		if ts := exchange.AgentTimestamp; ts != "" && ts > session.EndedAt {
			session.EndedAt = ts
		}
	}
	session.Exchanges = exchanges
	session.DurationMinutes = minutesBetween(session.StartedAt, session.EndedAt)
	return Records{
		Sessions: []Session{session},
		Discards: reader.discards,
		Deferred: reader.deferred,
	}, nil
}

// codexReader walks the rollout once and keeps both readings of it: the turns
// the event stream declares, and the ones the response items would give if it
// declares none.
type codexReader struct {
	session  Session
	provider string
	model    string
	effort   string

	signals  []codexSignal
	turns    []codexTurn
	discards []Discard
	deferred int

	pending map[string]*ToolUse

	// open is the turn the event stream has started and not closed.
	open      *codexTurn
	agentSaid string

	// recovering is the same for the response-item reading, which is only used
	// when the event stream recognized nothing.
	recovering *codexTurn
	recovered  []codexTurn
}

func (r *codexReader) read(content []byte) {
	for index, raw := range lines(content) {
		record := index + 1
		var line codexLine
		if err := json.Unmarshal([]byte(raw), &line); err != nil {
			r.discards = append(r.discards, Discard{Record: record,
				Reason: "invalid JSON: " + err.Error(), Category: "invalid JSON"})
			continue
		}
		var payload codexPayload
		if len(line.Payload) > 0 {
			if err := json.Unmarshal(line.Payload, &payload); err != nil {
				r.discards = append(r.discards, Discard{Record: record,
					Reason: "invalid payload: " + err.Error(), Category: "invalid payload"})
				continue
			}
		}

		switch line.Type {
		case "session_meta":
			r.sessionMeta(payload)
		case "turn_context":
			r.model = firstNonEmpty(payload.Model, r.model)
			r.effort = firstNonEmpty(payload.Effort, r.effort)
			putIfSet(r.session.Metadata, "model", r.model)
			putIfSet(r.session.Metadata, "reasoning_effort", r.effort)
		case "event_msg":
			r.event(record, line, payload)
		case "response_item":
			r.responseItem(record, line, payload)
		default:
			r.exclude(record, "record type", line.Type)
		}
	}
	if r.open != nil {
		// A rollout whose last event is a question is one Codex is still
		// answering. That turn is deferred and not discarded: the next run reads a
		// longer file and lands it.
		r.deferred++
	}
	if r.recovering != nil && len(r.turns) == 0 {
		r.deferred++
	}
}

func (r *codexReader) sessionMeta(payload codexPayload) {
	if payload.ID != "" {
		r.session.ID = payload.ID
	}
	r.session.StartedAt = validInstant(payload.Timestamp)
	r.provider = payload.ModelProvider
	putIfSet(r.session.Metadata, "cwd", payload.Cwd)
	putIfSet(r.session.Metadata, "cli_version", payload.CLIVersion)
	putIfSet(r.session.Metadata, "model_provider", payload.ModelProvider)
}

func (r *codexReader) event(record int, line codexLine, payload codexPayload) {
	switch payload.InnerType {
	case "user_message":
		// A question arriving over one that never completed replaces it, and the
		// replaced turn will never complete: it is counted here, because nothing
		// downstream can see the turn that was overwritten.
		if r.open != nil {
			r.discards = append(r.discards, Discard{Record: r.open.opened,
				Reason: "turn superseded by a later question before it completed"})
		}
		r.open = &codexTurn{
			opened: record, humanText: payload.Message, humanTS: validInstant(line.Timestamp),
			model: r.model, effort: r.effort,
		}
		r.resetTurnScope()
	case "agent_message":
		// The agent's own words. They do not close the turn, because a turn holds
		// several of them and closing on the first would split one answer into
		// many exchanges; they are what the turn says when `task_complete` hands
		// over no text of its own.
		if strings.TrimSpace(payload.Message) != "" {
			r.agentSaid = payload.Message
		}
	case "agent_reasoning":
		r.signals = append(r.signals, codexSignal{record: record, thinking: payload.Text})
	case "token_count":
		if payload.Info != nil && payload.Info.Last != nil {
			r.signals = append(r.signals, codexSignal{record: record, usage: payload.Info.Last})
		}
	case "task_complete":
		if r.open == nil {
			r.discards = append(r.discards, Discard{Record: record, Reason: "completed turn has no user message"})
			return
		}
		turn := *r.open
		turn.closed = record
		turn.agentText = firstNonEmpty(payload.LastAgentMessage, r.agentSaid)
		turn.agentTS = validInstant(line.Timestamp)
		r.turns = append(r.turns, turn)
		r.open = nil
		r.resetTurnScope()
	case "turn_aborted":
		if r.open != nil {
			r.discards = append(r.discards, Discard{Record: record, Reason: "aborted turn"})
		}
		r.open = nil
		r.resetTurnScope()
	default:
		r.exclude(record, "event", payload.InnerType)
	}
}

// resetTurnScope drops what only made sense inside the turn that just ended: the
// agent's last words and the tool calls still waiting for a verdict. A verdict
// arriving after the turn closed answers a call nothing stores any more, and it
// is counted as the orphan it is instead of patching a tool use of another turn.
func (r *codexReader) resetTurnScope() {
	r.agentSaid = ""
	r.pending = map[string]*ToolUse{}
}

func (r *codexReader) responseItem(record int, line codexLine, payload codexPayload) {
	switch payload.InnerType {
	case "reasoning":
		// A summary with no text is an empty row nobody can query, so it is left
		// out by name instead of stored as noise. It is an exclusion and not a
		// failure: the rollout keeps the reasoning of those turns encrypted and
		// writes no summary of it, so there was never anything to read.
		text := payload.Summary.text()
		if strings.TrimSpace(text) == "" {
			r.excludeRecord(record, "codex reasoning kept no readable summary")
			return
		}
		r.signals = append(r.signals, codexSignal{record: record, thinking: text})
	case "function_call", "custom_tool_call":
		tool := &ToolUse{
			Name:          payload.Name,
			ParamsSummary: Clip(firstNonEmpty(rawText(payload.Arguments), payload.Input), paramsBudget),
		}
		r.signals = append(r.signals, codexSignal{record: record, tool: tool})
		if payload.CallID != "" {
			r.pending[payload.CallID] = tool
		}
	case "function_call_output", "custom_tool_call_output":
		output := rawText(payload.Output)
		tool, ok := r.pending[payload.CallID]
		if !ok {
			r.discards = append(r.discards, Discard{Record: record,
				Reason:   "tool verdict has unknown call_id: " + payload.CallID,
				Category: "tool verdict has unknown call_id"})
			return
		}
		if isToolError(output) {
			tool.HadError = true
			tool.ErrorMessage = Clip(output, errorBudget)
		}
	case "message":
		r.recover(record, line, payload)
	default:
		r.exclude(record, "response item", payload.InnerType)
	}
}

// recover reads the conversation off the response items. It builds the same turn
// shape the event stream does and is only consulted when that stream recognized
// none: the two readings describe the same conversation, and using both would
// file every turn twice.
//
// A response item this reading does not use is neither discarded nor excluded:
// where the event stream won, the same words were ingested from the other
// reading, and counting them again as left out would report a conversation that
// did land as one that did not.
func (r *codexReader) recover(record int, line codexLine, payload codexPayload) {
	text := codexContentText(payload.Content)
	if strings.TrimSpace(text) == "" {
		return
	}
	switch payload.Role {
	case "user":
		r.recovering = &codexTurn{
			opened: record, humanText: text, humanTS: validInstant(line.Timestamp),
			model: r.model, effort: r.effort,
		}
	case "assistant":
		if r.recovering == nil {
			return
		}
		turn := *r.recovering
		turn.closed = record
		turn.agentText = text
		turn.agentTS = validInstant(line.Timestamp)
		r.recovered = append(r.recovered, turn)
		r.recovering = nil
	}
}

// exclude names a record this build does not read. A rollout is the runtime's
// own log and the conversation is a deliberate subset of it, so the records left
// out are reported as the exclusions they are and never as failures.
func (r *codexReader) exclude(record int, kind, name string) {
	r.discards = append(r.discards, Discard{
		Record: record, ByDesign: true,
		Reason:   "codex runtime " + kind + " not ingested: " + firstNonEmpty(name, "unnamed"),
		Category: "codex runtime " + kind + " not ingested",
	})
}

func (r *codexReader) excludeRecord(record int, reason string) {
	r.discards = append(r.discards, Discard{Record: record, Reason: reason, ByDesign: true})
}

// exchanges gives every turn the signals that fall inside its own span.
func (r *codexReader) exchanges(turns []codexTurn) []Exchange {
	exchanges := make([]Exchange, 0, len(turns))
	for _, turn := range turns {
		exchange := Exchange{
			Number:         len(exchanges) + 1,
			HumanText:      turn.humanText,
			AgentText:      turn.agentText,
			HumanTimestamp: turn.humanTS,
			AgentTimestamp: turn.agentTS,
		}
		var usage UsageTally
		seen := map[string]bool{}
		for _, signal := range r.signals {
			if signal.record <= turn.opened || signal.record > turn.closed {
				continue
			}
			switch {
			case signal.usage != nil:
				if signal.usage.Input != nil {
					usage.AddInputTokens(*signal.usage.Input)
				}
				if signal.usage.Output != nil {
					usage.AddOutputTokens(*signal.usage.Output)
				}
				if signal.usage.Reasoning != nil {
					usage.AddReasoningTokens(*signal.usage.Reasoning)
				}
			case signal.tool != nil:
				exchange.Tools = append(exchange.Tools, *signal.tool)
			default:
				// The streamed reasoning event and the response item that persists
				// it carry the same words. Both are read, and the second copy of one
				// text is not stored twice.
				text := strings.TrimSpace(signal.thinking)
				if text == "" || seen[text] {
					continue
				}
				seen[text] = true
				exchange.Thinking = append(exchange.Thinking, Thinking{
					Text: signal.thinking, WordCount: wordCount(signal.thinking),
				})
			}
		}
		exchange.Provenance = usage.Provenance(turn.model, r.provider)
		exchanges = append(exchanges, exchange)
	}
	return exchanges
}

func codexContentText(blocks []codexContent) string {
	parts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		if block.Text != "" {
			parts = append(parts, block.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// rawText reads a value Codex writes either as a JSON string or as the document
// itself.
func rawText(value json.RawMessage) string {
	if len(value) == 0 {
		return ""
	}
	var text string
	if err := json.Unmarshal(value, &text); err == nil {
		return text
	}
	return string(value)
}

// isToolError recognizes only a non-zero exit code inside output metadata.
// Guessing an error out of the text would file
// every command that printed the word "error" as a failure.
func isToolError(output string) bool {
	var document struct {
		Metadata struct {
			ExitCode *float64 `json:"exit_code"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal([]byte(output), &document); err != nil {
		return false
	}
	return document.Metadata.ExitCode != nil && *document.Metadata.ExitCode != 0
}

func putIfSet(payload map[string]any, key, value string) {
	if value != "" {
		payload[key] = value
	}
}
