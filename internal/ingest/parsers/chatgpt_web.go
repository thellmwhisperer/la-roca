package parsers

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
)

type chatGPTConversation struct {
	ConversationID   string                     `json:"conversation_id"`
	Title            string                     `json:"title"`
	CreateTime       *float64                   `json:"create_time"`
	UpdateTime       *float64                   `json:"update_time"`
	DefaultModelSlug string                     `json:"default_model_slug"`
	CurrentNode      string                     `json:"current_node"`
	Mapping          map[string]json.RawMessage `json:"mapping"`
}

type chatGPTNode struct {
	key      string
	ID       string
	Parent   string
	Children []string
	Message  *chatGPTMessage
	failure  chatGPTDiscard
}

type chatGPTMessage struct {
	Author struct {
		Role string `json:"role"`
	} `json:"author"`
	Content struct {
		Parts []json.RawMessage `json:"parts"`
	} `json:"content"`
	CreateTime *float64 `json:"create_time"`
	// The fields from UpdateTime to Channel are read for one reason: they are what
	// a legacy snapshot states about an answer and a mid-2026 shard does not, and
	// counting them is how two readings of the same exchange are ordered.
	UpdateTime *float64        `json:"update_time"`
	Status     string          `json:"status"`
	EndTurn    *bool           `json:"end_turn"`
	Channel    string          `json:"channel"`
	Metadata   chatGPTMetadata `json:"metadata"`
}

type chatGPTMetadata struct {
	ModelSlug      string `json:"model_slug"`
	RequestID      string `json:"request_id"`
	TurnExchangeID string `json:"turn_exchange_id"`
	Hidden         bool   `json:"is_visually_hidden_from_conversation"`
}

type chatGPTDiscard struct {
	reason   string
	category string
	byDesign bool
}

// ParseChatGPTWebConversations streams the top-level export array and keeps one
// mapping tree in memory at a time.
func ParseChatGPTWebConversations(reader io.Reader, _ FileMeta) (Records, error) {
	decoder := json.NewDecoder(reader)
	if err := openJSONArray(decoder, "ChatGPT web conversations"); err != nil {
		return Records{}, err
	}
	records := Records{}
	recordBase := 0
	for conversation := 1; decoder.More(); conversation++ {
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return Records{}, fmt.Errorf("decode ChatGPT web conversation %d: %w", conversation, err)
		}
		var payload chatGPTConversation
		if err := json.Unmarshal(raw, &payload); err != nil {
			records.Discards = append(records.Discards, Discard{
				Record:   recordBase + 1,
				Reason:   fmt.Sprintf("ChatGPT conversation %d is unreadable: %v", conversation, err),
				Category: "ChatGPT conversation is unreadable",
			})
			recordBase++
			continue
		}
		parsed, nodes := parseChatGPTConversation(payload, recordBase)
		recordBase += nodes
		records.Sessions = append(records.Sessions, parsed.Sessions...)
		records.Discards = append(records.Discards, parsed.Discards...)
	}
	if err := closeJSONArray(decoder, "ChatGPT web conversations"); err != nil {
		return Records{}, err
	}
	return records, nil
}

