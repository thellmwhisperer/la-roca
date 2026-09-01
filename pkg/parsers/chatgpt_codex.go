package parsers

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

type chatGPTCodexConversation struct {
	ID         string             `json:"id"`
	Title      string             `json:"title"`
	Archived   bool               `json:"archived"`
	CreateTime *float64           `json:"create_time"`
	UpdateTime *float64           `json:"update_time"`
	Turns      []chatGPTCodexTurn `json:"turns"`
}

type chatGPTCodexTurn struct {
	ID                    string             `json:"id"`
	Role                  string             `json:"role"`
	CustomInstructions    string             `json:"custom_instructions"`
	PreviousTurnID        string             `json:"previous_turn_id"`
	Branch                string             `json:"branch"`
	BranchName            string             `json:"branch_name"`
	ExternalPullRequestID string             `json:"external_pull_request_id"`
	PullRequestStatus     string             `json:"pull_request_status"`
	TurnStatus            string             `json:"turn_status"`
	CreateTime            *float64           `json:"create_time"`
	InputItems            []chatGPTCodexItem `json:"input_items"`
	OutputItems           []chatGPTCodexItem `json:"output_items"`
}

type chatGPTCodexItem struct {
	Type    string                `json:"type"`
	Role    string                `json:"role"`
	Content []chatGPTCodexContent `json:"content"`
}

type chatGPTCodexContent struct {
	ContentType string `json:"content_type"`
	Text        string `json:"text"`
}

func detectChatGPTCodex(file File) bool {
	object := firstObject(file.Content)
	return file.Meta.SourceAgent == "codex-cloud" && has(object, "id") && has(object, "turns")
}

// ParseChatGPTCodex streams the top-level export array and keeps one
// conversation in memory at a time.
func ParseChatGPTCodex(reader io.Reader, meta FileMeta) (Records, error) {
	decoder := json.NewDecoder(reader)
	if err := openJSONArray(decoder, "ChatGPT Codex conversations"); err != nil {
		return Records{}, err
	}
	records := Records{}
	for conversation := 1; decoder.More(); conversation++ {
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return Records{}, fmt.Errorf("decode ChatGPT Codex conversation %d: %w", conversation, err)
		}
		var payload chatGPTCodexConversation
		if err := json.Unmarshal(raw, &payload); err != nil {
			records.Discards = append(records.Discards, Discard{
				Record:   conversation,
				Reason:   fmt.Sprintf("ChatGPT Codex conversation %d is unreadable: %v", conversation, err),
				Category: "ChatGPT Codex conversation is unreadable",
			})
			continue
		}
		parsed := parseChatGPTCodexConversation(payload, conversation, meta)
		records.Sessions = append(records.Sessions, parsed.Sessions...)
		records.Discards = append(records.Discards, parsed.Discards...)
	}
	if err := closeJSONArray(decoder, "ChatGPT Codex conversations"); err != nil {
		return Records{}, err
	}
	return records, nil
}

