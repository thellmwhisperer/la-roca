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

type claudeWebConversation struct {
	UUID         string             `json:"uuid"`
	Name         string             `json:"name"`
	Summary      string             `json:"summary"`
	CreatedAt    string             `json:"created_at"`
	UpdatedAt    string             `json:"updated_at"`
	ChatMessages []claudeWebMessage `json:"chat_messages"`
}

type claudeWebMessage struct {
	UUID              string          `json:"uuid"`
	Text              string          `json:"text"`
	Content           json.RawMessage `json:"content"`
	Sender            string          `json:"sender"`
	CreatedAt         string          `json:"created_at"`
	UpdatedAt         string          `json:"updated_at"`
	Attachments       []claudeWebFile `json:"attachments"`
	Files             []claudeWebFile `json:"files"`
	ParentMessageUUID string          `json:"parent_message_uuid"`
}

type claudeWebFile struct {
	FileName string `json:"file_name"`
	Filename string `json:"filename"`
	Name     string `json:"name"`
}

// ParseClaudeWebConversations streams the top-level export array. Only one
// conversation tree is retained at a time, so a large history does not require
// a second copy of the whole conversations.json file in memory.
func ParseClaudeWebConversations(reader io.Reader, meta FileMeta) (Records, error) {
	decoder := json.NewDecoder(reader)
	if err := openJSONArray(decoder, "Claude web conversations"); err != nil {
		return Records{}, err
	}
	records := Records{}
	record, conversation := 0, 0
	for decoder.More() {
		conversation++
		var payload claudeWebConversation
		if err := decoder.Decode(&payload); err != nil {
			return Records{}, fmt.Errorf("decode Claude web conversation %d: %w", conversation, err)
		}
		parsed := parseClaudeWebConversation(payload, record)
		record += len(payload.ChatMessages)
		records.Sessions = append(records.Sessions, parsed.Sessions...)
		records.Discards = append(records.Discards, parsed.Discards...)
	}
	if err := closeJSONArray(decoder, "Claude web conversations"); err != nil {
		return Records{}, err
	}
	return records, nil
}

func parseClaudeWebConversation(payload claudeWebConversation, recordBase int) Records {
	if strings.TrimSpace(payload.UUID) == "" {
		return Records{Discards: []Discard{{
			Record: recordBase + 1, Reason: "conversation has no uuid",
		}}}
	}
	byID, parents, reasons := claudeWebMessageGraph(payload.ChatMessages)
	paired := make([]claudeWebMessage, 0, len(payload.ChatMessages)/2)
	for i, message := range payload.ChatMessages {
		parent := parents[i]
		if reasons[i] != "" || message.Sender != "assistant" || parent < 0 ||
			payload.ChatMessages[parent].Sender != "human" {
			continue
		}
		message.ParentMessageUUID = payload.ChatMessages[parent].UUID
		paired = append(paired, message)
	}
	sort.SliceStable(paired, func(i, j int) bool {
		return claudeWebMessageLess(paired[i], paired[j])
	})

	exchanges := make([]Exchange, 0, len(paired))
	exchangeMetadata := map[string]any{}
	for _, assistant := range paired {
		parent := payload.ChatMessages[byID[assistant.ParentMessageUUID]-1]
		metadata := claudeWebExchangeMetadata(parent, assistant)
		if len(metadata) > 0 {
			exchangeMetadata[assistant.UUID] = metadata
		}
		exchange := Exchange{
			Number:         len(exchanges) + 1,
			SourceID:       assistant.UUID,
			HumanText:      claudeWebText(parent),
			AgentText:      claudeWebText(assistant),
			HumanTimestamp: validInstant(parent.CreatedAt),
			AgentTimestamp: validInstant(assistant.CreatedAt),
		}
		exchange.LatencyMS = latency(exchange.HumanTimestamp, exchange.AgentTimestamp)
		exchange.Fingerprint = claudeWebExchangeFingerprint(exchange, metadata)
		exchanges = append(exchanges, exchange)
	}

	metadata := WithoutEmpty(map[string]any{
		"name":       strings.TrimSpace(payload.Name),
		"summary":    strings.TrimSpace(payload.Summary),
		"created_at": validInstant(payload.CreatedAt),
		"updated_at": validInstant(payload.UpdatedAt),
	})
	if len(exchangeMetadata) > 0 {
		metadata["claude_web"] = map[string]any{"exchange_metadata": exchangeMetadata}
	}
	discards := make([]Discard, 0, len(reasons))
	for i := range payload.ChatMessages {
		if reasons[i] != "" {
			discards = append(discards, Discard{
				Record: recordBase + i + 1, Reason: reasons[i],
			})
		}
	}
	started, ended := validInstant(payload.CreatedAt), validInstant(payload.UpdatedAt)
	return Records{Sessions: []Session{{
		ID: strings.TrimSpace(payload.UUID), SourceAgent: "claude-web",
		StartedAt: started, EndedAt: ended, DurationMinutes: minutesBetween(started, ended),
		Title: strings.TrimSpace(payload.Name), Metadata: metadata, Snapshot: true,
		SnapshotUpdatedAt: ended,
		ExchangeKeyScope:  "claude_web", Exchanges: exchanges,
	}}, Discards: discards}
}