func parseChatGPTConversation(payload chatGPTConversation, recordBase int) (Records, int) {
	payload.ConversationID = strings.TrimSpace(payload.ConversationID)
	if payload.ConversationID == "" {
		return Records{Discards: []Discard{{
			Record: recordBase + 1, Reason: "conversation has no conversation_id",
		}}}, len(payload.Mapping)
	}
	nodes := orderedChatGPTNodes(payload.Mapping)
	parents, reasons := chatGPTMessageGraph(nodes)
	exchanges := make([]Exchange, 0, len(nodes)/2)
	for index, node := range nodes {
		parent := parents[index]
		if reasons[index].reason != "" || chatGPTRole(node) != "assistant" ||
			parent < 0 || chatGPTRole(nodes[parent]) != "user" {
			continue
		}
		human := nodes[parent]
		model := strings.TrimSpace(node.Message.Metadata.ModelSlug)
		if model == "" {
			model = strings.TrimSpace(payload.DefaultModelSlug)
		}
		var usage UsageTally
		exchange := Exchange{
			Number:         len(exchanges) + 1,
			SourceID:       chatGPTNodeID(node),
			HumanText:      chatGPTText(human),
			AgentText:      chatGPTText(node),
			HumanTimestamp: chatGPTInstant(human.Message.CreateTime),
			AgentTimestamp: chatGPTInstant(node.Message.CreateTime),
			Provenance:     usage.Provenance(model, "openai"),
			Signal:         chatGPTSignal(node),
		}
		exchange.LatencyMS = latency(exchange.HumanTimestamp, exchange.AgentTimestamp)
		exchange.Fingerprint = chatGPTExchangeFingerprint(exchange)
		exchanges = append(exchanges, exchange)
	}

	discards := make([]Discard, 0, len(reasons))
	for index := range nodes {
		reason := reasons[index]
		if reason.reason == "" {
			continue
		}
		discards = append(discards, Discard{
			Record: recordBase + index + 1, Reason: reason.reason,
			Category: reason.category, ByDesign: reason.byDesign,
		})
	}
	started, ended := chatGPTInstant(payload.CreateTime), chatGPTInstant(payload.UpdateTime)
	metadata := WithoutEmpty(map[string]any{
		"created_at":         started,
		"updated_at":         ended,
		"default_model_slug": strings.TrimSpace(payload.DefaultModelSlug),
		"current_node":       strings.TrimSpace(payload.CurrentNode),
	})
	return Records{Sessions: []Session{{
		ID: payload.ConversationID, SourceAgent: "chatgpt-web",
		StartedAt: started, EndedAt: ended, DurationMinutes: minutesBetween(started, ended),
		Title: strings.TrimSpace(payload.Title), Metadata: metadata,
		Snapshot: true, SnapshotUpdatedAt: ended,
		ExchangeKeyScope: "chatgpt_web", Exchanges: exchanges,
	}}, Discards: discards}, len(nodes)
}

func orderedChatGPTNodes(mapping map[string]json.RawMessage) []chatGPTNode {
	keys := make([]string, 0, len(mapping))
	for key := range mapping {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	decoded := make(map[string]chatGPTNode, len(mapping))
	for _, key := range keys {
		decoded[key] = decodeChatGPTNode(key, mapping[key])
	}
	roots := make([]string, 0, len(keys))
	for _, key := range keys {
		parent := strings.TrimSpace(decoded[key].Parent)
		if parent == "" {
			roots = append(roots, key)
		} else if _, found := decoded[parent]; !found {
			roots = append(roots, key)
		}
	}
	ordered := make([]chatGPTNode, 0, len(mapping))
	visited := map[string]bool{}
	var walk func(string)
	walk = func(key string) {
		if visited[key] {
			return
		}
		node, found := decoded[key]
		if !found {
			return
		}
		visited[key] = true
		ordered = append(ordered, node)
		for _, child := range node.Children {
			walk(strings.TrimSpace(child))
		}
	}
	for _, root := range roots {
		walk(root)
	}
	for _, key := range keys {
		walk(key)
	}
	return ordered
}

func decodeChatGPTNode(key string, payload json.RawMessage) chatGPTNode {
	node := chatGPTNode{key: key}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fields); err != nil {
		node.failure = chatGPTDiscard{
			reason:   fmt.Sprintf("conversation node %s is unreadable: %v", key, err),
			category: "ChatGPT conversation node is unreadable",
		}
		return node
	}
	decodeField := func(name string, destination any) {
		raw, found := fields[name]
		if !found {
			return
		}
		if err := json.Unmarshal(raw, destination); err != nil && node.failure.reason == "" {
			node.failure = chatGPTDiscard{
				reason:   fmt.Sprintf("conversation node %s has unreadable %s: %v", key, name, err),
				category: fmt.Sprintf("ChatGPT conversation node has unreadable %s", name),
			}
		}
	}
	decodeField("id", &node.ID)
	decodeField("parent", &node.Parent)
	decodeField("children", &node.Children)
	rawMessage, found := fields["message"]
	if !found {
		return node
	}
	if err := json.Unmarshal(rawMessage, &node.Message); err != nil && node.failure.reason == "" {
		node.failure = chatGPTDiscard{
			reason:   fmt.Sprintf("message %s is unreadable: %v", chatGPTNodeID(node), err),
			category: "ChatGPT message is unreadable",
		}
	}
	return node
}

