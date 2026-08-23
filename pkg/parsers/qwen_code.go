package parsers

import (
	"encoding/json"
	"fmt"
	"strings"
)

type qwenCodeRecord struct {
	Type       string             `json:"type"`
	SessionID  string             `json:"sessionId"`
	UUID       string             `json:"uuid"`
	Timestamp  string             `json:"timestamp"`
	Cwd        string             `json:"cwd"`
	GitBranch  string             `json:"gitBranch"`
	Version    string             `json:"version"`
	Model      string             `json:"model"`
	Message    qwenCodeMessage    `json:"message"`
	Usage      *qwenCodeUsage     `json:"usageMetadata"`
	ToolResult qwenCodeToolResult `json:"toolCallResult"`
}

type qwenCodeMessage struct {
	Role  string         `json:"role"`
	Parts []qwenCodePart `json:"parts"`
}

type qwenCodePart struct {
	Text             string                    `json:"text"`
	FunctionCall     *qwenCodeFunctionCall     `json:"functionCall"`
	FunctionResponse *qwenCodeFunctionResponse `json:"functionResponse"`
}

type qwenCodeFunctionCall struct {
	ID   string          `json:"id"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

type qwenCodeFunctionResponse struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Response struct {
		Output json.RawMessage `json:"output"`
		Error  json.RawMessage `json:"error"`
	} `json:"response"`
}

type qwenCodeToolResult struct {
	Status string          `json:"status"`
	Error  json.RawMessage `json:"error"`
}

type qwenCodeUsage struct {
	Prompt    *int `json:"promptTokenCount"`
	Candidate *int `json:"candidatesTokenCount"`
	Thoughts  *int `json:"thoughtsTokenCount"`
	Cached    *int `json:"cachedContentTokenCount"`
	Total     *int `json:"totalTokenCount"`
}

type qwenCodeTurn struct {
	humanText strings.Builder
	agentText strings.Builder
	humanTS   string
	agentTS   string
	model     string
	usage     UsageTally
	tools     []*ToolUse
	pending   map[string]*ToolUse
	openCalls int
	signal    int
	answered  bool
}

type qwenCodeReader struct {
	sessionID    string
	cwd          string
	gitBranch    string
	version      string
	current      *qwenCodeTurn
	exchanges    []Exchange
	discards     []Discard
	deferred     int
	numberOffset int
}

func detectQwenCode(file File) bool {
	if file.Meta.SourceAgent != "qwen-code" ||
		!strings.HasSuffix(strings.ToLower(file.Meta.FileName), ".jsonl") {
		return false
	}
	for _, raw := range lines(file.Content) {
		var record qwenCodeRecord
		if json.Unmarshal([]byte(raw), &record) == nil && record.SessionID != "" &&
			record.Version != "" && qwenCodeRecordType(record.Type) {
			return true
		}
	}
	return false
}

func qwenCodeRecordType(kind string) bool {
	return kind == "system" || kind == "user" || kind == "assistant" || kind == "tool_result"
}

// ParseQwenCode projects one project chat stream onto its complete turns. Qwen
// records every model request separately during a tool loop, so one exchange
// aggregates those messages and their usage while retaining the model stated
// by the source.
func ParseQwenCode(content []byte, meta FileMeta) (Records, error) {
	reader := qwenCodeReader{numberOffset: meta.ExchangeNumberOffset}
	discards, valid := eachJSONLine(content, func(record int, raw string) error {
		var line qwenCodeRecord
		if err := json.Unmarshal([]byte(raw), &line); err != nil {
			return err
		}
		reader.consume(record, line)
		return nil
	})
	if valid == 0 || reader.sessionID == "" {
		return Records{}, fmt.Errorf("the Qwen Code chat declares no session")
	}
	reader.flush()

	session := Session{
		ID: reader.sessionID, SourceAgent: firstNonEmpty(meta.SourceAgent, "qwen-code"),
		Project: firstNonEmpty(meta.Project, lastSegment(reader.cwd)),
		Metadata: WithoutEmpty(map[string]any{
			"cwd": reader.cwd, "git_branch": reader.gitBranch,
			"qwen_version": reader.version, "source_path": meta.Path,
		}),
		ExchangeNumbersAuthoritative: true,
		Exchanges:                    reader.exchanges,
	}
	session.StartedAt, session.EndedAt, session.DurationMinutes = span(session.Exchanges)
	return Records{Sessions: []Session{session},
		Discards: append(discards, reader.discards...), Deferred: reader.deferred}, nil
}

func (r *qwenCodeReader) consume(record int, line qwenCodeRecord) {
	if line.SessionID == "" {
		r.unreadable(record, "Qwen Code record has no session id")
		return
	}
	if r.sessionID == "" {
		r.sessionID = line.SessionID
	} else if line.SessionID != r.sessionID {
		r.unreadable(record, "Qwen Code record belongs to another session")
		return
	}
	r.cwd = firstNonEmpty(r.cwd, line.Cwd)
	r.gitBranch = firstNonEmpty(r.gitBranch, line.GitBranch)
	r.version = firstNonEmpty(r.version, line.Version)

	switch line.Type {
	case "system":
		r.discards = append(r.discards, Discard{Record: record, ByDesign: true,
			Reason:   "Qwen Code runtime system record not ingested",
			Category: "Qwen Code runtime system record not ingested"})
	case "user":
		r.flush()
		r.current = &qwenCodeTurn{humanTS: line.Timestamp, pending: map[string]*ToolUse{}}
		appendQwenCodeText(&r.current.humanText, line.Message.Parts)
		if r.current.humanText.Len() == 0 {
			r.unreadable(record, "Qwen Code user message has no readable text")
		}
	case "assistant":
		if r.current == nil {
			r.unreadable(record, "Qwen Code assistant message arrived before a user message")
			return
		}
		r.current.claim(line)
	case "tool_result":
		r.claimToolResult(record, line)
	default:
		r.unreadable(record, "unknown Qwen Code record type: "+firstNonEmpty(line.Type, "unnamed"))
	}
}

func (t *qwenCodeTurn) claim(line qwenCodeRecord) {
	appendQwenCodeText(&t.agentText, line.Message.Parts)
	t.agentTS = lastInstant(t.agentTS, line.Timestamp)
	if t.model == "" {
		t.model = line.Model
	}
	if line.Model != "" {
		t.signal++
	}
	if line.Usage != nil {
		if line.Usage.Prompt != nil {
			t.usage.AddInputTokens(*line.Usage.Prompt)
			t.signal++
		}
		if line.Usage.Candidate != nil {
			t.usage.AddOutputTokens(*line.Usage.Candidate)
			t.signal++
		}
		if line.Usage.Thoughts != nil {
			t.usage.AddReasoningTokens(*line.Usage.Thoughts)
			t.signal++
		}
		if line.Usage.Cached != nil {
			t.signal++
		}
		if line.Usage.Total != nil {
			t.signal++
		}
	}
	hasCall := false
	for _, part := range line.Message.Parts {
		call := part.FunctionCall
		if call == nil {
			continue
		}
		hasCall = true
		tool := &ToolUse{Name: call.Name, ParamsSummary: paramsSummary(call.Args)}
		t.tools = append(t.tools, tool)
		t.openCalls++
		if call.ID != "" {
			t.pending[call.ID] = tool
		}
	}
	t.answered = !hasCall
}

func (r *qwenCodeReader) claimToolResult(record int, line qwenCodeRecord) {
	if r.current == nil {
		r.unreadable(record, "Qwen Code tool result arrived before a user message")
		return
	}
	for _, part := range line.Message.Parts {
		response := part.FunctionResponse
		if response == nil {
			continue
		}
		tool := r.current.pending[response.ID]
		if tool == nil {
			r.unreadable(record, "Qwen Code tool result has no matching call")
			continue
		}
		delete(r.current.pending, response.ID)
		r.current.openCalls--
		failed := line.ToolResult.Status != "" && line.ToolResult.Status != "success"
		message := firstNonEmpty(rawText(line.ToolResult.Error), rawText(response.Response.Error))
		tool.HadError = failed || message != ""
		if tool.HadError {
			tool.ErrorMessage = Clip(firstNonEmpty(message, line.ToolResult.Status), errorBudget)
		}
	}
}

func (r *qwenCodeReader) flush() {
	if r.current == nil {
		return
	}
	if !r.current.answered || r.current.openCalls > 0 {
		r.deferred++
		r.current = nil
		return
	}
	exchange := Exchange{
		Number:         r.numberOffset + len(r.exchanges) + 1,
		HumanText:      strings.TrimSpace(r.current.humanText.String()),
		AgentText:      strings.TrimSpace(r.current.agentText.String()),
		HumanTimestamp: r.current.humanTS, AgentTimestamp: r.current.agentTS,
		Provenance: r.current.usage.Provenance(r.current.model, ""),
		Signal:     &r.current.signal,
	}
	exchange.LatencyMS = latency(exchange.HumanTimestamp, exchange.AgentTimestamp)
	for _, tool := range r.current.tools {
		exchange.Tools = append(exchange.Tools, *tool)
	}
	r.exchanges = append(r.exchanges, exchange)
	r.current = nil
}

func appendQwenCodeText(builder *strings.Builder, parts []qwenCodePart) {
	for _, part := range parts {
		text := strings.TrimSpace(part.Text)
		if text == "" {
			continue
		}
		if builder.Len() > 0 {
			builder.WriteByte('\n')
		}
		builder.WriteString(text)
	}
}

func (r *qwenCodeReader) unreadable(record int, reason string) {
	r.discards = append(r.discards, Discard{Record: record, Reason: reason})
}
