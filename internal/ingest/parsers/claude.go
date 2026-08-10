package parsers

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// paramsBudget and errorBudget keep summaries and stored errors readable.
const (
	paramsBudget = 500
	errorBudget  = 1000
)

// claudeLine is one line of a Claude Code transcript. Only the fields the ingest
// reads are declared: a transcript carries far more, and a parser that decoded
// all of it would break every time the agent added a field.
type claudeLine struct {
	Type string `json:"type"`
	// Timestamp is the transcript's own; AuditTimestamp is what a Cowork audit
	// writes instead, and it wins when it is there.
	Timestamp      string         `json:"timestamp"`
	AuditTimestamp string         `json:"_audit_timestamp"`
	Cwd            string         `json:"cwd"`
	SessionID      string         `json:"session_id"`
	Message        *claudeMessage `json:"message"`
}

type claudeMessage struct {
	Content json.RawMessage `json:"content"`
	Model   string          `json:"model"`
}

// claudeBlock is one content block. `content` is only read on a tool_result,
// where it carries the error text.
type claudeBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	IsError   bool            `json:"is_error"`
	Content   json.RawMessage `json:"content"`
}

// turn is the human side of an exchange being built.
type turn struct {
	text      string
	timestamp string
}

// claudeBuilder is the state machine both Claude Code and Cowork transcripts
// walk: one human turn plus the aggregated agent response is one exchange.
type claudeBuilder struct {
	// auditMode is the Cowork variant. Its human turn is reset after each flush
	// so a second agent block does not reopen it.
	auditMode bool

	current   *turn
	blocks    int
	agentTS   string
	agentText []string
	thinking  []Thinking
	tools     []*ToolUse
	pending   map[string]*ToolUse

	number          int
	compactions     int
	afterCompaction bool

	exchanges []Exchange
	// toolRefs holds each closed exchange's calls by pointer, one slice per
	// exchange, until finish materializes them.
	toolRefs [][]*ToolUse
}

// ParseClaudeSession turns a Claude Code transcript into one session.
//
// An exchange is a human turn plus everything the agent answered until the next
// one. A `summary` line is a compaction: it closes the open exchange and marks
// the next one, which is what makes "what did we decide before the compaction"
// answerable.
func ParseClaudeSession(content []byte, meta FileMeta) (Records, error) {
	builder := &claudeBuilder{pending: map[string]*ToolUse{}}
	cwd, model := "", ""
	discards, validLines := consumeClaudeLines(content, func(line claudeLine) {
		if cwd == "" {
			cwd = line.Cwd
		}
		if model == "" && line.Message != nil {
			model = line.Message.Model
		}
		builder.consume(line)
	})
	if validLines == 0 && len(lines(content)) > 0 {
		return Records{}, fmt.Errorf("the Claude transcript contains no valid JSON lines")
	}
	deferred := builder.current != nil && builder.blocks == 0
	builder.flush()

	session := Session{
		ID:          meta.SessionID,
		SourceAgent: firstNonEmpty(meta.SourceAgent, "claude"),
		Project:     meta.Project,
		Exchanges:   builder.finish(),
		Metadata:    map[string]any{},
	}
	if cwd != "" {
		session.Metadata["cwd"] = cwd
	}
	if model != "" {
		session.Metadata["model"] = model
	}
	if builder.compactions > 0 {
		session.Metadata["compactions"] = builder.compactions
	}
	session.StartedAt, session.EndedAt, session.DurationMinutes = span(session.Exchanges)
	return Records{Sessions: []Session{session}, Discards: discards, Deferred: boolCount(deferred)}, nil
}

// ParseCoworkAudit turns a Cowork audit transcript into one session, merging in
// what its paired metadata file declares about it.
func ParseCoworkAudit(content []byte, meta FileMeta) (Records, error) {
	builder := &claudeBuilder{auditMode: true, pending: map[string]*ToolUse{}}
	firstSessionID := ""
	discards, _ := consumeClaudeLines(content, func(line claudeLine) {
		if firstSessionID == "" {
			firstSessionID = line.SessionID
		}
		builder.consume(line)
	})
	deferred := builder.current != nil && builder.blocks == 0
	builder.flush()
	exchanges := builder.finish()
	if len(exchanges) == 0 {
		return Records{Discards: discards, Deferred: boolCount(deferred)}, nil
	}

	sidecar := readSessionMetadata(meta.Sidecar)
	session := Session{
		ID:          firstNonEmpty(sidecar.sessionID, meta.SessionID, firstSessionID),
		SourceAgent: firstNonEmpty(meta.SourceAgent, "cowork"),
		Project:     meta.Project,
		Title:       sidecar.title,
		Snapshot:    true,
		Exchanges:   exchanges,
		Metadata:    map[string]any{"entrypoint": "claude-cowork"},
	}
	if sidecar.initialMessage != "" {
		session.Metadata["initial_message"] = sidecar.initialMessage
	}
	session.StartedAt, session.EndedAt, session.DurationMinutes = span(exchanges)
	return Records{Sessions: []Session{session}, Discards: discards, Deferred: boolCount(deferred)}, nil
}

