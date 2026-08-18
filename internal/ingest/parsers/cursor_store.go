package parsers

import (
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// cursorStoreParser reads the agent-home store.db era: one SQLite file per
// session under ~/.cursor/chats/<workspace-hash>/<session-uuid>/.
type cursorStoreParser struct{}

func (cursorStoreParser) Detect(file File) bool {
	db, ok := cursorOpenNamedSQLite(file, "store.db")
	if !ok {
		return false
	}
	defer db.close()
	hasBlobs, err := cursorTableExists(db.db, "blobs")
	return err == nil && hasBlobs
}

func (cursorStoreParser) Parse(file File) (Records, error) {
	db, err := cursorSnapshotDB(file.Content)
	if err != nil {
		return Records{}, fmt.Errorf("open Cursor store snapshot: %w", err)
	}
	defer db.close()
	ok, err := cursorTableExists(db.db, "blobs")
	if err != nil {
		return Records{}, err
	}
	if !ok {
		return Records{}, fmt.Errorf("Cursor snapshot is not an agent store")
	}
	return cursorStoreRecords(db.db, file.Meta)
}

type cursorStoreMeta struct {
	AgentID           string `json:"agentId"`
	LatestRootBlobID  string `json:"latestRootBlobId"`
	Name              string `json:"name"`
	Mode              string `json:"mode"`
	ApprovalMode      string `json:"approvalMode"`
	CreatedAt         int64  `json:"createdAt"`
	LastUsedModel     string `json:"lastUsedModel"`
	BlobEncryptionKey string `json:"blobEncryptionKey"`
	SubagentInfo      *struct {
		ParentAgentID string `json:"parentAgentId"`
	} `json:"subagentInfo"`
}

type cursorStoreSidecar struct {
	Title         string `json:"title"`
	Cwd           string `json:"cwd"`
	CreatedAtMs   int64  `json:"createdAtMs"`
	UpdatedAtMs   int64  `json:"updatedAtMs"`
	SchemaVersion int    `json:"schemaVersion"`
}

type cursorStoreMessage struct {
	Role    string          `json:"role"`
	ID      string          `json:"id"`
	Content json.RawMessage `json:"content"`
}

type cursorStorePart struct {
	Type       string          `json:"type"`
	Text       string          `json:"text"`
	ToolName   string          `json:"toolName"`
	ToolCallID string          `json:"toolCallId"`
	Args       json.RawMessage `json:"args"`
	Result     json.RawMessage `json:"result"`
}

type cursorStoreList struct {
	id   string
	kids []string
	ts   int64
}

func cursorStoreRecords(db *sql.DB, meta FileMeta) (Records, error) {
	blobs, err := cursorStoreBlobs(db)
	if err != nil {
		return Records{}, err
	}
	storeMeta, err := cursorStoreSessionMeta(db)
	if err != nil {
		return Records{}, err
	}
	sidecar := cursorStoreReadSidecar(meta.Sidecar)
	ordered, discards := cursorStoreOrderedMessages(blobs, storeMeta.LatestRootBlobID)
	source := firstNonEmpty(meta.SourceAgent, "cursor")
	sessionID := firstNonEmpty(storeMeta.AgentID, cursorStorePathSession(meta.Path), meta.SessionID)
	session := Session{
		ID:               "cursor:" + strings.TrimPrefix(sessionID, "cursor:"),
		SourceAgent:      source,
		Project:          firstNonEmpty(cursorProjectBase(sidecar.Cwd), meta.Project),
		Title:            firstNonEmpty(strings.TrimSpace(storeMeta.Name), strings.TrimSpace(sidecar.Title)),
		StartedAt:        cursorStoreTimestamp(storeMeta.CreatedAt, sidecar.CreatedAtMs),
		EndedAt:          cursorStoreTimestamp(sidecar.UpdatedAtMs, 0),
		Snapshot:         true,
		ExchangeKeyScope: cursorExchangeScope,
		Metadata: WithoutEmpty(map[string]any{
			"native_agent_id": storeMeta.AgentID,
			"mode":            storeMeta.Mode,
			"approval_mode":   storeMeta.ApprovalMode,
		}),
	}
	if sessionID == "" {
		session.ID = ""
	}
	if storeMeta.SubagentInfo != nil && storeMeta.SubagentInfo.ParentAgentID != "" {
		session.ParentID = "cursor:" + storeMeta.SubagentInfo.ParentAgentID
	}

	var current *cursorTurn
	turnNumber := 0
	closeCurrent := func() {
		if current == nil {
			return
		}
		if current.hasAnswer() || strings.TrimSpace(current.human) != "" {
			if current.model == "" && storeMeta.LastUsedModel != "" && current.hasAnswer() {
				current.model = storeMeta.LastUsedModel
				current.signal++
			}
			if current.hasAnswer() {
				session.Exchanges = append(session.Exchanges, current.exchange())
			} else {
				discards = append(discards, Discard{Reason: "Cursor turn has no assistant output",
					Category: "Cursor turn has no assistant output"})
			}
		}
		current = nil
	}
	for _, item := range ordered {
		switch item.msg.Role {
		case "system":
			discards = append(discards, cursorExcluded(item.record,
				"Cursor system prompt is not conversation content"))
		case "user":
			text, listed := cursorStoreUserText(item.msg.Content)
			if !listed {
				discards = append(discards, cursorExcluded(item.record,
					"Cursor injected context is not a user turn"))
				continue
			}
			if strings.TrimSpace(text) == "" {
				discards = append(discards, cursorExcluded(item.record,
					"Cursor user message has no text"))
				continue
			}
			closeCurrent()
			turnNumber++
			current = &cursorTurn{number: turnNumber, sourceID: firstNonEmpty(item.msg.ID, item.id),
				human: text, fingerprint: hashWriter{parts: [][]byte{append([]byte(nil), item.raw...)}}}
		case "assistant":
			if current == nil {
				discards = append(discards, cursorExcluded(item.record,
					"Cursor assistant message has no open user turn"))
				continue
			}
			current.fingerprint.parts = append(current.fingerprint.parts, append([]byte(nil), item.raw...))
			text, thought, tools := cursorStoreAssistant(item.msg.Content)
			if text != "" {
				current.agent = append(current.agent, text)
			}
			if thought != "" {
				current.thinking = append(current.thinking, Thinking{Text: thought, WordCount: wordCount(thought)})
			}
			current.tools = append(current.tools, tools...)
		case "tool":
			if current == nil {
				discards = append(discards, cursorExcluded(item.record,
					"Cursor tool result has no open user turn"))
				continue
			}
			current.fingerprint.parts = append(current.fingerprint.parts, append([]byte(nil), item.raw...))
		default:
			discards = append(discards, cursorExcluded(item.record,
				"Cursor store message has an unsupported role"))
		}
	}
	closeCurrent()
	PlaceThinking(session.Exchanges)
	if session.EndedAt == "" && len(session.Exchanges) > 0 {
		session.EndedAt = firstNonEmpty(session.Exchanges[len(session.Exchanges)-1].AgentTimestamp,
			session.Exchanges[len(session.Exchanges)-1].HumanTimestamp)
	}
	if session.StartedAt != "" && session.EndedAt != "" {
		if duration := cursorDuration(session.StartedAt, session.EndedAt); duration != nil {
			session.DurationMinutes = duration
		}
	}
	records := Records{Discards: discards}
	if session.ID != "" && len(session.Exchanges) > 0 {
		records.Sessions = []Session{session}
	}
	return records, nil
}

type cursorStoreItem struct {
	id     string
	raw    []byte
	record int
	msg    cursorStoreMessage
}

func cursorStoreBlobs(db *sql.DB) (map[string][]byte, error) {
	rows, err := db.Query(`SELECT id, data FROM blobs`)
	if err != nil {
		return nil, fmt.Errorf("read Cursor store blobs: %w", err)
	}
	defer rows.Close()
	blobs := map[string][]byte{}
	for rows.Next() {
		var id string
		var data []byte
		if err := rows.Scan(&id, &data); err != nil {
			return nil, err
		}
		blobs[id] = append([]byte(nil), data...)
	}
	return blobs, rows.Err()
}

func cursorStoreSessionMeta(db *sql.DB) (cursorStoreMeta, error) {
	var raw string
	err := db.QueryRow(`SELECT value FROM meta WHERE key = '0'`).Scan(&raw)
	if err == sql.ErrNoRows {
		return cursorStoreMeta{}, nil
	}
	if err != nil {
		if cursorTableMissing(err) {
			return cursorStoreMeta{}, nil
		}
		return cursorStoreMeta{}, fmt.Errorf("read Cursor store session metadata: %w", err)
	}
	decoded, err := hex.DecodeString(raw)
	if err != nil {
		return cursorStoreMeta{}, nil
	}
	var meta cursorStoreMeta
	if err := json.Unmarshal(decoded, &meta); err != nil {
		return cursorStoreMeta{}, nil
	}
	return meta, nil
}

func cursorTableMissing(err error) bool {
	return err != nil && strings.Contains(err.Error(), "no such table")
}

func cursorStoreReadSidecar(raw []byte) cursorStoreSidecar {
	var sidecar cursorStoreSidecar
	if len(raw) == 0 {
		return sidecar
	}
	_ = json.Unmarshal(raw, &sidecar)
	return sidecar
}

func cursorStorePathSession(path string) string {
	dir := filepath.Base(filepath.Dir(filepath.Clean(path)))
	if len(dir) == 36 && strings.Count(dir, "-") == 4 {
		return dir
	}
	return ""
}

func cursorStoreTimestamp(values ...int64) string {
	for _, value := range values {
		if value > 0 {
			return ISOFromEpochMS(float64(value))
		}
	}
	return ""
}

func cursorStoreOrderedMessages(blobs map[string][]byte, latestRoot string) ([]cursorStoreItem, []Discard) {
	var lists []cursorStoreList
	for id, data := range blobs {
		kids, ts := cursorStoreListChildren(data)
		if len(kids) == 0 {
			continue
		}
		known := 0
		for _, kid := range kids {
			if _, ok := blobs[kid]; ok {
				known++
			}
		}
		if known == 0 {
			continue
		}
		lists = append(lists, cursorStoreList{id: id, kids: kids, ts: ts})
	}
	sort.SliceStable(lists, func(i, j int) bool {
		if lists[i].ts != lists[j].ts {
			if lists[i].ts == 0 {
				return true
			}
			if lists[j].ts == 0 {
				return false
			}
			return lists[i].ts < lists[j].ts
		}
		if len(lists[i].kids) != len(lists[j].kids) {
			return len(lists[i].kids) > len(lists[j].kids)
		}
		return lists[i].id < lists[j].id
	})
	if latestRoot != "" {
		// Keep latestRoot in the walk even when it is a compacted window: new
		// turns after a summary live only on that node.
		found := false
		for _, list := range lists {
			if list.id == latestRoot {
				found = true
				break
			}
		}
		if !found {
			if kids, ts := cursorStoreListChildren(blobs[latestRoot]); len(kids) > 0 {
				lists = append(lists, cursorStoreList{id: latestRoot, kids: kids, ts: ts})
			}
		}
	}
	seen := map[string]bool{}
	var items []cursorStoreItem
	var discards []Discard
	record := 0
	for _, list := range lists {
		for _, id := range list.kids {
			if seen[id] {
				continue
			}
			seen[id] = true
			raw, ok := blobs[id]
			if !ok {
				continue
			}
			msg, ok := cursorStoreJSONMessage(raw)
			if !ok {
				continue
			}
			record++
			items = append(items, cursorStoreItem{id: id, raw: raw, record: record, msg: msg})
		}
	}
	return items, discards
}

func cursorStoreJSONMessage(raw []byte) (cursorStoreMessage, bool) {
	trimmed := strings.TrimSpace(string(raw))
	if !strings.HasPrefix(trimmed, "{") {
		return cursorStoreMessage{}, false
	}
	var msg cursorStoreMessage
	if err := json.Unmarshal(raw, &msg); err != nil || msg.Role == "" {
		return cursorStoreMessage{}, false
	}
	return msg, true
}

func cursorStoreListChildren(data []byte) ([]string, int64) {
	fields, ok := cursorStoreProtoFields(data)
	if !ok {
		return nil, 0
	}
	var kids []string
	var ts int64
	for _, field := range fields {
		if field.num == 1 && field.wire == 2 && len(field.bytes) == 32 {
			kids = append(kids, hex.EncodeToString(field.bytes))
		}
		if field.num == 26 && field.wire == 0 {
			ts = int64(field.varint)
		}
	}
	return kids, ts
}

type cursorStoreProtoField struct {
	num    uint64
	wire   uint64
	varint uint64
	bytes  []byte
}

func cursorStoreProtoFields(data []byte) ([]cursorStoreProtoField, bool) {
	var fields []cursorStoreProtoField
	i := 0
	for i < len(data) {
		key, n, ok := cursorStoreVarint(data[i:])
		if !ok {
			return nil, false
		}
		i += n
		field := cursorStoreProtoField{num: key >> 3, wire: key & 7}
		switch field.wire {
		case 0:
			value, size, ok := cursorStoreVarint(data[i:])
			if !ok {
				return nil, false
			}
			field.varint = value
			i += size
		case 1:
			if i+8 > len(data) {
				return nil, false
			}
			field.bytes = data[i : i+8]
			i += 8
		case 2:
			length, size, ok := cursorStoreVarint(data[i:])
			if !ok || i+size+int(length) > len(data) {
				return nil, false
			}
			i += size
			field.bytes = data[i : i+int(length)]
			i += int(length)
		case 5:
			if i+4 > len(data) {
				return nil, false
			}
			field.bytes = data[i : i+4]
			i += 4
		default:
			return nil, false
		}
		fields = append(fields, field)
	}
	return fields, true
}

func cursorStoreVarint(data []byte) (uint64, int, bool) {
	var value uint64
	for i := 0; i < len(data) && i < 10; i++ {
		value |= uint64(data[i]&0x7f) << (7 * i)
		if data[i] < 0x80 {
			return value, i + 1, true
		}
	}
	return 0, 0, false
}

func cursorStoreUserText(raw json.RawMessage) (string, bool) {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text, false
	}
	var parts []cursorStorePart
	if json.Unmarshal(raw, &parts) != nil {
		return "", false
	}
	var chunks []string
	for _, part := range parts {
		if part.Type == "text" {
			if trimmed := strings.TrimSpace(part.Text); trimmed != "" {
				chunks = append(chunks, trimmed)
			}
		}
	}
	return strings.Join(chunks, "\n\n"), true
}

func cursorStoreAssistant(raw json.RawMessage) (text, thought string, tools []ToolUse) {
	var asText string
	if json.Unmarshal(raw, &asText) == nil {
		return strings.TrimSpace(asText), "", nil
	}
	var parts []cursorStorePart
	if json.Unmarshal(raw, &parts) != nil {
		return "", "", nil
	}
	var texts, thoughts []string
	for _, part := range parts {
		switch part.Type {
		case "text":
			if trimmed := strings.TrimSpace(part.Text); trimmed != "" {
				texts = append(texts, trimmed)
			}
		case "reasoning":
			if trimmed := strings.TrimSpace(part.Text); trimmed != "" {
				thoughts = append(thoughts, trimmed)
			}
		case "tool-call":
			if name := strings.TrimSpace(part.ToolName); name != "" {
				tools = append(tools, ToolUse{Name: name, ParamsSummary: Clip(cursorStoreArgs(part.Args), 1000)})
			}
		}
	}
	return strings.Join(texts, "\n\n"), strings.Join(thoughts, "\n\n"), tools
}

func cursorStoreArgs(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return strings.TrimSpace(text)
	}
	return strings.TrimSpace(string(raw))
}
