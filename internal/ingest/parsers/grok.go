package parsers

import (
	"encoding/json"
	"net/url"
	"path/filepath"
	"strings"
)

// grokUpdateLine is one record from updates.jsonl, Grok Build's durable session
// stream. Content updates use method session/update. The parallel
// _x.ai/session/update method carries hooks, scheduling and other runtime state
// and is deliberately kept out of conversations.
type grokUpdateLine struct {
	Method string `json:"method"`
	Params struct {
		SessionID string `json:"sessionId"`
		Meta      struct {
			EventID  string `json:"eventId"`
			PromptID string `json:"promptId"`
		} `json:"_meta"`
		Update grokUpdate `json:"update"`
	} `json:"params"`
	Timestamp *float64 `json:"timestamp"`
}

type grokUpdate struct {
	SessionUpdate string          `json:"sessionUpdate"`
	Content       json.RawMessage `json:"content"`
	RawInput      json.RawMessage `json:"rawInput"`
	RawOutput     json.RawMessage `json:"rawOutput"`
	ToolCallID    string          `json:"toolCallId"`
	Title         string          `json:"title"`
	Kind          string          `json:"kind"`
	Status        string          `json:"status"`
	Entries       []grokPlanEntry `json:"entries"`
	Meta          struct {
		ModelID     string          `json:"modelId"`
		PromptIndex json.RawMessage `json:"promptIndex"`
		Tool        struct {
			Name string `json:"name"`
		} `json:"x.ai/tool"`
	} `json:"_meta"`
}

type grokContent struct {
	Type     string `json:"type"`
	Text     string `json:"text"`
	URI      string `json:"uri"`
	MIMEType string `json:"mimeType"`
}

type grokPlanEntry struct {
	Content string `json:"content"`
	Status  string `json:"status"`
}

// grokTurn is one user prompt and the chunk streams it drew. updates.jsonl
// writes answer and thought fragments separately, so strings are assembled in
// record order and only become an Exchange when the next prompt arrives.
type grokTurn struct {
	promptIndex string
	humanText   strings.Builder
	agentText   strings.Builder
	thoughtText strings.Builder
	planText    string
	humanTS     string
	agentTS     string
	model       string
	tools       []*ToolUse
	blocks      int
}

type grokReader struct {
	pending   map[string]*ToolUse
	current   *grokTurn
	exchanges []Exchange
	discards  []Discard
	deferred  int
}

// ParseGrokSession turns one updates.jsonl stream into one session. The session
// UUID and workspace are path identity, not mutable payload metadata. summary.json
// remains a title sidecar; timestamps come from the primary stream itself.
func ParseGrokSession(content []byte, meta FileMeta) (Records, error) {
	reader := &grokReader{pending: map[string]*ToolUse{}}
	discards, _ := consumeGrokUpdates(content, reader.consume)
	unanswered := reader.current != nil && reader.current.blocks == 0
	reader.flush(false)
	if unanswered {
		reader.deferred++
	}

	pathSession, pathProject, workspace := grokPathIdentity(meta.Path)
	sidecar := readGrokSummary(meta.Sidecar)
	session := Session{
		ID:          firstNonEmpty(pathSession, meta.SessionID),
		SourceAgent: firstNonEmpty(meta.SourceAgent, "grok"),
		Project:     firstNonEmpty(meta.Project, pathProject),
		Title:       sidecar.Title,
		Metadata:    WithoutEmpty(map[string]any{"cwd": workspace}),
		Exchanges:   reader.exchanges,
	}
	session.StartedAt, session.EndedAt, session.DurationMinutes = span(session.Exchanges)
	PlaceThinking(session.Exchanges)
	return Records{
		Sessions: []Session{session},
		Discards: append(discards, reader.discards...),
		Deferred: reader.deferred,
	}, nil
}

func consumeGrokUpdates(content []byte, consume func(int, grokUpdateLine)) ([]Discard, int) {
	return eachJSONLine(content, func(record int, raw string) error {
		var line grokUpdateLine
		if err := json.Unmarshal([]byte(raw), &line); err != nil {
			return err
		}
		consume(record, line)
		return nil
	})
}

