package parsers

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
)

// codexHistoryLine is the prompt history older Codex sessions kept apart from
// their metadata-only rollout. It records no answer or per-turn token usage.
type codexHistoryLine struct {
	Type      string   `json:"type"`
	SessionID string   `json:"session_id"`
	Text      string   `json:"text"`
	Timestamp *float64 `json:"ts"`
}

// prompt reports whether the record is a legacy prompt row. A record carrying a
// type is the runtime's own log line and never one of these, which is what lets
// the two shapes share a file without either reading being decided by whichever
// of them the file happens to open with.
func (l codexHistoryLine) prompt() bool {
	return l.Type == "" && strings.TrimSpace(l.Text) != "" &&
		l.Timestamp != nil && *l.Timestamp > 0
}

// historyExchange normalizes one legacy prompt. Its identity is the source's own
// words at their own instant inside their own session, so the same prompt read
// from the rollout it lived in and from `history.jsonl` lands as one row.
func historyExchange(number int, sessionID string, line codexHistoryLine) Exchange {
	timestamp := ISOFromEpochSeconds(*line.Timestamp)
	identity, _ := json.Marshal([3]string{sessionID, timestamp, line.Text})
	return Exchange{
		Number:         number,
		SourceID:       "codex-history:" + fmt.Sprintf("%x", sha256.Sum256(identity)),
		HumanText:      line.Text,
		HumanTimestamp: timestamp,
	}
}

func parseCodexHistory(content []byte, meta FileMeta) Records {
	records := Records{}
	byID := map[string]int{}
	for index, raw := range lines(content) {
		record := index + 1
		var line codexHistoryLine
		if err := json.Unmarshal([]byte(raw), &line); err != nil {
			records.Discards = append(records.Discards, Discard{
				Record: record, Reason: "invalid Codex history JSON: " + err.Error(),
				Category: "invalid Codex history JSON",
			})
			continue
		}
		if line.Type != "" {
			records.Discards = append(records.Discards, Discard{
				Record: record, ByDesign: true,
				Reason:   "codex runtime record not ingested from history: " + line.Type,
				Category: "codex runtime record not ingested from history",
			})
			continue
		}
		if line.SessionID == "" || !line.prompt() {
			records.Discards = append(records.Discards, Discard{
				Record: record, Reason: "Codex history record is not a valid prompt",
				Category: "invalid Codex history record",
			})
			continue
		}

		at, found := byID[line.SessionID]
		if !found {
			at = len(records.Sessions)
			byID[line.SessionID] = at
			records.Sessions = append(records.Sessions, Session{
				ID: line.SessionID, SourceAgent: firstNonEmpty(meta.SourceAgent, "codex"),
				Project: meta.Project, Metadata: map[string]any{}, HistoryFallback: true,
			})
		}
		session := &records.Sessions[at]
		exchange := historyExchange(len(session.Exchanges)+1, line.SessionID, line)
		session.Exchanges = append(session.Exchanges, exchange)
		timestamp := exchange.HumanTimestamp
		if session.StartedAt == "" || timestamp < session.StartedAt {
			session.StartedAt = timestamp
		}
		if timestamp > session.EndedAt {
			session.EndedAt = timestamp
		}
	}
	for i := range records.Sessions {
		session := &records.Sessions[i]
		session.DurationMinutes = minutesBetween(session.StartedAt, session.EndedAt)
	}
	return records
}
