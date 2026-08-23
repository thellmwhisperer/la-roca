package parsers

import (
	"encoding/json"
	"path/filepath"
	"strings"
)

// Subagent transcript shapes. The kind is read off the file name, which is what
// the runtime encodes it in.
const (
	subagentPlain   = "subagent"
	subagentAside   = "aside_question"
	subagentCompact = "compact"
)

// ClassifySubagent names what a subagent transcript is. A compact is not a
// conversation: it is the summary the runtime wrote when it folded one, so it
// lands as a thinking block and not as an exchange.
func ClassifySubagent(path string) string {
	name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	switch {
	case strings.Contains(name, subagentAside):
		return subagentAside
	case strings.Contains(name, "compact"):
		return subagentCompact
	}
	return subagentPlain
}

// ParseSubagent turns a subagent transcript into one session of its own, whose
// parent is the session that spawned it.
//
// The identity is the transcript's, not the path's: `agentId` is the child and
// `sessionId` is the parent. A subagent filed under its parent's id is a
// subagent whose work is credited to somebody else.
func ParseSubagent(content []byte, meta FileMeta) (Records, error) {
	kind := ClassifySubagent(meta.Path)

	type message struct {
		Type      string         `json:"type"`
		Timestamp string         `json:"timestamp"`
		SessionID string         `json:"sessionId"`
		AgentID   string         `json:"agentId"`
		UUID      string         `json:"uuid"`
		Message   *claudeMessage `json:"message"`
		record    int
	}

	var entries []message
	var discards []Discard
	for index, raw := range lines(content) {
		var entry message
		if err := json.Unmarshal([]byte(raw), &entry); err != nil {
			discards = append(discards, Discard{Record: index + 1,
				Reason: "invalid JSON: " + err.Error(), Category: "invalid JSON"})
			continue
		}
		entry.record = index + 1
		entries = append(entries, entry)
	}
	if len(entries) == 0 {
		return Records{Discards: discards}, nil
	}

	parentID := entries[0].SessionID
	agentID := entries[0].AgentID
	sessionID := firstNonEmpty(agentID, parentID)
	if sessionID == "" {
		discards = append(discards, Discard{Record: entries[0].record, Reason: "subagent record declares no session identity"})
		return Records{Discards: discards}, nil
	}

	session := Session{
		ID:                           sessionID,
		SourceAgent:                  firstNonEmpty(meta.SourceAgent, "claude-code"),
		Project:                      meta.Project,
		Metadata:                     map[string]any{"source_type": kind},
		ExchangeNumbersAuthoritative: true,
	}
	if agentID != "" && parentID != "" {
		session.ParentID = parentID
	}

	if kind == subagentCompact {
		for _, entry := range entries {
			if entry.Type != "system" {
				discards = append(discards, Discard{Record: entry.record,
					Reason:   "unsupported compact record: " + entry.Type,
					Category: "unsupported compact record"})
				continue
			}
			text, blocks := decodeContent(entry.Message)
			if text == "" {
				text = joinText(blocks)
			}
			if text == "" {
				discards = append(discards, Discard{Record: entry.record, Reason: "compact record has no readable content"})
				continue
			}
			session.Thinking = append(session.Thinking, Thinking{
				Depth:     subagentCompact,
				Text:      text,
				WordCount: wordCount(text),
			})
		}
		if len(session.Thinking) == 0 {
			return Records{Discards: discards}, nil
		}
		return Records{Sessions: []Session{session}, Discards: discards}, nil
	}

	// The transcript writes the agent's answer in chunks: consecutive assistant
	// lines are one answer, and the human line that follows closes it.
	type side struct {
		text, timestamp string
		record          int
		// model and usage are the provenance the assistant chunks declared; the
		// human side of a transcript declares none.
		model string
		usage UsageTally
	}
	type turn struct {
		human side
		agent *side
	}
	var current *turn
	deferred := 0
	flush := func() {
		if current == nil {
			return
		}
		if current.agent == nil {
			discards = append(discards, Discard{Record: current.human.record,
				Reason: "human turn with no answer to pair it with"})
			current = nil
			return
		}
		number := meta.ExchangeNumberOffset + len(session.Exchanges) + 1
		session.Exchanges = append(session.Exchanges, Exchange{
			Number:         number,
			HumanText:      current.human.text,
			AgentText:      current.agent.text,
			HumanTimestamp: current.human.timestamp,
			AgentTimestamp: current.agent.timestamp,
			LatencyMS:      latency(current.human.timestamp, current.agent.timestamp),
			Provenance:     current.agent.usage.Provenance(current.agent.model, ""),
		})
		current = nil
	}
	for _, entry := range entries {
		text, blocks := decodeContent(entry.Message)
		if text == "" {
			text = joinText(blocks)
		}
		if text == "" {
			flush()
			discards = append(discards, Discard{Record: entry.record, Reason: "subagent record has no readable content"})
			continue
		}
		switch entry.Type {
		case "user":
			flush()
			current = &turn{human: side{text: text, timestamp: validInstant(entry.Timestamp), record: entry.record}}
		case "assistant":
			if current == nil {
				deferred++
				discards = append(discards, Discard{Record: entry.record,
					Reason: "answer with no human turn to pair it with"})
				continue
			}
			if current.agent == nil {
				current.agent = &side{text: text, timestamp: validInstant(entry.Timestamp), record: entry.record}
			} else {
				current.agent.text += "\n" + text
				if timestamp := validInstant(entry.Timestamp); timestamp != "" {
					current.agent.timestamp = timestamp
				}
			}
			answer := current.agent
			if answer.model == "" && entry.Message != nil {
				answer.model = entry.Message.Model
			}
			claimClaudeUsage(&answer.usage, entry.Message)
		default:
			flush()
			discards = append(discards, Discard{Record: entry.record,
				Reason:   "unsupported subagent record: " + entry.Type,
				Category: "unsupported subagent record"})
		}
	}

	if current != nil && current.agent == nil {
		deferred++
	}
	flush()
	if len(session.Exchanges) == 0 {
		return Records{Discards: discards, Deferred: deferred}, nil
	}
	session.StartedAt, session.EndedAt, session.DurationMinutes = span(session.Exchanges)
	return Records{Sessions: []Session{session}, Discards: discards, Deferred: deferred}, nil
}

