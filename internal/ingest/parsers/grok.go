package parsers

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// grokLine is one record of a Grok Build chat_history.jsonl. The file is the
// agent's active conversation: a compacted window of the last exchange, not the
// whole session. Older turns are written elsewhere (compaction segments and the
// raw protocol stream), so this parser reads exactly what the transcript keeps.
type grokLine struct {
	Type            string          `json:"type"`
	Content         json.RawMessage `json:"content"`
	SyntheticReason string          `json:"synthetic_reason"`
	Summary         json.RawMessage `json:"summary"`
	ToolCalls       []grokToolCall  `json:"tool_calls"`
	ModelID         string          `json:"model_id"`
	ToolCallID      string          `json:"tool_call_id"`
}

type grokToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// grokSummaryBlock is one element of a reasoning record's `summary` array. The
// full reasoning text is written separately as `encrypted_content`, which this
// build cannot read; the summary is the readable part.
type grokSummaryBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// grokExitCode finds the exit status a tool result states. Grok writes tool
// verdicts as text, and a result that ran a shell command states its exit code
// as `exit: N` near the start of the text. Only that explicit statement counts
// as a failure: guessing one out of the words would file every result that
// printed "error" as a failed call.
var grokExitCode = regexp.MustCompile(`\bexit:\s*(-?\d+)`)

// grokTurn is one human turn and the agent activity it drew, from the human
// message to the next one.
type grokTurn struct {
	humanText string
	model     string
	agentText []string
	thinking  []Thinking
	tools     []*ToolUse
	blocks    int
}

// grokReader walks one chat_history.jsonl.
type grokReader struct {
	pending   map[string]*ToolUse
	current   *grokTurn
	exchanges []Exchange
	discards  []Discard
	deferred  int
}

// ParseGrokSession turns a Grok Build chat_history.jsonl into one session.
//
// A real human message opens a turn and the agent's reasoning, answers and tool
// calls fill it until the next real human message closes it. The records Grok
// injects as user turns for its own machinery — compaction history and system
// reminders — are marked `synthetic_reason` and are not human turns, so they are
// excluded by name instead of being stored as questions nobody asked.
func ParseGrokSession(content []byte, meta FileMeta) (Records, error) {
	reader := &grokReader{pending: map[string]*ToolUse{}}
	discards, _ := consumeGrokLines(content, reader.consume)
	unanswered := reader.current != nil && reader.current.blocks == 0
	reader.flush()
	if unanswered {
		reader.deferred++
	}

	sidecar := readGrokSummary(meta.Sidecar)
	session := Session{
		ID:          firstNonEmpty(sidecar.ID, meta.SessionID),
		SourceAgent: firstNonEmpty(meta.SourceAgent, "grok"),
		Project:     meta.Project,
		Title:       sidecar.Title,
		StartedAt:   sidecar.StartedAt,
		EndedAt:     sidecar.EndedAt,
		Metadata:    map[string]any{},
		Exchanges:   reader.exchanges,
	}
	if session.StartedAt != "" && session.EndedAt != "" {
		session.DurationMinutes = minutesBetween(session.StartedAt, session.EndedAt)
	}
	PlaceThinking(session.Exchanges)
	return Records{
		Sessions: []Session{session},
		Discards: append(discards, reader.discards...),
		Deferred: reader.deferred,
	}, nil
}

// consumeGrokLines feeds every record to the reader. One invalid line is a
// discard and never the whole file: a live transcript can be mid-write.
func consumeGrokLines(content []byte, consume func(int, grokLine)) ([]Discard, int) {
	return eachJSONLine(content, func(record int, raw string) {
		var line grokLine
		_ = json.Unmarshal([]byte(raw), &line)
		consume(record, line)
	})
}