func (r *grokReader) consume(record int, line grokUpdateLine) {
	if line.Method == "_x.ai/session/update" {
		r.exclude(record, "runtime update", line.Params.Update.SessionUpdate)
		return
	}
	if line.Method != "session/update" {
		r.unreadable(record, "unknown Grok update method: "+firstNonEmpty(line.Method, "unnamed"))
		return
	}

	timestamp := grokTimestamp(line.Timestamp)
	switch line.Params.Update.SessionUpdate {
	case "user_message_chunk":
		promptIndex := rawText(line.Params.Update.Meta.PromptIndex)
		if r.current == nil || r.current.answered(promptIndex) {
			// A following prompt proves the previous user turn is closed even when
			// Grok recorded no agent activity for it. Only the final unanswered
			// prompt can still be in flight and therefore deferred.
			r.flush(true)
			r.current = &grokTurn{
				promptIndex: promptIndex,
				humanTS:     timestamp,
				model:       line.Params.Update.Meta.ModelID,
			}
		}
		block, ok := readGrokContent(line.Params.Update.Content)
		if !ok {
			r.unreadable(record, "invalid Grok user content")
			return
		}
		if block.Type != "text" {
			r.exclude(record, "user attachment", firstNonEmpty(block.Type, block.MIMEType))
			return
		}
		r.current.humanText.WriteString(block.Text)
	case "agent_message_chunk":
		r.claim(record, timestamp, func(turn *grokTurn) bool {
			block, ok := readGrokContent(line.Params.Update.Content)
			if !ok || block.Type != "text" {
				return false
			}
			turn.agentText.WriteString(block.Text)
			return block.Text != ""
		})
	case "agent_thought_chunk":
		r.claim(record, timestamp, func(turn *grokTurn) bool {
			block, ok := readGrokContent(line.Params.Update.Content)
			if !ok || block.Type != "text" {
				return false
			}
			turn.thoughtText.WriteString(block.Text)
			return block.Text != ""
		})
	case "tool_call":
		r.claim(record, timestamp, func(turn *grokTurn) bool {
			update := line.Params.Update
			tool := &ToolUse{
				Name:          firstNonEmpty(update.Meta.Tool.Name, update.Kind, update.Title),
				ParamsSummary: Clip(rawText(update.RawInput), paramsBudget),
			}
			turn.tools = append(turn.tools, tool)
			if update.ToolCallID != "" {
				r.pending[update.ToolCallID] = tool
			}
			return true
		})
	case "tool_call_update":
		if r.current == nil {
			r.unreadable(record, "grok tool update arrived before any user prompt: "+
				firstNonEmpty(line.Params.Update.ToolCallID, "unnamed"))
			return
		}
		r.current.agentTS = lastInstant(r.current.agentTS, timestamp)
		tool, ok := r.pending[line.Params.Update.ToolCallID]
		if !ok {
			r.unreadable(record, "tool update has unknown toolCallId: "+line.Params.Update.ToolCallID)
			return
		}
		if grokFailedStatus(line.Params.Update.Status) {
			tool.HadError = true
			tool.ErrorMessage = Clip(grokToolOutput(line.Params.Update), errorBudget)
		}
	case "plan":
		r.claim(record, timestamp, func(turn *grokTurn) bool {
			turn.planText = grokPlanText(line.Params.Update.Entries)
			return turn.planText != ""
		})
	default:
		r.unreadable(record, "unknown Grok content update: "+
			firstNonEmpty(line.Params.Update.SessionUpdate, "unnamed"))
	}
}

// answered says whether an arriving user chunk belongs to a later turn than the
// open one. A different prompt index proves it outright. Without an index, the
// agent having already answered proves it just as well: a prompt that drew a
// reply is closed, and appending to it would fuse a whole stream into one
// exchange. Only chunks of a prompt nobody has answered yet keep joining it.
func (t *grokTurn) answered(promptIndex string) bool {
	if promptIndex != "" {
		return promptIndex != t.promptIndex
	}
	return t.blocks > 0
}

