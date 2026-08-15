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
	Usage   *claudeUsage    `json:"usage"`
}

// claudeUsage is what one assistant message declares it spent. The prompt is
// billed across three tiers and the transcript states them apart, so the one
// number an operator asks for is their sum; the runtime does not separate the
// reasoning tokens from the rest of the output, and no reasoning total is
// invented for it.
type claudeUsage struct {
	Input         *int `json:"input_tokens"`
	Output        *int `json:"output_tokens"`
	CacheCreation *int `json:"cache_creation_input_tokens"`
	CacheRead     *int `json:"cache_read_input_tokens"`
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
	// model and usage are the turn's provenance, gathered over every assistant
	// message the turn is made of.
	model string
	usage UsageTally

	number          int
	compactions     int
	afterCompaction bool
	orphanedAgent   int

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
	for range builder.orphanedAgent {
		discards = append(discards, Discard{Reason: "assistant content has no open human turn"})
	}
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
	return eachJSONLine(content, func(_ int, raw string) error {
		var line claudeLine
		if err := json.Unmarshal([]byte(raw), &line); err != nil {
			return err
		}
		consume(line)
		return nil
	})
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
	text, blocks := decodeContent(line.Message)
	if b.current == nil {
		if text != "" || len(blocks) > 0 {
			b.orphanedAgent++
		}
		return
	}
	if b.agentTS == "" {
		b.agentTS = line.stamp()
	}
	b.claim(line.Message)
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

// claim records what one assistant message says about how it was produced. A
// turn is answered by several of them, so the counts add up and the first model
// named is the turn's: a transcript that switched model mid-turn still answered
// under the one it started with.
func (b *claudeBuilder) claim(message *claudeMessage) {
	if message == nil {
		return
	}
	if b.model == "" {
		b.model = message.Model
	}
	claimClaudeUsage(&b.usage, message)
}

// claimClaudeUsage adds one assistant message's declared spend to a turn's
// tally. The three prompt tiers are one number because "how many tokens did this
// question cost to ask" is the question an operator has; the runtime does not
// separate the reasoning tokens, so none are claimed.
func claimClaudeUsage(tally *UsageTally, message *claudeMessage) {
	if message == nil || message.Usage == nil {
		return
	}
	usage := message.Usage
	if usage.Input != nil || usage.CacheCreation != nil || usage.CacheRead != nil {
		tally.AddInputTokens(intOrZero(usage.Input) +
			intOrZero(usage.CacheCreation) + intOrZero(usage.CacheRead))
	}
	if usage.Output != nil {
		tally.AddOutputTokens(*usage.Output)
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
		// A Claude transcript names the model that answered and counts what it
		// spent; it names no provider and states no price, so those stay absent.
		Provenance: b.usage.Provenance(b.model, ""),
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
	b.model = ""
	b.usage = UsageTally{}
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
	return validInstant(firstNonEmpty(l.AuditTimestamp, l.Timestamp))
}

// decodeContent reads a message's content, which is either a bare string or a
// list of blocks. Every text block belongs to the message and is retained.
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
	var textParts []string
	for _, block := range blocks {
		if block.Type == "text" {
			textParts = append(textParts, block.Text)
		}
	}
	return strings.Join(textParts, "\n"), blocks
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

// parseISO accepts only timestamps that identify one instant. Zone-less and
// otherwise non-RFC3339 spellings are rejected at the parser boundary because
// guessing a zone would manufacture an anchor that the source never declared.
func parseISO(value string) (time.Time, bool) {
	if value == "" {
		return time.Time{}, false
	}
	if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return parsed, true
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