func (r *grokReader) consume(record int, line grokLine) {
	switch line.Type {
	case "system":
		r.exclude(record, "runtime prompt", "")
	case "user":
		if line.SyntheticReason != "" {
			r.exclude(record, "runtime machinery injected as a user turn", line.SyntheticReason)
			return
		}
		r.flush()
		r.current = &grokTurn{humanText: grokUserText(line.Content)}
	case "reasoning":
		text := grokReasoningText(line.Summary)
		if strings.TrimSpace(text) == "" {
			// The transcript keeps the reasoning encrypted and writes no summary of
			// it, so there was never anything to read. It is an exclusion and not a
			// failure, exactly as Codex's unreadable reasoning is.
			r.excludeRecord(record, "grok reasoning kept no readable summary")
			return
		}
		r.claim(func(turn *grokTurn) {
			turn.thinking = append(turn.thinking, Thinking{Text: text, WordCount: wordCount(text)})
		})
	case "assistant":
		text := grokAssistantText(line.Content)
		r.claim(func(turn *grokTurn) {
			if turn.model == "" {
				turn.model = line.ModelID
			}
			if strings.TrimSpace(text) != "" {
				turn.agentText = append(turn.agentText, text)
			}
			for _, call := range line.ToolCalls {
				tool := &ToolUse{Name: call.Name, ParamsSummary: Clip(call.Arguments, paramsBudget)}
				turn.tools = append(turn.tools, tool)
				if call.ID != "" {
					r.pending[call.ID] = tool
				}
			}
		})
	case "tool_result":
		r.claim(func(turn *grokTurn) {
			r.verdict(record, line.ToolCallID, string(line.Content))
		})
	default:
		r.exclude(record, "record type", line.Type)
	}
}

// claim counts one record of agent activity into the open turn. A record with no
// open human turn is an orphan: the agent answered a question the transcript
// never kept, and nothing can be the exchange it belonged to.
func (r *grokReader) claim(fill func(*grokTurn)) {
	if r.current == nil {
		return
	}
	fill(r.current)
	r.current.blocks++
}

// verdict carries a tool result's exit status back to the call it answered.
// Without it every tool use would look successful, because the call itself never
// says how it went.
func (r *grokReader) verdict(record int, callID string, content string) {
	tool, ok := r.pending[callID]
	if !ok {
		r.discards = append(r.discards, Discard{Record: record,
			Reason:   "tool verdict has unknown call_id: " + callID,
			Category: "tool verdict has unknown call_id"})
		return
	}
	if failedGrokExit(content) {
		tool.HadError = true
		tool.ErrorMessage = Clip(content, errorBudget)
	}
}

// flush closes the open turn into an exchange. A human turn with no agent
// activity is not an exchange yet: it is a question still in flight, and the
// next ingest of the grown file lands it.
func (r *grokReader) flush() {
	if r.current == nil || r.current.blocks == 0 {
		r.current = nil
		r.pending = map[string]*ToolUse{}
		return
	}
	exchange := Exchange{
		Number:     len(r.exchanges) + 1,
		HumanText:  r.current.humanText,
		AgentText:  strings.Join(r.current.agentText, "\n"),
		Thinking:   r.current.thinking,
		Provenance: Provenance{Model: r.current.model},
	}
	// The calls stay pointers until the turn is closed so a verdict that arrives
	// in time still lands; the copies are only made here.
	for _, tool := range r.current.tools {
		exchange.Tools = append(exchange.Tools, *tool)
	}
	r.exchanges = append(r.exchanges, exchange)
	r.current = nil
	r.pending = map[string]*ToolUse{}
}

func (r *grokReader) exclude(record int, kind, name string) {
	r.discards = append(r.discards, Discard{
		Record: record, ByDesign: true,
		Reason:   "grok runtime " + kind + " not ingested: " + firstNonEmpty(name, "unnamed"),
		Category: "grok runtime " + kind + " not ingested",
	})
}

func (r *grokReader) excludeRecord(record int, reason string) {
	r.discards = append(r.discards, Discard{Record: record, Reason: reason, ByDesign: true})
}

// grokUserText joins the content blocks of a real human message.
func grokUserText(content json.RawMessage) string {
	var blocks []grokSummaryBlock
	if err := json.Unmarshal(content, &blocks); err != nil {
		return ""
	}
	return joinBlockTexts(blocks, func(block grokSummaryBlock) string { return block.Text }, "\n")
}

// grokAssistantText reads the agent's answer, which Grok writes as a bare string.
func grokAssistantText(content json.RawMessage) string {
	var text string
	if err := json.Unmarshal(content, &text); err != nil {
		return ""
	}
	return text
}

// grokReasoningText joins the readable summaries of one reasoning record. The
// summaries are fragments of one stream of thought, so they are joined with a
// space rather than a line break, exactly as Codex's reasoning is.
func grokReasoningText(summary json.RawMessage) string {
	var blocks []grokSummaryBlock
	if err := json.Unmarshal(summary, &blocks); err != nil {
		return ""
	}
	return joinBlockTexts(blocks, func(block grokSummaryBlock) string { return block.Text }, " ")
}

// failedGrokExit is true only when a tool result explicitly states a failing
// exit code. A result that states no exit code is not an error.
func failedGrokExit(content string) bool {
	window := content
	if len(window) > 300 {
		window = window[:300]
	}
	match := grokExitCode.FindStringSubmatch(window)
	if len(match) != 2 {
		return false
	}
	var code int
	if _, err := fmt.Sscanf(match[1], "%d", &code); err != nil {
		return false
	}
	return code != 0
}

