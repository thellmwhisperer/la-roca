package parsers

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
	"modernc.org/sqlite/vfs"
)

const cursorExchangeScope = "cursor"

type cursorParser struct{}

func (cursorParser) Detect(file File) bool {
	if name := file.Meta.FileName; name != "" && name != "state.vscdb" &&
		name != "ai-code-tracking.db" {
		return false
	}
	if len(file.Content) < 16 || string(file.Content[:16]) != "SQLite format 3\x00" {
		return false
	}
	db, err := cursorSnapshotDB(file.Content)
	if err != nil {
		return false
	}
	defer db.close()
	state, err := cursorStateStore(db.db)
	if err == nil && state {
		return true
	}
	tracking, err := cursorTrackingStore(db.db)
	return err == nil && tracking
}

func (cursorParser) Parse(file File) (Records, error) {
	db, err := cursorSnapshotDB(file.Content)
	if err != nil {
		return Records{}, fmt.Errorf("open Cursor snapshot: %w", err)
	}
	defer db.close()
	if tracking, err := cursorTrackingStore(db.db); err != nil {
		return Records{}, err
	} else if tracking {
		return cursorTrackingRecords(db.db)
	}
	state, err := cursorStateStore(db.db)
	if err != nil {
		return Records{}, err
	}
	if !state {
		return Records{}, fmt.Errorf("Cursor snapshot has neither state nor AI tracking tables")
	}
	return cursorStateRecords(db.db, file.Meta)
}

type cursorSnapshot struct {
	db  *sql.DB
	vfs *vfs.FS
}

func (s *cursorSnapshot) close() {
	_ = s.db.Close()
	_ = s.vfs.Close()
}

func cursorSnapshotDB(content []byte) (*cursorSnapshot, error) {
	vfsName, memoryVFS, err := vfs.New(cursorByteFS{content: content})
	if err != nil {
		return nil, err
	}
	dsn := "file:state.vscdb?" + url.Values{
		"mode": {"ro"}, "immutable": {"1"}, "vfs": {vfsName},
	}.Encode()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		memoryVFS.Close()
		return nil, err
	}
	if err := db.Ping(); err != nil {
		db.Close()
		memoryVFS.Close()
		return nil, err
	}
	if _, err := db.Exec(`PRAGMA query_only = ON`); err != nil {
		db.Close()
		memoryVFS.Close()
		return nil, err
	}
	return &cursorSnapshot{db: db, vfs: memoryVFS}, nil
}

type cursorByteFS struct{ content []byte }

func (f cursorByteFS) Open(name string) (fs.File, error) {
	if name != "state.vscdb" {
		return nil, fs.ErrNotExist
	}
	return &cursorByteFile{Reader: bytes.NewReader(f.content), size: int64(len(f.content))}, nil
}

type cursorByteFile struct {
	*bytes.Reader
	size int64
}

func (f *cursorByteFile) Close() error               { return nil }
func (f *cursorByteFile) Stat() (fs.FileInfo, error) { return cursorByteInfo{f.size}, nil }

type cursorByteInfo struct{ size int64 }

func (cursorByteInfo) Name() string       { return "state.vscdb" }
func (i cursorByteInfo) Size() int64      { return i.size }
func (cursorByteInfo) Mode() fs.FileMode  { return 0o400 }
func (cursorByteInfo) ModTime() time.Time { return time.Time{} }
func (cursorByteInfo) IsDir() bool        { return false }
func (cursorByteInfo) Sys() any           { return nil }

func cursorStateStore(db *sql.DB) (bool, error) {
	item, err := cursorTableExists(db, "ItemTable")
	if err != nil || !item {
		return false, err
	}
	disk, err := cursorTableExists(db, "cursorDiskKV")
	if err != nil || !disk {
		return false, err
	}
	var count int
	err = db.QueryRow(`SELECT COUNT(*) FROM cursorDiskKV
		WHERE key LIKE 'composerData:%' OR key LIKE 'bubbleId:%'`).Scan(&count)
	if err != nil {
		return false, err
	}
	if count > 0 {
		return true, nil
	}
	err = db.QueryRow(`SELECT COUNT(*) FROM ItemTable
		WHERE key IN ('aiService.prompts', 'aiService.generations')`).Scan(&count)
	return count > 0, err
}

func cursorTrackingStore(db *sql.DB) (bool, error) {
	for _, table := range []string{"ai_code_hashes", "tracking_state"} {
		present, err := cursorTableExists(db, table)
		if err != nil || !present {
			return false, err
		}
	}
	return true, nil
}

