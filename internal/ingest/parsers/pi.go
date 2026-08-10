package parsers

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"unicode"
)

// isAbsolutePath accepts either path syntax. Pi refuses to project a session
// whose working directory is relative, because a relative one names no project,
// and a Windows one is still absolute.
func isAbsolutePath(value string) bool {
	if strings.HasPrefix(value, "/") || strings.HasPrefix(value, `\`) {
		return true
	}
	return len(value) >= 3 && unicode.IsLetter(rune(value[0])) && value[1] == ':' &&
		(value[2] == '\\' || value[2] == '/')
}

// piVersion is the only session format this build reads. A version it does not
// know is refused by name instead of guessed at: a tree read with the wrong
// rules produces exchanges nobody ever had.
const piVersion = 3

// piTerminalReasons are the stop reasons that close a turn. Anything else means
// the agent is still answering, and an unfinished turn is not an exchange.
var piTerminalReasons = map[string]bool{
	"stop": true, "length": true, "error": true, "aborted": true,
}

type piEntry struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	// ParentID is a pointer so a declared null can be told from an absent key:
	// the root declares null, and an entry with no key at all is malformed.
	ParentID  *string    `json:"parentId"`
	Timestamp any        `json:"timestamp"`
	Message   *piMessage `json:"message"`

	hasParent bool
	record    int
}

func (e *piEntry) UnmarshalJSON(data []byte) error {
	type plain piEntry
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(data, &probe); err != nil {
		return err
	}
	var decoded plain
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*e = piEntry(decoded)
	_, e.hasParent = probe["parentId"]
	return nil
}

type piMessage struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	Timestamp  any             `json:"timestamp"`
	StopReason string          `json:"stopReason"`
	ToolCallID string          `json:"toolCallId"`
	IsError    bool            `json:"isError"`
}

type piBlock struct {
	Type      string `json:"type"`
	Text      string `json:"text"`
	Thinking  string `json:"thinking"`
	Redacted  bool   `json:"redacted"`
	Encrypted bool   `json:"encrypted"`
	ID        string `json:"id"`
	Name      string `json:"name"`
}

type piHeader struct {
	Type          string `json:"type"`
	Version       int    `json:"version"`
	ID            string `json:"id"`
	Cwd           string `json:"cwd"`
	Timestamp     any    `json:"timestamp"`
	ParentSession string `json:"parentSession"`
}

// ParsePiSession projects a Pi v3 tree onto its active branch.
//
// Pi keeps the whole conversation tree, including the branches the user walked
// away from. Only the branch that ends at the last entry is the conversation
// that happened, and only its complete turns are exchanges: a turn whose tool
// calls have no results yet is deferred, not ingested half-written.
func ParsePiSession(content []byte, meta FileMeta) (Records, error) {
	rows, deferred, discards, err := piRows(content)
	if err != nil {
		return Records{}, err
	}

	var header piHeader
	if err := json.Unmarshal([]byte(rows[0]), &header); err != nil || header.Type != "session" {
		return Records{}, fmt.Errorf("the first complete row is not a Pi session header")
	}
	if header.Version != piVersion {
		return Records{}, fmt.Errorf("unsupported Pi session version: %d", header.Version)
	}
	if header.ID == "" {
		return Records{}, fmt.Errorf("the Pi session declares no id")
	}
	if !isAbsolutePath(header.Cwd) {
		return Records{}, fmt.Errorf("the Pi session's cwd is not absolute")
	}

	session := Session{
		ID:          "pi:" + header.ID,
		SourceAgent: "pi",
		Project:     lastSegment(header.Cwd),
		Metadata: WithoutEmpty(map[string]any{
			"source":            "pi",
			"native_session_id": header.ID,
			"source_path":       meta.Path,
			"format_version":    piVersion,
			"cwd":               header.Cwd,
			"parent_session":    header.ParentSession,
		}),
		Snapshot: true,
	}
	if header.ParentSession != "" {
		session.ParentID = header.ParentSession
	}

	if len(rows) == 1 {
		session.Metadata["source_high_water_at"] = isoFromAnyInstant(header.Timestamp)
		return Records{Sessions: []Session{session}, Deferred: deferred, Discards: discards}, nil
	}

	entries, err := piEntries(rows[1:])
	if err != nil {
		return Records{}, err
	}
	active, err := piActivePath(entries)
	if err != nil {
		return Records{}, err
	}
	exchanges, stillOpen, exchangeDiscards := piExchanges(active)
	deferred += stillOpen
	discards = append(discards, exchangeDiscards...)

	session.Exchanges = exchanges
	session.Metadata["source_high_water_id"] = active[len(active)-1].ID
	if at := isoFromAnyInstant(active[len(active)-1].Timestamp); at != "" {
		session.Metadata["source_high_water_at"] = at
	}
	session.StartedAt, session.EndedAt, session.DurationMinutes = span(exchanges)
	return Records{Sessions: []Session{session}, Deferred: deferred, Discards: discards}, nil
}

// piRows splits the file, holding back a live tail. A final line with no newline
// behind it is a line Pi is still writing: reading it would ingest half a
// message, so it is deferred until the next run.
func piRows(content []byte) ([]string, int, []Discard, error) {
	text := string(content)
	if strings.TrimSpace(text) == "" {
		return nil, 0, nil, fmt.Errorf("empty Pi session")
	}
	physical := strings.SplitAfter(text, "\n")
	var rows []string
	deferred := 0
	var discards []Discard
	for index, raw := range physical {
		final := index == len(physical)-1
		if strings.TrimSpace(raw) == "" {
			continue
		}
		if final && !strings.HasSuffix(raw, "\n") {
			deferred++
			continue
		}
		line := strings.TrimSpace(raw)
		if !json.Valid([]byte(line)) {
			if final {
				deferred++
				continue
			}
			discards = append(discards, Discard{Record: index + 1, Reason: "invalid JSON"})
			continue
		}
		rows = append(rows, line)
	}
	if len(rows) == 0 {
		return nil, deferred, discards, fmt.Errorf("the Pi session has no complete header")
	}
	return rows, deferred, discards, nil
}

func piEntries(rows []string) ([]*piEntry, error) {
	entries := make([]*piEntry, 0, len(rows))
	for index, row := range rows {
		var entry piEntry
		if err := json.Unmarshal([]byte(row), &entry); err != nil {
			return nil, fmt.Errorf("a Pi entry is not an object: %w", err)
		}
		entry.record = index + 2
		entries = append(entries, &entry)
	}
	return entries, nil
}

// piActivePath is the branch that ends at the last entry, root first. Its
// validations reject two roots, a missing parent, a cycle, or a last entry that
// is not a leaf because the file was written out of order.
func piActivePath(entries []*piEntry) ([]*piEntry, error) {
	byID := make(map[string]*piEntry, len(entries))
	children := map[string]int{}
	roots := 0
	for _, entry := range entries {
		if entry.ID == "" {
			return nil, fmt.Errorf("a Pi entry declares no id")
		}
		if !entry.hasParent {
			return nil, fmt.Errorf("the parent id is missing for %s", entry.ID)
		}
		if _, seen := byID[entry.ID]; seen {
			return nil, fmt.Errorf("duplicate Pi entry id: %s", entry.ID)
		}
		byID[entry.ID] = entry
		if entry.ParentID == nil {
			roots++
			continue
		}
		if *entry.ParentID == "" {
			return nil, fmt.Errorf("invalid parent id for %s", entry.ID)
		}
		children[*entry.ParentID]++
	}
	if roots != 1 {
		return nil, fmt.Errorf("ambiguous Pi tree roots")
	}
	for _, entry := range entries {
		if entry.ParentID == nil {
			continue
		}
		if _, ok := byID[*entry.ParentID]; !ok {
			return nil, fmt.Errorf("missing parent for %s", entry.ID)
		}
	}

	leaf := entries[len(entries)-1]
	if children[leaf.ID] > 0 {
		return nil, fmt.Errorf("the final Pi entry is not an active leaf")
	}

	var reversed []*piEntry
	walked := map[string]bool{}
	for cursor := leaf; cursor != nil; {
		if walked[cursor.ID] {
			return nil, fmt.Errorf("cycle at %s", cursor.ID)
		}
		walked[cursor.ID] = true
		reversed = append(reversed, cursor)
		if cursor.ParentID == nil {
			break
		}
		cursor = byID[*cursor.ParentID]
	}
	slices.Reverse(reversed)
	return reversed, nil
}

// piPending is one turn being assembled along the active branch.
type piPending struct {
	userID     string
	userRecord int
	humanText  string
	humanTS    string
	compacted  bool
	agentText  []string
	thinking   []string
	calls      []string
	callNames  map[string]string
	results    map[string]bool
	errors     map[string]bool
	invalid    bool
	terminal   *piEntry
}

func piExchanges(active []*piEntry) ([]Exchange, int, []Discard) {
	var exchanges []Exchange
	var pending *piPending
	compacted := false
	deferred := 0
	number := 0
	var discards []Discard

	close := func() {
		if pending == nil {
			return
		}
		exchange, ok := piProject(pending, &number)
		if !ok {
			deferred++
			pending = nil
			return
		}
		exchanges = append(exchanges, exchange)
		pending = nil
	}

	for _, entry := range active {
		if entry.Type == "compaction" {
			compacted = true
			continue
		}
		if entry.Type != "message" || entry.Message == nil {
			discards = append(discards, Discard{Record: entry.record, Reason: "unsupported or incomplete Pi entry"})
			continue
		}
		message := entry.Message
		switch message.Role {
		case "user":
			close()
			text := piContentText(message.Content)
			if text == "" {
				discards = append(discards, Discard{Record: entry.record, Reason: "user message has no readable content"})
				continue
			}
			pending = &piPending{
				userID:     entry.ID,
				userRecord: entry.record,
				humanText:  text,
				humanTS:    isoFromAnyInstant(firstInstant(message.Timestamp, entry.Timestamp)),
				compacted:  compacted,
				callNames:  map[string]string{},
				results:    map[string]bool{},
				errors:     map[string]bool{},
			}
		case "assistant":
			if pending == nil {
				discards = append(discards, Discard{Record: entry.record, Reason: "assistant message has no user turn"})
				continue
			}
			discards = append(discards, piConsumeAssistant(pending, message, entry.record)...)
			if piTerminalReasons[message.StopReason] {
				pending.terminal = entry
			} else {
				pending.terminal = nil
			}
		case "toolResult":
			if pending == nil {
				discards = append(discards, Discard{Record: entry.record, Reason: "tool result has no user turn"})
				continue
			}
			id := message.ToolCallID
			if _, declared := pending.callNames[id]; !declared || pending.results[id] {
				pending.invalid = true
				discards = append(discards, Discard{Record: entry.record, Reason: "tool result has no unique matching call"})
				continue
			}
			pending.results[id] = true
			pending.errors[id] = message.IsError
		default:
			discards = append(discards, Discard{Record: entry.record, Reason: "unsupported message role: " + message.Role})
		}
	}
	close()
	return exchanges, deferred, discards
}

func piConsumeAssistant(pending *piPending, message *piMessage, record int) []Discard {
	var text string
	if err := json.Unmarshal(message.Content, &text); err == nil {
		if trimmed := strings.TrimSpace(text); trimmed != "" {
			pending.agentText = append(pending.agentText, trimmed)
		}
		return nil
	}
	var blocks []piBlock
	if err := json.Unmarshal(message.Content, &blocks); err != nil {
		return []Discard{{Record: record, Reason: "assistant content is neither text nor blocks"}}
	}
	var discards []Discard
	for _, block := range blocks {
		switch block.Type {
		case "text":
			if trimmed := strings.TrimSpace(block.Text); trimmed != "" {
				pending.agentText = append(pending.agentText, trimmed)
			}
		case "thinking":
			// A redacted or encrypted block is not readable text, and storing its
			// envelope would fill the corpus with noise nobody can query. A block
			// whose text is blank is the same noise by the same argument, and the
			// text blocks above already refuse it.
			if block.Redacted || block.Encrypted {
				discards = append(discards, Discard{Record: record, Reason: "thinking block is redacted or encrypted"})
				continue
			}
			trimmed := strings.TrimSpace(block.Thinking)
			if trimmed == "" {
				discards = append(discards, Discard{Record: record, Reason: "thinking block has no readable text"})
				continue
			}
			pending.thinking = append(pending.thinking, trimmed)
		case "toolCall":
			if block.ID == "" {
				pending.invalid = true
				discards = append(discards, Discard{Record: record, Reason: "tool call declares no id"})
				continue
			}
			if _, seen := pending.callNames[block.ID]; seen {
				pending.invalid = true
				discards = append(discards, Discard{Record: record, Reason: "duplicate tool call id"})
				continue
			}
			pending.callNames[block.ID] = block.Name
			pending.calls = append(pending.calls, block.ID)
		default:
			discards = append(discards, Discard{Record: record, Reason: "unsupported assistant block: " + block.Type})
		}
	}
	return discards
}

// piProject turns a finished turn into an exchange, or refuses to. A turn with a
// call that never got its result is not incomplete data to patch: it is a turn
// still in flight.
func piProject(pending *piPending, number *int) (Exchange, bool) {
	if pending.terminal == nil || pending.invalid {
		return Exchange{}, false
	}
	if len(pending.calls) != len(pending.results) {
		return Exchange{}, false
	}
	*number++
	terminal := pending.terminal
	agentTS := isoFromAnyInstant(firstInstant(terminal.Message.Timestamp, terminal.Timestamp))
	exchange := Exchange{
		Number:            *number,
		SourceID:          pending.userID,
		Fingerprint:       pending.fingerprint(),
		IsAfterCompaction: pending.compacted,
		HumanText:         pending.humanText,
		AgentText:         strings.Join(pending.agentText, "\n"),
		HumanTimestamp:    pending.humanTS,
		AgentTimestamp:    agentTS,
		LatencyMS:         latency(pending.humanTS, agentTS),
	}
	for _, text := range pending.thinking {
		exchange.Thinking = append(exchange.Thinking, Thinking{
			Text:              text,
			WordCount:         wordCount(text),
			Position:          float64(*number),
			IsAfterCompaction: pending.compacted,
		})
	}
	for _, id := range pending.calls {
		tool := ToolUse{Name: pending.callNames[id], HadError: pending.errors[id]}
		if tool.HadError {
			tool.ErrorMessage = "tool_error"
		}
		exchange.Tools = append(exchange.Tools, tool)
	}
	return exchange, true
}

// fingerprint hashes a canonical projection of the turn, so re-reading an
// unchanged file recognizes the exchange that already landed. Only stable
// identity for the same turn is required.
func (p *piPending) fingerprint() string {
	projection := struct {
		User     string     `json:"user"`
		Human    string     `json:"human"`
		Agent    []string   `json:"agent"`
		Thinking []string   `json:"thinking"`
		Tools    [][]string `json:"tools"`
	}{User: p.userID, Human: p.humanText, Agent: p.agentText, Thinking: p.thinking}
	for _, id := range p.calls {
		verdict := "ok"
		if p.errors[id] {
			verdict = "error"
		}
		projection.Tools = append(projection.Tools, []string{id, p.callNames[id], verdict})
	}
	encoded, err := json.Marshal(projection)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

// piContentText is the readable text of a message's content, string or block
// list.
func piContentText(content json.RawMessage) string {
	if len(content) == 0 {
		return ""
	}
	var text string
	if err := json.Unmarshal(content, &text); err == nil {
		return strings.TrimSpace(text)
	}
	var blocks []piBlock
	if err := json.Unmarshal(content, &blocks); err != nil {
		return ""
	}
	parts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		if block.Type == "text" && strings.TrimSpace(block.Text) != "" {
			parts = append(parts, strings.TrimSpace(block.Text))
		}
	}
	return strings.Join(parts, "\n")
}

// lastSegment is the working directory's final component, in either syntax.
func lastSegment(value string) string {
	trimmed := strings.TrimRight(value, `/\`)
	if index := strings.LastIndexAny(trimmed, `/\`); index >= 0 {
		return trimmed[index+1:]
	}
	return trimmed
}

func firstInstant(values ...any) any {
	for _, value := range values {
		switch typed := value.(type) {
		case nil:
		case string:
			if typed != "" {
				return typed
			}
		default:
			return typed
		}
	}
	return nil
}
