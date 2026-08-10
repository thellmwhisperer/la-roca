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
	InnerType        string          `json:"type"`
	Message          string          `json:"message"`
	LastAgentMessage string          `json:"last_agent_message"`
	Summary          []codexSummary  `json:"summary"`
	CallID           string          `json:"call_id"`
	Name             string          `json:"name"`
	Arguments        string          `json:"arguments"`
	Input            string          `json:"input"`
	Output           json.RawMessage `json:"output"`
}

type codexSummary struct {
	Text string `json:"text"`
}

// ParseCodexSession turns a Codex rollout into one session.
//
// A turn is closed by `task_complete`, and one aborted by `turn_aborted` is
// discarded whole: half a turn in the corpus is worse than no turn, because it
// reads as an answer the agent never gave.
func ParseCodexSession(content []byte, meta FileMeta) (Records, error) {
	session := Session{
		ID:          meta.SessionID,
		SourceAgent: firstNonEmpty(meta.SourceAgent, "codex"),
		Project:     meta.Project,
		Metadata:    map[string]any{},
	}

	var (
		number     int
		humanText  string
		humanTS    string
		haveHuman  bool
		thinking   []Thinking
		tools      []*ToolUse
		pending    = map[string]*ToolUse{}
		exchanges  []Exchange
		perTurn    [][]*ToolUse
		perTurnThk [][]Thinking
		discards   []Discard
	)
	discard := func() {
		humanText, humanTS, haveHuman = "", "", false
		thinking, tools = nil, nil
		pending = map[string]*ToolUse{}
	}

	for index, raw := range lines(content) {
		var line codexLine
		if err := json.Unmarshal([]byte(raw), &line); err != nil {
			discards = append(discards, Discard{Record: index + 1, Reason: "invalid JSON: " + err.Error()})
			continue
		}
		var payload codexPayload
		if len(line.Payload) > 0 {
			if err := json.Unmarshal(line.Payload, &payload); err != nil {
				discards = append(discards, Discard{Record: index + 1, Reason: "invalid payload: " + err.Error()})
				continue
			}
		}

		switch line.Type {
		case "session_meta":
			if payload.ID != "" {
				session.ID = payload.ID
			}
			session.StartedAt = payload.Timestamp
			putIfSet(session.Metadata, "cwd", payload.Cwd)
			putIfSet(session.Metadata, "cli_version", payload.CLIVersion)
			putIfSet(session.Metadata, "model_provider", payload.ModelProvider)
		case "turn_context":
			putIfSet(session.Metadata, "model", payload.Model)
		case "event_msg":
			switch payload.InnerType {
			case "user_message":
				humanText, humanTS, haveHuman = payload.Message, line.Timestamp, true
			case "task_complete":
				if !haveHuman {
					discards = append(discards, Discard{Record: index + 1, Reason: "completed turn has no user message"})
					continue
				}
				number++
				exchanges = append(exchanges, Exchange{
					Number:         number,
					HumanText:      humanText,
					AgentText:      payload.LastAgentMessage,
					HumanTimestamp: humanTS,
					AgentTimestamp: line.Timestamp,
				})
				perTurn = append(perTurn, tools)
				perTurnThk = append(perTurnThk, thinking)
				discard()
			case "turn_aborted":
				if haveHuman {
					discards = append(discards, Discard{Record: index + 1, Reason: "aborted turn"})
				}
				discard()
			default:
				discards = append(discards, Discard{Record: index + 1, Reason: "unsupported event: " + payload.InnerType})
			}
		case "response_item":
			switch payload.InnerType {
			case "reasoning":
				text := summaryText(payload.Summary)
				thinking = append(thinking, Thinking{Text: text, WordCount: wordCount(text)})
			case "function_call", "custom_tool_call":
				tool := &ToolUse{
					Name:          payload.Name,
					ParamsSummary: Clip(firstNonEmpty(payload.Arguments, payload.Input), paramsBudget),
				}
				tools = append(tools, tool)
				if payload.CallID != "" {
					pending[payload.CallID] = tool
				}
			case "function_call_output", "custom_tool_call_output":
				output := outputText(payload.Output)
				if tool, ok := pending[payload.CallID]; ok && isToolError(output) {
					tool.HadError = true
					tool.ErrorMessage = Clip(output, errorBudget)
				}
			default:
				discards = append(discards, Discard{Record: index + 1, Reason: "unsupported response item: " + payload.InnerType})
			}
		default:
			discards = append(discards, Discard{Record: index + 1, Reason: "unsupported record type: " + line.Type})
		}
	}

	for i := range exchanges {
		exchanges[i].Thinking = perTurnThk[i]
		for _, tool := range perTurn[i] {
			exchanges[i].Tools = append(exchanges[i].Tools, *tool)
		}
		if ts := exchanges[i].AgentTimestamp; ts != "" && ts > session.EndedAt {
			session.EndedAt = ts
		}
	}
	PlaceThinking(exchanges)
	session.Exchanges = exchanges
	session.DurationMinutes = minutesBetween(session.StartedAt, session.EndedAt)
	return Records{Sessions: []Session{session}, Discards: discards}, nil
}

func summaryText(items []codexSummary) string {
	parts := make([]string, 0, len(items))
	for _, item := range items {
		if item.Text != "" {
			parts = append(parts, item.Text)
		}
	}
	return strings.Join(parts, " ")
}

// outputText reads a call's output, which Codex writes either as a JSON string or
// as the document itself.
func outputText(output json.RawMessage) string {
	if len(output) == 0 {
		return ""
	}
	var text string
	if err := json.Unmarshal(output, &text); err == nil {
		return text
	}
	return string(output)
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