// grokSummary is the structured session metadata Grok writes as summary.json.
type grokSummary struct {
	Info struct {
		ID  string `json:"id"`
		Cwd string `json:"cwd"`
	} `json:"info"`
	SessionSummary        string   `json:"session_summary"`
	CreatedAt             string   `json:"created_at"`
	UpdatedAt             string   `json:"updated_at"`
	LastActiveAt          string   `json:"last_active_at"`
	NumMessages           int      `json:"num_messages"`
	NumChatMessages       int      `json:"num_chat_messages"`
	CurrentModelID        string   `json:"current_model_id"`
	GitRootDir            string   `json:"git_root_dir"`
	GitRemotes            []string `json:"git_remotes"`
	HeadCommit            string   `json:"head_commit"`
	HeadBranch            string   `json:"head_branch"`
	AgentName             string   `json:"agent_name"`
	SandboxProfile        string   `json:"sandbox_profile"`
	ReasoningEffort       string   `json:"reasoning_effort"`
	RequestID             string   `json:"request_id"`
	ChatFormatVersion     int      `json:"chat_format_version"`
	GeneratedTitle        string   `json:"generated_title"`
	LastTurnSummary       string   `json:"last_turn_summary"`
	LastTurnSummaryPrompt string   `json:"last_turn_summary_prompt_id"`
}

// grokSummaryView is the little the transcript path needs from its paired
// summary.json.
type grokSummaryView struct {
	ID        string
	Title     string
	StartedAt string
	EndedAt   string
}

func readGrokSummary(content []byte) grokSummaryView {
	if len(content) == 0 {
		return grokSummaryView{}
	}
	var summary grokSummary
	if err := json.Unmarshal(content, &summary); err != nil {
		return grokSummaryView{}
	}
	return grokSummaryView{
		ID:        summary.Info.ID,
		Title:     strings.TrimSpace(summary.GeneratedTitle),
		StartedAt: validInstant(summary.CreatedAt),
		EndedAt:   validInstant(firstNonEmpty(summary.LastActiveAt, summary.UpdatedAt)),
	}
}

// ParseGrokSessionMetadata turns a Grok Build summary.json into a session
// snapshot: no exchange, and every field it does know merged over whatever the
// transcript already wrote.
func ParseGrokSessionMetadata(content []byte, meta FileMeta) (Records, error) {
	var summary grokSummary
	if err := json.Unmarshal(content, &summary); err != nil {
		return Records{Discards: []Discard{{Record: 1,
			Reason: "invalid metadata JSON: " + err.Error(), Category: "invalid metadata JSON"}}}, nil
	}
	sessionID := firstNonEmpty(summary.Info.ID, meta.SessionID)
	if sessionID == "" {
		return Records{Discards: []Discard{{Record: 1,
			Reason: "grok session metadata has no identity (info.id)"}}}, nil
	}

	started := validInstant(summary.CreatedAt)
	ended := validInstant(firstNonEmpty(summary.LastActiveAt, summary.UpdatedAt))
	session := Session{
		ID:              sessionID,
		SourceAgent:     firstNonEmpty(meta.SourceAgent, "grok"),
		Project:         meta.Project,
		StartedAt:       started,
		EndedAt:         ended,
		DurationMinutes: minutesBetween(started, ended),
		Title:           strings.TrimSpace(summary.GeneratedTitle),
		Snapshot:        true,
		Metadata: WithoutEmpty(map[string]any{
			"cwd":                      summary.Info.Cwd,
			"model":                    summary.CurrentModelID,
			"git_root_dir":             summary.GitRootDir,
			"git_remotes":              summary.GitRemotes,
			"head_commit":              summary.HeadCommit,
			"head_branch":              summary.HeadBranch,
			"agent_name":               summary.AgentName,
			"sandbox_profile":          summary.SandboxProfile,
			"reasoning_effort":         summary.ReasoningEffort,
			"session_summary":          summary.SessionSummary,
			"num_messages":             summary.NumMessages,
			"num_chat_messages":        summary.NumChatMessages,
			"chat_format_version":      summary.ChatFormatVersion,
			"request_id":               summary.RequestID,
			"last_turn_summary":        summary.LastTurnSummary,
			"last_turn_summary_prompt": summary.LastTurnSummaryPrompt,
		}),
	}
	return Records{Sessions: []Session{session}}, nil
}