// LooksLikeSubagent decides whether a file really is a subagent transcript
// before any parser reads it. A Claude line carries a transcript `type`, a
// `message` object and the identity the parser depends on, so a foreign
// transcript under a shared root is rejected instead of misread.
//
// Three answers, not two: true for a confirmed shape, false for a foreign one,
// and unknown for a file with nothing parseable in it, which stays the ordinary
// skip and not a contract failure.
func LooksLikeSubagent(content []byte) (confirmed bool, known bool) {
	type probe struct {
		Type      string          `json:"type"`
		Message   json.RawMessage `json:"message"`
		SessionID string          `json:"sessionId"`
		AgentID   string          `json:"agentId"`
	}
	parseable := false
	for i, raw := range lines(content) {
		if i >= shapeProbeLines {
			break
		}
		var entry probe
		if err := json.Unmarshal([]byte(raw), &entry); err != nil {
			continue
		}
		parseable = true
		isTranscript := entry.Type == "user" || entry.Type == "assistant" || entry.Type == "system"
		hasMessage := len(entry.Message) > 0 && strings.HasPrefix(strings.TrimSpace(string(entry.Message)), "{")
		if isTranscript && hasMessage && (entry.AgentID != "" || entry.SessionID != "") {
			return true, true
		}
	}
	return false, parseable
}

// shapeProbeLines is how much of a file the shape probe reads. Confirming a
// large transcript has to stay cheap.
const shapeProbeLines = 50

func joinText(blocks []claudeBlock) string {
	parts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		if block.Type == "text" && block.Text != "" {
			parts = append(parts, block.Text)
		}
	}
	return strings.Join(parts, "\n")
}