func (r *grokReader) claim(record int, timestamp string, fill func(*grokTurn) bool) {
	if r.current == nil {
		// Agent content this build does read, which no prompt in this file can
		// hold: a stream that begins mid-conversation loses turns, and that is a
		// failure to report and not machinery excluded by design.
		r.unreadable(record, "grok content update arrived before any user prompt")
		return
	}
	if fill(r.current) {
		r.current.blocks++
		r.current.agentTS = lastInstant(r.current.agentTS, timestamp)
	}
}

func (r *grokReader) flush(closed bool) {
	if r.current == nil || (r.current.blocks == 0 && !closed) {
		r.current = nil
		r.pending = map[string]*ToolUse{}
		return
	}
	var usage UsageTally
	sourceID := ""
	if r.current.promptIndex != "" {
		sourceID = "grok-prompt:" + r.current.promptIndex
	}
	exchange := Exchange{
		Number:         len(r.exchanges) + 1,
		SourceID:       sourceID,
		HumanText:      r.current.humanText.String(),
		AgentText:      r.current.agentText.String(),
		HumanTimestamp: r.current.humanTS,
		AgentTimestamp: r.current.agentTS,
		LatencyMS:      latency(r.current.humanTS, r.current.agentTS),
		Provenance:     usage.Provenance(r.current.model, "xai"),
	}
	if text := strings.TrimSpace(r.current.thoughtText.String()); text != "" {
		exchange.Thinking = append(exchange.Thinking, Thinking{Text: text, WordCount: wordCount(text)})
	}
	if text := strings.TrimSpace(r.current.planText); text != "" {
		exchange.Thinking = append(exchange.Thinking, Thinking{Text: text, WordCount: wordCount(text)})
	}
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
		Reason:   "grok " + kind + " not ingested: " + firstNonEmpty(name, "unnamed"),
		Category: "grok " + kind + " not ingested",
	})
}

func (r *grokReader) unreadable(record int, reason string) {
	r.discards = append(r.discards, Discard{Record: record, Reason: reason})
}

func readGrokContent(raw json.RawMessage) (grokContent, bool) {
	var content grokContent
	return content, len(raw) > 0 && json.Unmarshal(raw, &content) == nil
}

func grokTimestamp(value *float64) string {
	if value == nil {
		return ""
	}
	return ISOFromEpochSeconds(*value)
}

func lastInstant(current, candidate string) string {
	if candidate > current {
		return candidate
	}
	return current
}

func grokFailedStatus(status string) bool {
	return status == "failed" || status == "error"
}

func grokToolOutput(update grokUpdate) string {
	if output := rawText(update.RawOutput); output != "" {
		return output
	}
	var blocks []struct {
		Content string `json:"content"`
	}
	if json.Unmarshal(update.Content, &blocks) == nil {
		return joinBlockTexts(blocks, func(block struct {
			Content string `json:"content"`
		}) string {
			return block.Content
		}, "\n")
	}
	return rawText(update.Content)
}

func grokPlanText(entries []grokPlanEntry) string {
	parts := make([]string, 0, len(entries))
	for _, entry := range entries {
		if text := strings.TrimSpace(entry.Content); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n")
}

func grokPathIdentity(path string) (sessionID, project, workspace string) {
	if filepath.Base(path) != "updates.jsonl" {
		return "", "", ""
	}
	sessionDir := filepath.Dir(path)
	sessionID = filepath.Base(sessionDir)
	encodedWorkspace := filepath.Base(filepath.Dir(sessionDir))
	decoded, err := url.PathUnescape(encodedWorkspace)
	if err != nil || decoded == "" || decoded == encodedWorkspace {
		return sessionID, "", ""
	}
	return sessionID, filepath.Base(decoded), decoded
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

type grokSummaryView struct {
	Title string
}

func readGrokSummary(content []byte) grokSummaryView {
	if len(content) == 0 {
		return grokSummaryView{}
	}
	var summary grokSummary
	if err := json.Unmarshal(content, &summary); err != nil {
		return grokSummaryView{}
	}
	return grokSummaryView{Title: strings.TrimSpace(summary.GeneratedTitle)}
}

// ParseGrokSessionMetadata turns a Grok Build summary.json into a session
// snapshot: no exchange, and every field it does know merged over whatever the
// primary update stream already wrote.
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