func chatGPTMessageGraph(nodes []chatGPTNode) ([]int, map[int]chatGPTDiscard) {
	byID := make(map[string]int, len(nodes)*2)
	graph := make([]parentGraphNode, len(nodes))
	reasons := map[int]chatGPTDiscard{}
	sourceIDs := map[string]int{}
	for index, node := range nodes {
		id := chatGPTNodeID(node)
		graph[index] = parentGraphNode{id: node.key, parent: strings.TrimSpace(node.Parent)}
		byID[node.key] = index + 1
		if declared := strings.TrimSpace(node.ID); declared != "" {
			if _, found := byID[declared]; !found {
				byID[declared] = index + 1
			}
		}
		if previous, duplicated := sourceIDs[id]; duplicated {
			reasons[index] = chatGPTDiscard{
				reason:   fmt.Sprintf("message id %s is duplicated (first seen at record %d)", id, previous),
				category: "message id is duplicated",
			}
		} else {
			sourceIDs[id] = index + 1
		}
		if reasons[index].reason == "" {
			reasons[index] = chatGPTNodeReason(node)
		}
	}
	discarded := func(index int) bool { return reasons[index].reason != "" }
	parents := survivingParents(graph, byID, discarded)
	discardParentCycles(graph, parents, discarded, func(index int) {
		reasons[index] = chatGPTDiscard{
			reason:   fmt.Sprintf("message %s has a cyclic parent chain", chatGPTNodeID(nodes[index])),
			category: "message has a cyclic parent chain",
		}
	})
	return survivingParents(graph, byID, discarded), reasons
}

func chatGPTNodeReason(node chatGPTNode) chatGPTDiscard {
	id := chatGPTNodeID(node)
	if node.failure.reason != "" {
		return node.failure
	}
	if node.Message == nil {
		return chatGPTDiscard{reason: "empty ChatGPT conversation node",
			category: "empty ChatGPT conversation node", byDesign: true}
	}
	if node.Message.Metadata.Hidden {
		return chatGPTDiscard{reason: fmt.Sprintf("hidden ChatGPT message %s", id),
			category: "hidden ChatGPT message", byDesign: true}
	}
	role := chatGPTRole(node)
	switch role {
	case "system", "tool":
		return chatGPTDiscard{reason: fmt.Sprintf("ChatGPT %s message %s", role, id),
			category: fmt.Sprintf("ChatGPT %s message", role), byDesign: true}
	case "user", "assistant":
		if chatGPTText(node) == "" {
			return chatGPTDiscard{reason: fmt.Sprintf("empty ChatGPT %s message %s", role, id),
				category: fmt.Sprintf("empty ChatGPT %s message", role), byDesign: true}
		}
		return chatGPTDiscard{}
	default:
		return chatGPTDiscard{
			reason:   fmt.Sprintf("message %s has unsupported author role %q", id, role),
			category: "message has unsupported author role",
		}
	}
}

// chatGPTSignal counts what the export stated about one answer. A legacy
// snapshot states more per message than a mid-2026 shard does, and this count is
// what the writer compares so the poorer reading of the same exchange never
// takes the richer one's place, whichever run each arrived in.
func chatGPTSignal(node chatGPTNode) int {
	if node.Message == nil {
		return 0
	}
	message := node.Message
	stated := 0
	for _, present := range []bool{
		message.UpdateTime != nil,
		message.EndTurn != nil,
		strings.TrimSpace(message.Status) != "",
		strings.TrimSpace(message.Channel) != "",
		strings.TrimSpace(message.Metadata.ModelSlug) != "",
		strings.TrimSpace(message.Metadata.RequestID) != "",
		strings.TrimSpace(message.Metadata.TurnExchangeID) != "",
	} {
		if present {
			stated++
		}
	}
	return stated
}

func chatGPTNodeID(node chatGPTNode) string {
	if id := strings.TrimSpace(node.ID); id != "" {
		return id
	}
	return strings.TrimSpace(node.key)
}

func chatGPTRole(node chatGPTNode) string {
	if node.Message == nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(node.Message.Author.Role))
}

func chatGPTText(node chatGPTNode) string {
	if node.Message == nil {
		return ""
	}
	parts := make([]string, 0, len(node.Message.Content.Parts))
	for _, raw := range node.Message.Content.Parts {
		var text string
		if json.Unmarshal(raw, &text) != nil {
			var object struct {
				Text string `json:"text"`
			}
			if json.Unmarshal(raw, &object) == nil {
				text = object.Text
			}
		}
		if text = strings.TrimSpace(text); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n")
}

func chatGPTInstant(value *float64) string {
	if value == nil {
		return ""
	}
	return ISOFromEpochSeconds(*value)
}

func chatGPTExchangeFingerprint(exchange Exchange) string {
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