func claudeWebMessageGraph(messages []claudeWebMessage) (map[string]int, []int, map[int]string) {
	byID := make(map[string]int, len(messages))
	reasons := map[int]string{}
	for i := range messages {
		message := &messages[i]
		message.UUID = strings.TrimSpace(message.UUID)
		message.ParentMessageUUID = strings.TrimSpace(message.ParentMessageUUID)
		switch {
		case message.UUID == "":
			reasons[i] = "message has no uuid"
		case byID[message.UUID] != 0:
			reasons[i] = fmt.Sprintf("message uuid %s is duplicated", message.UUID)
		default:
			byID[message.UUID] = i + 1
		}
		if reasons[i] == "" && message.Sender != "human" && message.Sender != "assistant" {
			reasons[i] = fmt.Sprintf("message %s has unsupported sender %q", message.UUID, message.Sender)
		}
		if reasons[i] == "" && claudeWebText(*message) == "" {
			reasons[i] = fmt.Sprintf("%s message %s has no text", message.Sender, message.UUID)
		}
	}
	parents := claudeWebSurvivingParents(messages, byID, reasons)
	claudeWebDiscardCycles(messages, parents, reasons)
	return byID, claudeWebSurvivingParents(messages, byID, reasons), reasons
}

func claudeWebSurvivingParents(messages []claudeWebMessage, byID map[string]int,
	reasons map[int]string) []int {
	parents := make([]int, len(messages))
	for i := range parents {
		parents[i] = -1
		seen := map[int]bool{i: true}
		parentID := messages[i].ParentMessageUUID
		for parentID != "" {
			position, found := byID[parentID]
			if !found || seen[position-1] {
				break
			}
			parent := position - 1
			seen[parent] = true
			if reasons[parent] == "" {
				parents[i] = parent
				break
			}
			parentID = messages[parent].ParentMessageUUID
		}
	}
	return parents
}

func claudeWebDiscardCycles(messages []claudeWebMessage, parents []int, reasons map[int]string) {
	state := make([]uint8, len(messages))
	var visit func(int)
	visit = func(index int) {
		state[index] = 1
		parent := parents[index]
		if parent >= 0 && reasons[parent] == "" {
			switch state[parent] {
			case 0:
				visit(parent)
			case 1:
				for member := index; ; member = parents[member] {
					reasons[member] = fmt.Sprintf(
						"message %s has a cyclic parent chain", messages[member].UUID)
					if member == parent {
						break
					}
				}
			}
		}
		state[index] = 2
	}
	for i := range messages {
		if reasons[i] == "" && state[i] == 0 {
			visit(i)
		}
	}
}

// ParseClaudeWebMemories reads each exported memory independently. UUID is the
// preferred identity; stable list position supports older export shapes.
func ParseClaudeWebMemories(reader io.Reader, meta FileMeta) (Records, error) {
	decoder := json.NewDecoder(reader)
	if err := openJSONArray(decoder, "Claude web memories"); err != nil {
		return Records{}, err
	}
	records := Records{}
	for index := 1; decoder.More(); index++ {
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return Records{}, fmt.Errorf("decode Claude web memory %d: %w", index, err)
		}
		memory, reason := parseClaudeWebMemory(raw, meta, index)
		if reason != "" {
			records.Discards = append(records.Discards, Discard{Record: index, Reason: reason})
			continue
		}
		records.Memories = append(records.Memories, memory)
	}
	if err := closeJSONArray(decoder, "Claude web memories"); err != nil {
		return Records{}, err
	}
	return records, nil
}