func boolCount(value bool) int {
	if value {
		return 1
	}
	return 0
}

func consumeClaudeLines(content []byte, consume func(claudeLine)) ([]Discard, int) {
	var discards []Discard
	valid := 0
	for index, raw := range lines(content) {
		var line claudeLine
		if err := json.Unmarshal([]byte(raw), &line); err != nil {
			discards = append(discards, Discard{Record: index + 1, Reason: "invalid JSON: " + err.Error()})
			continue
		}
		valid++
		consume(line)
	}
	return discards, valid
}

func (b *claudeBuilder) consume(line claudeLine) {
	switch line.Type {
	case "summary":
		if b.auditMode {
			return // a Cowork audit has no compaction line
		}
		b.compactions++
		b.flush()
		b.current = nil
		b.afterCompaction = true
	case "user":
		b.consumeUser(line)
	case "assistant":
		b.consumeAssistant(line)
	}
}

func (b *claudeBuilder) consumeUser(line claudeLine) {
	text, blocks := decodeContent(line.Message)
	hasToolResults := false
	for _, block := range blocks {
		if block.Type != "tool_result" {
			continue
		}
		hasToolResults = true
		b.backfill(block)
	}

	// A line that is only tool results is not a turn boundary: it is the runtime
	// answering itself inside the current human turn.
	if hasToolResults && text == "" {
		return
	}

	b.flush()
	if b.auditMode {
		b.current = &turn{text: text, timestamp: line.stamp()}
		return
	}
	// Only a line with real human text, or one that is not a tool answer at all,
	// opens a new turn.
	if text != "" || !hasToolResults {
		b.current = &turn{text: text, timestamp: line.stamp()}
	}
}

// backfill carries a tool result's verdict back to the call it answered. Without
// it every tool use would look successful, because the call itself never says so.
func (b *claudeBuilder) backfill(block claudeBlock) {
	if block.ToolUseID == "" {
		return
	}
	tool, ok := b.pending[block.ToolUseID]
	if !ok {
		return
	}
	if block.IsError {
		tool.HadError = true
		tool.ErrorMessage = Clip(resultText(block.Content), errorBudget)
	}
	delete(b.pending, block.ToolUseID)
}

func (b *claudeBuilder) consumeAssistant(line claudeLine) {
	if b.current == nil {
		return
	}
	if b.agentTS == "" {
		b.agentTS = line.stamp()
	}
	text, blocks := decodeContent(line.Message)
	// A Cowork audit may write the agent's answer as a bare string.
	if len(blocks) == 0 && text != "" {
		b.blocks++
		b.agentText = append(b.agentText, text)
		return
	}
	for _, block := range blocks {
		b.blocks++
		switch block.Type {
		case "text":
			b.agentText = append(b.agentText, block.Text)
		case "thinking":
			b.thinking = append(b.thinking, Thinking{
				Text:      block.Thinking,
				WordCount: wordCount(block.Thinking),
			})
		case "tool_use":
			tool := &ToolUse{Name: block.Name, ParamsSummary: paramsSummary(block.Input)}
			b.tools = append(b.tools, tool)
			if block.ID != "" {
				b.pending[block.ID] = tool
			}
		}
	}
}

// flush closes the open exchange. A human turn with no agent answer is not an
// exchange yet: it is a question still in flight, and the next ingest will pick
// it up when it has been answered.
func (b *claudeBuilder) flush() {
	// Nothing to close leaves the state exactly as it was. Resetting here would
	// wipe the compaction mark a `summary` line just raised, and the exchange
	// after a compaction would stop being findable as such.
	if b.current == nil || b.blocks == 0 {
		return
	}
	b.number++
	exchange := Exchange{
		Number:            b.number,
		IsAfterCompaction: b.afterCompaction,
		HumanText:         b.current.text,
		AgentText:         strings.Join(b.agentText, "\n"),
		HumanTimestamp:    b.current.timestamp,
		AgentTimestamp:    b.agentTS,
		LatencyMS:         latency(b.current.timestamp, b.agentTS),
		Thinking:          b.thinking,
	}
	for i := range exchange.Thinking {
		exchange.Thinking[i].IsAfterCompaction = b.afterCompaction
	}
	b.exchanges = append(b.exchanges, exchange)
	// The calls stay pointers until the file is over: a tool_result that arrives
	// after the exchange was closed still has to carry its verdict back, or a
	// failed call would be filed as a success.
	b.toolRefs = append(b.toolRefs, b.tools)
	if b.auditMode {
		b.current = nil
	}
	b.reset()
}

