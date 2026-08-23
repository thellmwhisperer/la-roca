package parsers

import (
	"encoding/json"
	"strings"
)

// CallEvent is one tool call or tool result as the session log wrote it.
// Command and Output are filled only when the source recorded them; they are
// not inferred from the tool's display name.
type CallEvent struct {
	Timestamp string
	ID        string
	Name      string
	Params    string
	Command   string
	Output    string
	IsResult  bool
}

// ObserveCalls lists tool-call records in source order using the same field
// names the ingest parsers already read. Unknown kinds return nothing.
func ObserveCalls(kind Kind, content []byte) []CallEvent {
	switch kind {
	case KindClaudeSession:
		return observeClaude(content)
	case KindGrokSession:
		return observeGrok(content)
	case KindCodexSession:
		return observeCodex(content)
	case KindPiSession:
		return observePi(content)
	default:
		return nil
	}
}

func observeClaude(content []byte) []CallEvent {
	var events []CallEvent
	_, _ = eachJSONLine(content, func(_ int, raw string) error {
		var line claudeLine
		if err := json.Unmarshal([]byte(raw), &line); err != nil {
			return err
		}
		_, blocks := decodeContent(line.Message)
		for _, block := range blocks {
			switch block.Type {
			case "tool_use":
				events = append(events, CallEvent{
					Timestamp: line.stamp(),
					ID:        block.ID,
					Name:      block.Name,
					Params:    paramsSummary(block.Input),
					Command:   recordedCommand(block.Input),
				})
			case "tool_result":
				events = append(events, CallEvent{
					Timestamp: line.stamp(),
					ID:        block.ToolUseID,
					Output:    resultText(block.Content),
					IsResult:  true,
				})
			}
		}
		return nil
	})
	return events
}

func observeGrok(content []byte) []CallEvent {
	var events []CallEvent
	_, _ = consumeGrokUpdates(content, func(_ int, line grokUpdateLine) {
		if line.Method != "session/update" {
			return
		}
		update := line.Params.Update
		switch update.SessionUpdate {
		case "tool_call":
			events = append(events, CallEvent{
				Timestamp: grokTimestamp(line.Timestamp),
				ID:        update.ToolCallID,
				Name:      firstNonEmpty(update.Meta.Tool.Name, update.Kind, update.Title),
				Params:    Clip(rawText(update.RawInput), paramsBudget),
				Command:   recordedCommand(update.RawInput),
			})
		case "tool_call_update":
			events = append(events, CallEvent{
				Timestamp: grokTimestamp(line.Timestamp),
				ID:        update.ToolCallID,
				Output:    grokToolOutput(update),
				IsResult:  true,
			})
		}
	})
	return events
}

func observeCodex(content []byte) []CallEvent {
	var events []CallEvent
	_, _ = eachJSONLine(content, func(_ int, raw string) error {
		var line codexLine
		if err := json.Unmarshal([]byte(raw), &line); err != nil {
			return err
		}
		if line.Type != "response_item" {
			return nil
		}
		var payload codexPayload
		if err := json.Unmarshal(line.Payload, &payload); err != nil {
			return err
		}
		switch payload.InnerType {
		case "function_call", "custom_tool_call":
			params := firstNonEmpty(rawText(payload.Arguments), payload.Input)
			events = append(events, CallEvent{
				Timestamp: validInstant(line.Timestamp),
				ID:        payload.CallID,
				Name:      payload.Name,
				Params:    Clip(params, paramsBudget),
				Command:   firstNonEmpty(recordedCommand(payload.Arguments), payload.Input),
			})
		case "function_call_output", "custom_tool_call_output":
			events = append(events, CallEvent{
				Timestamp: validInstant(line.Timestamp),
				ID:        payload.CallID,
				Output:    rawText(payload.Output),
				IsResult:  true,
			})
		}
		return nil
	})
	return events
}

func observePi(content []byte) []CallEvent {
	var events []CallEvent
	_, _ = eachJSONLine(content, func(_ int, raw string) error {
		var entry piEntry
		if err := json.Unmarshal([]byte(raw), &entry); err != nil {
			return err
		}
		if entry.Type != "message" || entry.Message == nil {
			return nil
		}
		stamp := isoFromAnyInstant(firstInstant(entry.Message.Timestamp, entry.Timestamp))
		switch entry.Message.Role {
		case "assistant":
			var blocks []piBlock
			if json.Unmarshal(entry.Message.Content, &blocks) != nil {
				return nil
			}
			for _, block := range blocks {
				if block.Type != "toolCall" || block.ID == "" {
					continue
				}
				events = append(events, CallEvent{
					Timestamp: stamp,
					ID:        block.ID,
					Name:      block.Name,
					Params:    paramsSummary(block.Arguments),
					Command:   recordedCommand(block.Arguments),
				})
			}
		case "toolResult":
			events = append(events, CallEvent{
				Timestamp: stamp,
				ID:        entry.Message.ToolCallID,
				Output:    piContentText(entry.Message.Content),
				IsResult:  true,
			})
		case "bashExecution":
			if entry.Message.Exclude {
				return nil
			}
			events = append(events, CallEvent{
				Timestamp: stamp,
				ID:        "bash:" + entry.ID,
				Name:      "bash",
				Params:    Clip(strings.TrimSpace(entry.Message.Command), paramsBudget),
				Command:   strings.TrimSpace(entry.Message.Command),
				Output:    piContentText(entry.Message.Content),
			})
		}
		return nil
	})
	return events
}

func recordedCommand(raw json.RawMessage) string {
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil {
		return ""
	}
	return strings.TrimSpace(rawText(object["command"]))
}