func parseClaudeWebMemory(raw json.RawMessage, meta FileMeta, index int) (Memory, string) {
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		text = strings.TrimSpace(text)
	}
	object := map[string]json.RawMessage{}
	if text == "" {
		if err := json.Unmarshal(raw, &object); err != nil {
			return Memory{}, fmt.Sprintf("memory %d is neither text nor an object", index)
		}
		text = firstJSONString(object, "memory", "content", "text")
	}
	if text == "" {
		return Memory{}, fmt.Sprintf("memory %d has no text", index)
	}
	identity := firstJSONString(object, "uuid", "id")
	path := "memory-uuid:" + identity
	if identity == "" {
		path = fmt.Sprintf("%s#memory=entry-%d", meta.Path, index)
	}
	created := validInstant(firstJSONString(object, "created_at"))
	updated := validInstant(firstJSONString(object, "updated_at"))
	metadata := WithoutEmpty(map[string]any{
		"_cron_source": "claude-web", "file_path": path, "file_name": meta.FileName,
		"export_file_path": meta.Path, "memory_uuid": firstJSONString(object, "uuid", "id"),
		"updated_at": updated,
	})
	return Memory{
		Layer: "user", Content: text, Origin: "cron", SourceAgent: "claude-web",
		Metadata: metadata, Source: "claude-web", FilePath: path,
		CreatedAt: firstNonEmpty(created, updated),
	}, ""
}

func firstJSONString(object map[string]json.RawMessage, keys ...string) string {
	for _, key := range keys {
		var value string
		if err := json.Unmarshal(object[key], &value); err == nil &&
			strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func claudeWebText(message claudeWebMessage) string {
	return firstNonEmpty(strings.TrimSpace(message.Text), piContentText(message.Content))
}

func claudeWebExchangeMetadata(human, assistant claudeWebMessage) map[string]any {
	metadata := map[string]any{}
	for key, names := range map[string][]string{
		"human_attachments":     claudeWebFileNames(human.Attachments),
		"human_files":           claudeWebFileNames(human.Files),
		"assistant_attachments": claudeWebFileNames(assistant.Attachments),
		"assistant_files":       claudeWebFileNames(assistant.Files),
	} {
		if len(names) > 0 {
			metadata[key] = names
		}
	}
	return metadata
}

func claudeWebFileNames(files []claudeWebFile) []string {
	names := make([]string, 0, len(files))
	for _, file := range files {
		if name := firstNonEmpty(file.FileName, file.Filename, file.Name); name != "" {
			names = append(names, name)
		}
	}
	return names
}

func claudeWebMessageLess(left, right claudeWebMessage) bool {
	leftTime, leftOK := parseISO(left.CreatedAt)
	rightTime, rightOK := parseISO(right.CreatedAt)
	if leftOK && rightOK && !leftTime.Equal(rightTime) {
		return leftTime.Before(rightTime)
	}
	if leftOK != rightOK {
		return leftOK
	}
	return left.UUID < right.UUID
}

func claudeWebExchangeFingerprint(exchange Exchange, metadata map[string]any) string {
	projection := struct {
		HumanText      string         `json:"human_text"`
		AgentText      string         `json:"agent_text"`
		HumanTimestamp string         `json:"human_timestamp"`
		AgentTimestamp string         `json:"agent_timestamp"`
		Metadata       map[string]any `json:"metadata,omitempty"`
	}{exchange.HumanText, exchange.AgentText, exchange.HumanTimestamp,
		exchange.AgentTimestamp, metadata}
	encoded, _ := json.Marshal(projection)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func validInstant(value string) string {
	value = strings.TrimSpace(value)
	if _, ok := parseISO(value); ok {
		return value
	}
	return ""
}

func ClaudeWebTimestampBefore(left, right string) bool {
	leftTime, leftOK := parseISO(strings.TrimSpace(left))
	rightTime, rightOK := parseISO(strings.TrimSpace(right))
	return leftOK && rightOK && leftTime.Before(rightTime)
}

func openJSONArray(decoder *json.Decoder, label string) error {
	token, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("read %s: %w", label, err)
	}
	if delimiter, ok := token.(json.Delim); !ok || delimiter != '[' {
		return fmt.Errorf("read %s: top-level value is not an array", label)
	}
	return nil
}

func closeJSONArray(decoder *json.Decoder, label string) error {
	token, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("finish %s: %w", label, err)
	}
	if delimiter, ok := token.(json.Delim); !ok || delimiter != ']' {
		return fmt.Errorf("finish %s: top-level array is not closed", label)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("finish %s: content follows the top-level array", label)
		}
		return fmt.Errorf("finish %s: %w", label, err)
	}
	return nil
}
