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

type claudeWebDiscard struct {
	reason   string
	category string
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
		if reasons[i].reason != "" || message.Sender != "assistant" || parent < 0 ||
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
		if reasons[i].reason != "" {
			discards = append(discards, Discard{
				Record: recordBase + i + 1, Reason: reasons[i].reason,
				Category: reasons[i].category,
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

func claudeWebMessageGraph(messages []claudeWebMessage) (map[string]int, []int, map[int]claudeWebDiscard) {
	byID := make(map[string]int, len(messages))
	nodes := make([]parentGraphNode, len(messages))
	reasons := map[int]claudeWebDiscard{}
	for i := range messages {
		message := &messages[i]
		message.UUID = strings.TrimSpace(message.UUID)
		message.ParentMessageUUID = strings.TrimSpace(message.ParentMessageUUID)
		nodes[i] = parentGraphNode{id: message.UUID, parent: message.ParentMessageUUID}
		switch {
		case message.UUID == "":
			reasons[i] = claudeWebDiscard{reason: "message has no uuid"}
		case byID[message.UUID] != 0:
			reasons[i] = claudeWebDiscard{
				reason:   fmt.Sprintf("message uuid %s is duplicated", message.UUID),
				category: "message uuid is duplicated",
			}
		default:
			byID[message.UUID] = i + 1
		}
		if reasons[i].reason == "" && message.Sender != "human" && message.Sender != "assistant" {
			reasons[i] = claudeWebDiscard{
				reason:   fmt.Sprintf("message %s has unsupported sender %q", message.UUID, message.Sender),
				category: "message has unsupported sender",
			}
		}
		if reasons[i].reason == "" && claudeWebText(*message) == "" {
			reasons[i] = claudeWebDiscard{
				reason:   fmt.Sprintf("%s message %s has no text", message.Sender, message.UUID),
				category: message.Sender + " message has no text",
			}
		}
	}
	discarded := func(index int) bool { return reasons[index].reason != "" }
	parents := survivingParents(nodes, byID, discarded)
	discardParentCycles(nodes, parents, discarded, func(index int) {
		reasons[index] = claudeWebDiscard{
			reason:   fmt.Sprintf("message %s has a cyclic parent chain", messages[index].UUID),
			category: "message has a cyclic parent chain",
		}
	})
	return byID, survivingParents(nodes, byID, discarded), reasons
}

// ParseClaudeWebMemories reads each exported memory independently. UUID is the
// preferred identity; stable list position supports older export shapes. A
// current-format account object expands into conversations_memory,
// project_memories, and memory_files.
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
		memories, reason := parseClaudeWebMemoryEntry(raw, meta, index)
		if reason != "" {
			category := "memory has no text"
			if strings.Contains(reason, "neither text nor an object") {
				category = "memory is neither text nor an object"
			}
			records.Discards = append(records.Discards, Discard{
				Record: index, Reason: reason, Category: category,
			})
			continue
		}
		records.Memories = append(records.Memories, memories...)
	}
	if err := closeJSONArray(decoder, "Claude web memories"); err != nil {
		return Records{}, err
	}
	return records, nil
}

func parseClaudeWebMemoryEntry(raw json.RawMessage, meta FileMeta, index int) ([]Memory, string) {
	object := map[string]json.RawMessage{}
	if err := json.Unmarshal(raw, &object); err == nil &&
		has(object, "conversations_memory", "project_memories", "memory_files") {
		return parseClaudeWebAccountMemories(object, meta, index)
	}
	memory, reason := parseClaudeWebMemory(raw, meta, index)
	if reason != "" {
		return nil, reason
	}
	return []Memory{memory}, ""
}

func parseClaudeWebAccountMemories(object map[string]json.RawMessage, meta FileMeta, index int) ([]Memory, string) {
	account := firstJSONString(object, "account_uuid")
	var memories []Memory
	if text := firstJSONString(object, "conversations_memory"); text != "" {
		path := "memory-account:" + account
		if account == "" {
			path = fmt.Sprintf("%s#memory=conversations", meta.Path)
		}
		memories = append(memories, claudeWebUserMemory(text, "", path, meta, map[string]any{
			"memory_kind":  "conversations_memory",
			"account_uuid": account,
		}))
	}
	projects := map[string]string{}
	if raw, ok := object["project_memories"]; ok {
		_ = json.Unmarshal(raw, &projects)
	}
	keys := make([]string, 0, len(projects))
	for key := range projects {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, project := range keys {
		text := strings.TrimSpace(projects[project])
		project = strings.TrimSpace(project)
		if text == "" || project == "" {
			continue
		}
		memories = append(memories, claudeWebUserMemory(text, project, "memory-project:"+project, meta, map[string]any{
			"memory_kind":  "project_memory",
			"account_uuid": account,
			"project_uuid": project,
		}))
	}
	var files []claudeWebMemoryFile
	if raw, ok := object["memory_files"]; ok {
		_ = json.Unmarshal(raw, &files)
	}
	for i, file := range files {
		text := strings.TrimSpace(file.Content)
		if text == "" {
			continue
		}
		sourcePath := strings.TrimSpace(file.Path)
		path := "memory-file:" + sourcePath
		if sourcePath == "" {
			path = fmt.Sprintf("%s#memory=file-%d", meta.Path, i+1)
		}
		memories = append(memories, claudeWebUserMemory(text, "", path, meta, map[string]any{
			"memory_kind":      "memory_file",
			"account_uuid":     account,
			"memory_file_path": sourcePath,
			"updated_at":       validInstant(file.UpdatedAt),
		}))
	}
	if len(memories) == 0 {
		return nil, fmt.Sprintf("memory %d has no text", index)
	}
	return memories, ""
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
	return claudeWebUserMemory(text, "", path, meta, map[string]any{
		"memory_uuid": identity,
		"created_at":  validInstant(firstJSONString(object, "created_at")),
		"updated_at":  validInstant(firstJSONString(object, "updated_at")),
	}), ""
}

func claudeWebUserMemory(content, project, path string, meta FileMeta, extra map[string]any) Memory {
	created, _ := extra["created_at"].(string)
	updated, _ := extra["updated_at"].(string)
	metadata := WithoutEmpty(map[string]any{
		"_cron_source":     "claude-web",
		"file_path":        path,
		"file_name":        meta.FileName,
		"export_file_path": meta.Path,
		"memory_uuid":      extra["memory_uuid"],
		"updated_at":       updated,
		"memory_kind":      extra["memory_kind"],
		"account_uuid":     extra["account_uuid"],
		"project_uuid":     extra["project_uuid"],
		"memory_file_path": extra["memory_file_path"],
	})
	return Memory{
		Layer: "user", Content: content, Origin: "cron", SourceAgent: "claude-web",
		Project: project, Metadata: metadata, Source: "claude-web", FilePath: path,
		CreatedAt: firstNonEmpty(created, updated),
	}
}

type claudeWebMemoryFile struct {
	Content   string `json:"content"`
	Path      string `json:"path"`
	UpdatedAt string `json:"updated_at"`
}

type claudeWebProjectDoc struct {
	UUID      string `json:"uuid"`
	Filename  string `json:"filename"`
	FileName  string `json:"file_name"`
	Content   string `json:"content"`
	CreatedAt string `json:"created_at"`
}

// ParseClaudeWebProject turns one official project entity and its docs into
// store rows. Ordinary conversations are not joined here: the export has no key.
func ParseClaudeWebProject(content []byte, meta FileMeta) (Records, error) {
	var payload struct {
		UUID           string                `json:"uuid"`
		Name           string                `json:"name"`
		Description    string                `json:"description"`
		PromptTemplate string                `json:"prompt_template"`
		IsPrivate      *bool                 `json:"is_private"`
		IsStarter      *bool                 `json:"is_starter_project"`
		CreatedAt      string                `json:"created_at"`
		UpdatedAt      string                `json:"updated_at"`
		Creator        json.RawMessage       `json:"creator"`
		Docs           []claudeWebProjectDoc `json:"docs"`
	}
	if err := json.Unmarshal(content, &payload); err != nil {
		return Records{}, fmt.Errorf("decode Claude web project: %w", err)
	}
	uuid := strings.TrimSpace(payload.UUID)
	if uuid == "" {
		return Records{Discards: []Discard{{Record: 1, Reason: "project has no uuid"}}}, nil
	}
	body := firstNonEmpty(strings.TrimSpace(payload.Description),
		strings.TrimSpace(payload.PromptTemplate), strings.TrimSpace(payload.Name))
	if body == "" {
		return Records{Discards: []Discard{{Record: 1, Reason: "project has no text"}}}, nil
	}
	path := "project-entity:" + uuid
	created, updated := validInstant(payload.CreatedAt), validInstant(payload.UpdatedAt)
	metadata := WithoutEmpty(map[string]any{
		"_cron_source":     "claude-web",
		"file_path":        path,
		"file_name":        meta.FileName,
		"export_file_path": meta.Path,
		"memory_kind":      "project_entity",
		"uuid":             uuid,
		"name":             strings.TrimSpace(payload.Name),
		"description":      strings.TrimSpace(payload.Description),
		"prompt_template":  strings.TrimSpace(payload.PromptTemplate),
		"created_at":       created,
		"updated_at":       updated,
	})
	if payload.IsPrivate != nil {
		metadata["is_private"] = *payload.IsPrivate
	}
	if payload.IsStarter != nil {
		metadata["is_starter_project"] = *payload.IsStarter
	}
	if len(payload.Creator) > 0 && string(payload.Creator) != "null" {
		var creator any
		if json.Unmarshal(payload.Creator, &creator) == nil {
			metadata["creator"] = creator
		}
	}
	memories := []Memory{{
		Layer: "project", Content: body, Origin: "cron", SourceAgent: "claude-web",
		Project: uuid, Metadata: metadata, Source: "claude-web", FilePath: path,
		CreatedAt: firstNonEmpty(created, updated),
	}}
	for i, doc := range payload.Docs {
		text := strings.TrimSpace(doc.Content)
		if text == "" {
			continue
		}
		docID := strings.TrimSpace(doc.UUID)
		docPath := "project-doc:" + firstNonEmpty(docID, fmt.Sprintf("%s#%d", uuid, i+1))
		memories = append(memories, Memory{
			Layer: "project", Content: text, Origin: "cron", SourceAgent: "claude-web",
			Project: uuid, Source: "claude-web", FilePath: docPath,
			CreatedAt: validInstant(doc.CreatedAt),
			Metadata: WithoutEmpty(map[string]any{
				"_cron_source":     "claude-web",
				"file_path":        docPath,
				"file_name":        meta.FileName,
				"export_file_path": meta.Path,
				"memory_kind":      "project_doc",
				"uuid":             docID,
				"filename":         firstNonEmpty(strings.TrimSpace(doc.Filename), strings.TrimSpace(doc.FileName)),
				"created_at":       validInstant(doc.CreatedAt),
				"project_uuid":     uuid,
			}),
		})
	}
	return Records{Memories: memories}, nil
}

type claudeWebDesignMessage struct {
	UUID      string          `json:"uuid"`
	Role      string          `json:"role"`
	CreatedAt string          `json:"created_at"`
	Content   json.RawMessage `json:"content"`
}

// ParseClaudeWebDesignChat keeps the narrow project {uuid,name} relation the
// official export actually states. Ordinary conversations.json items have none.
func ParseClaudeWebDesignChat(content []byte, meta FileMeta) (Records, error) {
	var payload struct {
		UUID      string `json:"uuid"`
		Title     string `json:"title"`
		CreatedAt string `json:"created_at"`
		UpdatedAt string `json:"updated_at"`
		Project   struct {
			UUID string `json:"uuid"`
			Name string `json:"name"`
		} `json:"project"`
		Messages []claudeWebDesignMessage `json:"messages"`
	}
	if err := json.Unmarshal(content, &payload); err != nil {
		return Records{}, fmt.Errorf("decode Claude web design chat: %w", err)
	}
	id := strings.TrimSpace(payload.UUID)
	if id == "" {
		return Records{Discards: []Discard{{Record: 1, Reason: "design chat has no uuid"}}}, nil
	}
	exchanges := make([]Exchange, 0, len(payload.Messages)/2)
	for i, message := range payload.Messages {
		if claudeWebDesignRole(message) != "assistant" || i == 0 ||
			claudeWebDesignRole(payload.Messages[i-1]) != "user" {
			continue
		}
		human := payload.Messages[i-1]
		exchange := Exchange{
			Number:         len(exchanges) + 1,
			SourceID:       strings.TrimSpace(message.UUID),
			HumanText:      claudeWebDesignText(human.Content),
			AgentText:      claudeWebDesignText(message.Content),
			HumanTimestamp: validInstant(human.CreatedAt),
			AgentTimestamp: validInstant(message.CreatedAt),
		}
		exchange.LatencyMS = latency(exchange.HumanTimestamp, exchange.AgentTimestamp)
		exchange.Fingerprint = claudeWebExchangeFingerprint(exchange, nil)
		exchanges = append(exchanges, exchange)
	}
	started, ended := validInstant(payload.CreatedAt), validInstant(payload.UpdatedAt)
	project := strings.TrimSpace(payload.Project.UUID)
	return Records{Sessions: []Session{{
		ID: id, SourceAgent: "claude-web", Project: project,
		StartedAt: started, EndedAt: ended, DurationMinutes: minutesBetween(started, ended),
		Title: strings.TrimSpace(payload.Title), Snapshot: true, SnapshotUpdatedAt: ended,
		ExchangeKeyScope: "claude_web", Exchanges: exchanges,
		Metadata: WithoutEmpty(map[string]any{
			"created_at":   started,
			"updated_at":   ended,
			"project_uuid": project,
			"project_name": strings.TrimSpace(payload.Project.Name),
		}),
	}}}, nil
}

func claudeWebDesignRole(message claudeWebDesignMessage) string {
	return strings.ToLower(strings.TrimSpace(message.Role))
}

func claudeWebDesignText(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return strings.TrimSpace(text)
	}
	var object struct {
		Content string `json:"content"`
	}
	if json.Unmarshal(raw, &object) == nil {
		return strings.TrimSpace(object.Content)
	}
	return ""
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
