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
		if line.Type != "" || line.SessionID == "" || strings.TrimSpace(line.Text) == "" ||
			line.Timestamp == nil || *line.Timestamp <= 0 {
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
		timestamp := ISOFromEpochSeconds(*line.Timestamp)
		identity, _ := json.Marshal([3]string{line.SessionID, timestamp, line.Text})
		session.Exchanges = append(session.Exchanges, Exchange{
			Number:         len(session.Exchanges) + 1,
			SourceID:       "codex-history:" + fmt.Sprintf("%x", sha256.Sum256(identity)),
			HumanText:      line.Text,
			HumanTimestamp: timestamp,
		})
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
