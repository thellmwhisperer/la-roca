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

type pendingCall struct {
	Name    string
	Command string
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

// Observable reports whether ObserveCalls can read tool-call records of this
// kind. Database stores are not live JSONL logs and are not observable.
func Observable(kind Kind) bool {
	switch kind {
	case KindClaudeSession, KindGrokSession, KindCodexSession, KindPiSession:
		return true
	default:
		return false
	}
}

func observeClaude(content []byte) []CallEvent {
	var events []CallEvent
	pending := map[string]pendingCall{}
	_, _ = eachJSONLine(content, func(_ int, raw string) error {
		var line claudeLine
		if err := json.Unmarshal([]byte(raw), &line); err != nil {
			return err
		}
		_, blocks := decodeContent(line.Message)
		for _, block := range blocks {
			switch block.Type {
			case "tool_use":
				pending[block.ID] = pendingCall{Name: block.Name, Command: recordedCommand(block.Input)}
				events = append(events, CallEvent{
					Timestamp: line.stamp(),
					ID:        block.ID,
					Name:      block.Name,
					Params:    paramsSummary(block.Input),
					Command:   recordedCommand(block.Input),
				})
			case "tool_result":
				call := pending[block.ToolUseID]
				events = append(events, CallEvent{
					Timestamp: line.stamp(),
					ID:        block.ToolUseID,
					Name:      call.Name,
					Command:   call.Command,
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
	pending := map[string]pendingCall{}
	_, _ = consumeGrokUpdates(content, func(_ int, line grokUpdateLine) {
		if line.Method != "session/update" {
			return
		}
		update := line.Params.Update
		switch update.SessionUpdate {
		case "tool_call":
			command := recordedCommand(update.RawInput)
			pending[update.ToolCallID] = pendingCall{
				Name:    firstNonEmpty(update.Meta.Tool.Name, update.Kind, update.Title),
				Command: command,
			}
			events = append(events, CallEvent{
				Timestamp: grokTimestamp(line.Timestamp),
				ID:        update.ToolCallID,
				Name:      firstNonEmpty(update.Meta.Tool.Name, update.Kind, update.Title),
				Params:    Clip(rawText(update.RawInput), paramsBudget),
				Command:   command,
			})
		case "tool_call_update":
			call := pending[update.ToolCallID]
			events = append(events, CallEvent{
				Timestamp: grokTimestamp(line.Timestamp),
				ID:        update.ToolCallID,
				Name:      call.Name,
				Command:   call.Command,
				Output:    grokToolOutput(update),
				IsResult:  true,
			})
		}
	})
	return events
}

func observeCodex(content []byte) []CallEvent {
	var events []CallEvent
	pending := map[string]pendingCall{}
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
			command := firstNonEmpty(recordedCommand(payload.Arguments), payload.Input)
			pending[payload.CallID] = pendingCall{Name: payload.Name, Command: command}
			events = append(events, CallEvent{
				Timestamp: validInstant(line.Timestamp),
				ID:        payload.CallID,
				Name:      payload.Name,
				Params:    Clip(params, paramsBudget),
				Command:   command,
			})
		case "function_call_output", "custom_tool_call_output":
			call := pending[payload.CallID]
			events = append(events, CallEvent{
				Timestamp: validInstant(line.Timestamp),
				ID:        payload.CallID,
				Name:      call.Name,
				Command:   call.Command,
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
	pending := map[string]pendingCall{}
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
				pending[block.ID] = pendingCall{Name: block.Name, Command: recordedCommand(block.Arguments)}
				events = append(events, CallEvent{
					Timestamp: stamp,
					ID:        block.ID,
					Name:      block.Name,
					Params:    paramsSummary(block.Arguments),
					Command:   recordedCommand(block.Arguments),
				})
			}
		case "toolResult":
			call := pending[entry.Message.ToolCallID]
			events = append(events, CallEvent{
				Timestamp: stamp,
				ID:        entry.Message.ToolCallID,
				Name:      call.Name,
				Command:   call.Command,
				Output:    piContentText(entry.Message.Content),
				IsResult:  true,
			})
		case "bashExecution":
			if entry.Message.Exclude {
				return nil
			}
			command := strings.TrimSpace(entry.Message.Command)
			id := "bash:" + entry.ID
			output := strings.TrimSpace(entry.Message.Output)
			if output == "" {
				output = piContentText(entry.Message.Content)
			}
			events = append(events, CallEvent{
				Timestamp: stamp,
				ID:        id,
				Name:      "bash",
				Params:    Clip(command, paramsBudget),
				Command:   command,
			})
			events = append(events, CallEvent{
				Timestamp: stamp,
				ID:        id,
				Name:      "bash",
				Command:   command,
				Output:    output,
				IsResult:  true,
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