func cursorTableExists(db *sql.DB, table string) (bool, error) {
	var count int
	err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master
		WHERE type = 'table' AND name = ?`, table).Scan(&count)
	return count == 1, err
}

type cursorComposer struct {
	ComposerID                  string         `json:"composerId"`
	CreatedAt                   int64          `json:"createdAt"`
	LastUpdatedAt               int64          `json:"lastUpdatedAt"`
	Name                        string         `json:"name"`
	Subtitle                    string         `json:"subtitle"`
	Status                      string         `json:"status"`
	UnifiedMode                 string         `json:"unifiedMode"`
	ParentComposerID            string         `json:"parentComposerId"`
	FullConversationHeadersOnly []cursorHeader `json:"fullConversationHeadersOnly"`
}

type cursorHeader struct {
	BubbleID string `json:"bubbleId"`
	Type     int    `json:"type"`
}

type cursorBubble struct {
	BubbleID            string   `json:"bubbleId"`
	Text                string   `json:"text"`
	CreatedAt           string   `json:"createdAt"`
	WorkspaceProjectDir string   `json:"workspaceProjectDir"`
	WorkspaceURIs       []string `json:"workspaceUris"`
	Thinking            struct {
		Text string `json:"text"`
	} `json:"thinking"`
	ModelInfo struct {
		ModelName string `json:"modelName"`
	} `json:"modelInfo"`
	TokenCount struct {
		InputTokens  int `json:"inputTokens"`
		OutputTokens int `json:"outputTokens"`
	} `json:"tokenCount"`
	ToolFormerData struct {
		Name    string `json:"name"`
		Params  string `json:"params"`
		RawArgs string `json:"rawArgs"`
		Status  string `json:"status"`
		Result  string `json:"result"`
		Error   string `json:"error"`
	} `json:"toolFormerData"`
}

type cursorJSONRow struct {
	key    string
	raw    []byte
	record int
}

func cursorStateRecords(db *sql.DB, meta FileMeta) (Records, error) {
	rows, err := db.Query(`SELECT key, value FROM cursorDiskKV
		WHERE key LIKE 'composerData:%' OR key LIKE 'bubbleId:%' ORDER BY key`)
	if err != nil {
		return Records{}, fmt.Errorf("read Cursor conversation rows: %w", err)
	}
	defer rows.Close()
	var composers, bubbles []cursorJSONRow
	record := 0
	for rows.Next() {
		var key string
		var raw []byte
		if err := rows.Scan(&key, &raw); err != nil {
			return Records{}, err
		}
		record++
		row := cursorJSONRow{key: key, raw: append([]byte(nil), raw...), record: record}
		if strings.HasPrefix(key, "composerData:") {
			composers = append(composers, row)
		} else {
			bubbles = append(bubbles, row)
		}
	}
	if err := rows.Err(); err != nil {
		return Records{}, err
	}
	bubbleRows := make(map[string]cursorJSONRow, len(bubbles))
	for _, bubble := range bubbles {
		bubbleRows[bubble.key] = bubble
	}
	referenced := map[string]bool{}
	records := Records{}
	for _, row := range composers {
		var composer cursorComposer
		if err := json.Unmarshal(row.raw, &composer); err != nil {
			records.Discards = append(records.Discards, Discard{Record: row.record,
				Reason: "Cursor composer is malformed JSON", Category: "malformed Cursor composer"})
			continue
		}
		if composer.ComposerID == "" {
			composer.ComposerID = strings.TrimPrefix(row.key, "composerData:")
		}
		if composer.ComposerID == "" {
			records.Discards = append(records.Discards, Discard{Record: row.record,
				Reason: "Cursor composer has no identity", Category: "Cursor composer has no identity"})
			continue
		}
		if len(composer.FullConversationHeadersOnly) == 0 {
			records.Discards = append(records.Discards, cursorExcluded(row.record,
				"Cursor composer has no conversation headers"))
			continue
		}
		session, deferred, discards := cursorSession(composer, bubbleRows, referenced, meta)
		records.Discards = append(records.Discards, discards...)
		records.Deferred += deferred
		if len(session.Exchanges) > 0 {
			records.Sessions = append(records.Sessions, session)
		}
	}
	for _, bubble := range bubbles {
		if !referenced[bubble.key] {
			records.Discards = append(records.Discards, cursorExcluded(bubble.record,
				"Cursor bubble is outside the active conversation headers"))
		}
	}
	secondary, err := cursorSecondaryRecords(db)
	if err != nil {
		return Records{}, err
	}
	records.Discards = append(records.Discards, secondary...)
	return records, nil
}

func cursorSession(composer cursorComposer, bubbles map[string]cursorJSONRow,
	referenced map[string]bool, meta FileMeta) (Session, int, []Discard) {
	source := firstNonEmpty(meta.SourceAgent, "cursor")
	session := Session{
		ID:               "cursor:" + composer.ComposerID,
		SourceAgent:      source,
		StartedAt:        ISOFromEpochMS(float64(composer.CreatedAt)),
		EndedAt:          ISOFromEpochMS(float64(composer.LastUpdatedAt)),
		Title:            firstNonEmpty(strings.TrimSpace(composer.Name), strings.TrimSpace(composer.Subtitle)),
		Snapshot:         true,
		ExchangeKeyScope: cursorExchangeScope,
		Metadata: WithoutEmpty(map[string]any{
			"native_composer_id": composer.ComposerID,
			"status":             composer.Status,
			"mode":               composer.UnifiedMode,
		}),
	}
	if composer.ParentComposerID != "" {
		session.ParentID = "cursor:" + composer.ParentComposerID
	}
	var current *cursorTurn
	var discards []Discard
	deferred := 0
	turnNumber := 0
	composerHasEnd := composer.LastUpdatedAt > 0
	closeCurrent := func() {
		if current == nil {
			return
		}
		if current.hasAnswer() {
			session.Exchanges = append(session.Exchanges, current.exchange())
		} else if composer.Status == "none" {
			deferred++
		} else {
			discards = append(discards, Discard{Reason: "Cursor turn has no assistant output",
				Category: "Cursor turn has no assistant output"})
		}
		current = nil
	}
	for index, header := range composer.FullConversationHeadersOnly {
		key := "bubbleId:" + composer.ComposerID + ":" + header.BubbleID
		referenced[key] = true
		row, found := bubbles[key]
		if !found {
			discards = append(discards, Discard{Record: index + 1,
				Reason:   "Cursor conversation header points to a missing bubble",
				Category: "Cursor conversation header points to a missing bubble"})
			continue
		}
		var bubble cursorBubble
		if err := json.Unmarshal(row.raw, &bubble); err != nil {
			discards = append(discards, Discard{Record: row.record,
				Reason: "Cursor bubble is malformed JSON", Category: "malformed Cursor bubble"})
			continue
		}
		switch header.Type {
		case 1:
			closeCurrent()
			turnNumber++
			current = newCursorTurn(turnNumber, header.BubbleID, bubble, row.raw)
			if session.StartedAt == "" {
				session.StartedAt = bubble.CreatedAt
			}
			if project := cursorProject(bubble); session.Project == "" && project != "" {
				session.Project = project
			}
		case 2:
			if current == nil {
				discards = append(discards, Discard{Record: row.record,
					Reason:   "Cursor assistant bubble has no open user turn",
					Category: "Cursor assistant bubble has no open user turn"})
				continue
			}
			current.addAssistant(bubble, row.raw)
			if project := cursorProject(bubble); session.Project == "" && project != "" {
				session.Project = project
			}
			if !composerHasEnd && bubble.CreatedAt != "" {
				session.EndedAt = bubble.CreatedAt
			}
		default:
			discards = append(discards, cursorExcluded(row.record,
				"Cursor conversation header has an unsupported bubble type"))
		}
	}
	closeCurrent()
	PlaceThinking(session.Exchanges)
	if session.StartedAt != "" && session.EndedAt != "" {
		if duration := cursorDuration(session.StartedAt, session.EndedAt); duration != nil {
			session.DurationMinutes = duration
		}
	}
	return session, deferred, discards
}

type cursorTurn struct {
	number      int
	sourceID    string
	human       string
	humanAt     string
	agent       []string
	agentAt     string
	model       string
	usage       UsageTally
	thinking    []Thinking
	tools       []ToolUse
	fingerprint hashWriter
	signal      int
}

type hashWriter struct{ parts [][]byte }

func newCursorTurn(number int, sourceID string, bubble cursorBubble, raw []byte) *cursorTurn {
	return &cursorTurn{number: number, sourceID: sourceID,
		human: strings.TrimSpace(bubble.Text), humanAt: bubble.CreatedAt,
		fingerprint: hashWriter{parts: [][]byte{append([]byte(nil), raw...)}}}
}

func (t *cursorTurn) addAssistant(bubble cursorBubble, raw []byte) {
	t.fingerprint.parts = append(t.fingerprint.parts, append([]byte(nil), raw...))
	if text := strings.TrimSpace(bubble.Text); text != "" {
		t.agent = append(t.agent, text)
	}
	if bubble.CreatedAt != "" {
		t.agentAt = bubble.CreatedAt
	}
	if thought := strings.TrimSpace(bubble.Thinking.Text); thought != "" {
		t.thinking = append(t.thinking, Thinking{Text: thought, WordCount: wordCount(thought)})
	}
	if name := strings.TrimSpace(bubble.ToolFormerData.Name); name != "" {
		params := firstNonEmpty(strings.TrimSpace(bubble.ToolFormerData.Params),
			strings.TrimSpace(bubble.ToolFormerData.RawArgs))
		hadError := strings.EqualFold(bubble.ToolFormerData.Status, "error") ||
			strings.TrimSpace(bubble.ToolFormerData.Error) != ""
		t.tools = append(t.tools, ToolUse{Name: name, ParamsSummary: Clip(params, 1000),
			HadError: hadError, ErrorMessage: Clip(strings.TrimSpace(bubble.ToolFormerData.Error), 1000)})
	}
	if model := strings.TrimSpace(bubble.ModelInfo.ModelName); model != "" {
		if t.model == "" {
			t.signal++
		}
		t.model = model
	}
	if bubble.TokenCount.InputTokens > 0 {
		t.usage.AddInputTokens(bubble.TokenCount.InputTokens)
		t.signal++
	}
	if bubble.TokenCount.OutputTokens > 0 {
		t.usage.AddOutputTokens(bubble.TokenCount.OutputTokens)
		t.signal++
	}
}

func (t *cursorTurn) hasAnswer() bool {
	return len(t.agent) > 0 || len(t.thinking) > 0 || len(t.tools) > 0
}

func (t *cursorTurn) exchange() Exchange {
	return Exchange{
		Number:         t.number,
		SourceID:       t.sourceID,
		Fingerprint:    t.fingerprint.sum(),
		HumanText:      t.human,
		AgentText:      strings.Join(t.agent, "\n\n"),
		HumanTimestamp: t.humanAt,
		AgentTimestamp: t.agentAt,
		LatencyMS:      latency(t.humanAt, t.agentAt),
		Thinking:       t.thinking,
		Tools:          t.tools,
		Provenance:     t.usage.Provenance(t.model, ""),
		Signal:         &t.signal,
	}
}

func (h hashWriter) sum() string {
	digest := sha256.New()
	for _, part := range h.parts {
		digest.Write(part)
		digest.Write([]byte{0})
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func cursorProject(bubble cursorBubble) string {
	for _, rawURI := range bubble.WorkspaceURIs {
		workspaceURI, err := url.Parse(strings.TrimSpace(rawURI))
		if err != nil || workspaceURI.Scheme != "file" {
			continue
		}
		if project := cursorProjectBase(filepath.FromSlash(workspaceURI.Path)); project != "" {
			return project
		}
	}
	return cursorProjectBase(bubble.WorkspaceProjectDir)
}

func cursorProjectBase(path string) string {
	path = strings.TrimSpace(path)
	base := filepath.Base(filepath.Clean(path))
	if path == "" || base == "." || base == string(filepath.Separator) {
		return ""
	}
	return base
}

func cursorDuration(started, ended string) *int {
	start, startErr := time.Parse(time.RFC3339Nano, started)
	end, endErr := time.Parse(time.RFC3339Nano, ended)
	if startErr != nil || endErr != nil || end.Before(start) {
		return nil
	}
	minutes := int(end.Sub(start).Minutes())
	return &minutes
}

func cursorSecondaryRecords(db *sql.DB) ([]Discard, error) {
	rows, err := db.Query(`SELECT key, value FROM ItemTable
		WHERE key IN ('aiService.prompts', 'aiService.generations') ORDER BY key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var discards []Discard
	for rows.Next() {
		var key string
		var raw []byte
		if err := rows.Scan(&key, &raw); err != nil {
			return nil, err
		}
		var entries []json.RawMessage
		if err := json.Unmarshal(raw, &entries); err != nil {
			discards = append(discards, Discard{Reason: "Cursor workspace AI history is malformed",
				Category: "malformed Cursor workspace AI history"})
			continue
		}
		category := "Cursor workspace prompts repeat the composer store"
		if key == "aiService.generations" {
			category = "Cursor workspace generations repeat the composer store"
		}
		for index := range entries {
			discards = append(discards, cursorExcluded(index+1, category))
		}
	}
	return discards, rows.Err()
}

func cursorTrackingRecords(db *sql.DB) (Records, error) {
	var records Records
	for _, table := range []string{"ai_code_hashes", "scored_commits", "tracking_state"} {
		present, err := cursorTableExists(db, table)
		if err != nil {
			return Records{}, err
		}
		if !present {
			continue
		}
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil {
			return Records{}, err
		}
		for record := 1; record <= count; record++ {
			records.Discards = append(records.Discards, cursorExcluded(record,
				"Cursor AI tracking contains attribution metadata, not conversation content"))
		}
	}
	return records, nil
}

func cursorExcluded(record int, reason string) Discard {
	return Discard{Record: record, Reason: reason, Category: reason, ByDesign: true}
}