func (b *claudeBuilder) reset() {
	b.blocks = 0
	b.agentTS = ""
	b.agentText = nil
	b.thinking = nil
	b.tools = nil
	b.afterCompaction = false
}

// finish materializes the tool calls and places the thinking blocks. Neither can
// be known before the last line has been read.
func (b *claudeBuilder) finish() []Exchange {
	PlaceThinking(b.exchanges)
	for i := range b.exchanges {
		for _, tool := range b.toolRefs[i] {
			b.exchanges[i].Tools = append(b.exchanges[i].Tools, *tool)
		}
	}
	return b.exchanges
}

// stamp is the transcript's timestamp, or the audit's when that is what the file
// writes.
func (l claudeLine) stamp() string {
	return firstNonEmpty(l.AuditTimestamp, l.Timestamp)
}

// decodeContent reads a message's content, which is either a bare string or a
// list of blocks. A list's text is its first text block because a user turn has
// one.
func decodeContent(message *claudeMessage) (string, []claudeBlock) {
	if message == nil || len(message.Content) == 0 {
		return "", nil
	}
	var text string
	if err := json.Unmarshal(message.Content, &text); err == nil {
		return text, nil
	}
	var blocks []claudeBlock
	if err := json.Unmarshal(message.Content, &blocks); err != nil {
		return "", nil
	}
	for _, block := range blocks {
		if block.Type == "text" {
			return block.Text, blocks
		}
	}
	return "", blocks
}

// resultText reads a tool result's payload, string or list of text blocks.
func resultText(content json.RawMessage) string {
	if len(content) == 0 {
		return ""
	}
	var text string
	if err := json.Unmarshal(content, &text); err == nil {
		return text
	}
	var blocks []claudeBlock
	if err := json.Unmarshal(content, &blocks); err != nil {
		return ""
	}
	parts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		parts = append(parts, block.Text)
	}
	return strings.Join(parts, "\n")
}

// paramsSummary keeps the call's arguments as the file wrote them, clipped.
// Re-serializing them would reorder the keys, and a summary that changes shape
// between two runs of the same file is a summary that cannot be compared.
func paramsSummary(input json.RawMessage) string {
	text := strings.TrimSpace(string(input))
	if text == "" || text == "null" || text == "{}" || text == "[]" {
		return ""
	}
	return Clip(text, paramsBudget)
}

// span is the session's start, end and duration, read off its exchanges: the
// first thing the human said and the last thing the agent answered.
func span(exchanges []Exchange) (string, string, *int) {
	var started, ended string
	for _, exchange := range exchanges {
		if ts := exchange.HumanTimestamp; ts != "" && (started == "" || ts < started) {
			started = ts
		}
		if ts := exchange.AgentTimestamp; ts != "" && (ended == "" || ts > ended) {
			ended = ts
		}
	}
	return started, ended, minutesBetween(started, ended)
}

// elapsed is the time from one ISO 8601 instant to another, and only when both
// ends are readable.
func elapsed(from, to string) (time.Duration, bool) {
	start, haveStart := parseISO(from)
	end, haveEnd := parseISO(to)
	return end.Sub(start), haveStart && haveEnd
}

// minutesBetween is the whole minutes from one instant to another, nil when
// either end is unreadable.
func minutesBetween(from, to string) *int {
	span, ok := elapsed(from, to)
	if !ok {
		return nil
	}
	minutes := int(span.Seconds() / 60)
	return &minutes
}

// latency is the milliseconds the agent took, and only when that is a positive
// number: a clock that went backwards is not a measurement.
func latency(human, agent string) *int {
	span, ok := elapsed(human, agent)
	if !ok || span < 0 {
		return nil
	}
	value := int(span.Milliseconds())
	return &value
}

// parseISO reads the timestamp shapes the agents write. It is not the clock: it
// reads what the file says, which is what keeps a parser deterministic.
func parseISO(value string) (time.Time, bool) {
	if value == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05.999999999"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed, true
		}
	}
	return time.Time{}, false
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