func parseChatGPTCodexConversation(payload chatGPTCodexConversation, record int, meta FileMeta) Records {
	payload.ID = strings.TrimSpace(payload.ID)
	if payload.ID == "" {
		return Records{Discards: []Discard{{
			Record: record, Reason: "conversation has no id",
		}}}
	}
	users := map[string]chatGPTCodexTurn{}
	for _, turn := range payload.Turns {
		id := strings.TrimSpace(turn.ID)
		if strings.ToLower(strings.TrimSpace(turn.Role)) == "user" && id != "" {
			users[id] = turn
		}
	}
	exchanges := make([]Exchange, 0, len(payload.Turns)/2)
	discards := []Discard{}
	for _, turn := range payload.Turns {
		if strings.ToLower(strings.TrimSpace(turn.Role)) != "assistant" {
			continue
		}
		previousTurnID := strings.TrimSpace(turn.PreviousTurnID)
		if previousTurnID == "" {
			continue
		}
		human, ok := users[previousTurnID]
		if !ok {
			continue
		}
		humanText := chatGPTCodexText(human.InputItems)
		agentText := chatGPTCodexText(turn.OutputItems)
		if humanText == "" {
			discards = append(discards, Discard{
				Record: record, Reason: fmt.Sprintf("empty ChatGPT Codex user turn %s", turn.PreviousTurnID),
				Category: "empty ChatGPT Codex user turn", ByDesign: true,
			})
			continue
		}
		if agentText == "" {
			discards = append(discards, Discard{
				Record: record, Reason: fmt.Sprintf("empty ChatGPT Codex assistant turn %s", strings.TrimSpace(turn.ID)),
				Category: "empty ChatGPT Codex assistant turn", ByDesign: true,
			})
			continue
		}
		exchange := Exchange{
			Number:         len(exchanges) + 1,
			SourceID:       strings.TrimSpace(turn.ID),
			HumanText:      humanText,
			AgentText:      agentText,
			HumanTimestamp: chatGPTInstant(human.CreateTime),
			AgentTimestamp: chatGPTInstant(turn.CreateTime),
			Signal:         chatGPTCodexSignal(turn),
		}
		exchange.LatencyMS = latency(exchange.HumanTimestamp, exchange.AgentTimestamp)
		exchange.Fingerprint = chatGPTCodexExchangeFingerprint(exchange)
		exchanges = append(exchanges, exchange)
	}
	started, ended := chatGPTInstant(payload.CreateTime), chatGPTInstant(payload.UpdateTime)
	if started == "" {
		for _, exchange := range exchanges {
			if exchange.HumanTimestamp != "" && (started == "" || exchange.HumanTimestamp < started) {
				started = exchange.HumanTimestamp
			}
		}
	}
	if ended == "" {
		for _, exchange := range exchanges {
			if exchange.AgentTimestamp != "" && exchange.AgentTimestamp > ended {
				ended = exchange.AgentTimestamp
			}
		}
	}
	metadata := WithoutEmpty(map[string]any{
		"archived": payload.Archived,
	})
	return Records{Sessions: []Session{{
		ID: payload.ID, SourceAgent: firstNonEmpty(meta.SourceAgent, "codex-cloud"),
		StartedAt: started, EndedAt: ended, DurationMinutes: minutesBetween(started, ended),
		Title: strings.TrimSpace(payload.Title), Metadata: metadata,
		ExchangeKeyScope: "codex_cloud", Exchanges: exchanges,
	}}, Discards: discards}
}

func chatGPTCodexText(items []chatGPTCodexItem) string {
	parts := make([]string, 0, len(items))
	for _, item := range items {
		if strings.ToLower(strings.TrimSpace(item.Type)) != "message" {
			continue
		}
		for _, content := range item.Content {
			if strings.ToLower(strings.TrimSpace(content.ContentType)) != "text" {
				continue
			}
			if text := strings.TrimSpace(content.Text); text != "" {
				parts = append(parts, text)
			}
		}
	}
	return strings.Join(parts, "\n")
}

// chatGPTCodexSignal counts what the export stated about one assistant turn.
// The count is how two readings of the same exchange are ordered so a poorer
// snapshot never takes the richer one's provenance.
func chatGPTCodexSignal(turn chatGPTCodexTurn) *int {
	stated := 0
	for _, present := range []bool{
		strings.TrimSpace(turn.Branch) != "",
		strings.TrimSpace(turn.BranchName) != "",
		strings.TrimSpace(turn.ExternalPullRequestID) != "",
		strings.TrimSpace(turn.PreviousTurnID) != "",
		strings.TrimSpace(turn.PullRequestStatus) != "",
		strings.TrimSpace(turn.TurnStatus) != "",
		len(turn.OutputItems) > 0,
		turn.CreateTime != nil,
	} {
		if present {
			stated++
		}
	}
	return &stated
}

func chatGPTCodexExchangeFingerprint(exchange Exchange) string {
	projection := struct {
		HumanText      string     `json:"human_text"`
		AgentText      string     `json:"agent_text"`
		HumanTimestamp string     `json:"human_timestamp"`
		AgentTimestamp string     `json:"agent_timestamp"`
		Provenance     Provenance `json:"provenance"`
	}{exchange.HumanText, exchange.AgentText, exchange.HumanTimestamp,
		exchange.AgentTimestamp, exchange.Provenance}
	encoded, _ := json.Marshal(projection)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}
