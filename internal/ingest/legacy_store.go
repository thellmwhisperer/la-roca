package ingest

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/thellmwhisperer/la-roca/pkg/ingestprovenance"
	"github.com/thellmwhisperer/la-roca/pkg/parsers"
)

// The pre-federation store held harvested conversations and agent-written
// memories in one SQLite database. This reader is the upgrade path into the
// federation. It is not a general compatibility layer. Conversations land in
// the corpus; memories keep the layer they were stored under and land in ops.

const (
	legacyStoreSource                       = "legacy-store"
	legacyStoreMemoryFile                   = "legacy-store:memory:"
	legacyMemoryIDKey                       = "legacy_memory_id"
	legacySupersedesKey                     = "legacy_supersedes"
	legacyStoreMissingToolExchangeReason    = "tool references a missing exchange"
	legacyStoreMissingExchangeSessionReason = "exchange references a missing session"
	legacyStoreMissingToolSessionReason     = "tool references a missing session"
	legacyStoreMissingThinkingSessionReason = "thinking block references a missing session"
	legacyStoreEmptyMemoryReason            = "legacy store memory has empty content"
)

var legacyStoreSchema = []foreignTable{
	{"sessions", []string{"session_id", "source_agent", "project", "started_at",
		"ended_at", "duration_minutes", "title", "metadata"}},
	{"exchanges", []string{"id", "session_id", "exchange_number", "is_after_compaction",
		"human_text", "agent_text", "human_timestamp", "agent_timestamp", "response_latency_ms"}},
	{"tool_uses", []string{"id", "session_id", "exchange_number", "tool_name",
		"tool_params_summary", "had_error", "error_message", "initiative_type"}},
	{"thinking_blocks", []string{"id", "session_id", "exchange_number",
		"position_in_session", "depth", "caution_ratio", "word_count",
		"is_after_compaction", "full_text"}},
	{"memories", []string{"id", "layer", "content", "metadata", "origin",
		"source_agent", "source_session", "source_sequence", "project",
		"status", "supersedes", "created_at"}},
}

var legacyStoreExclusions = []struct {
	table, reason string
}{
	{"flow_patterns", "legacy store derived flow patterns"},
	{"garden_channels", "legacy store garden records"},
	{"garden_memberships", "legacy store garden records"},
	{"garden_messages", "legacy store garden records"},
	{"garden_read_cursors", "legacy store garden records"},
	{"garden_voice_leases", "legacy store garden records"},
	{"ingest_file_state", "legacy store ingest state"},
	{"layers", "legacy store layer registry"},
	{"layer_stats", "legacy store layer statistics"},
	{"messages", "legacy store unused message rows"},
	{"proposal_annotations", "legacy store proposals"},
	{"proposals", "legacy store proposals"},
	{"queryplan_teach_examples", "legacy store query-plan examples"},
	{"run_logs", "legacy store run history"},
	{"runs", "legacy store run history"},
}

// ReadLegacyStore projects a pre-federation La Roca database onto normalized
// records. The whole read happens before anything is written, on a query_only
// connection: a snapshot, never a live tail.
func ReadLegacyStore(ctx context.Context, path string) (parsers.Records, []string, error) {
	db, err := openLegacyStoreSource(ctx, path)
	if err != nil {
		return parsers.Records{}, nil, err
	}
	defer db.Close()

	sessions, err := queryRows(ctx, db,
		`SELECT CAST(rowid AS TEXT) AS source_rowid, session_id, source_agent, project, started_at, ended_at,
		        duration_minutes, title, metadata
		 FROM sessions ORDER BY started_at, session_id, rowid`)
	if err != nil {
		return parsers.Records{}, nil, err
	}
	exchangeProjection, err := legacyStoreProjection(ctx, db, "exchanges", []string{
		"id", "session_id", "exchange_number", "is_after_compaction", "human_text",
		"agent_text", "human_timestamp", "agent_timestamp", "response_latency_ms",
		"model", "provider", "tokens_in", "tokens_out", "tokens_reasoning", "cost_usd",
	})
	if err != nil {
		return parsers.Records{}, nil, err
	}
	exchanges, err := queryRows(ctx, db,
		`SELECT `+exchangeProjection+`
		 FROM exchanges ORDER BY session_id, exchange_number, id`)
	if err != nil {
		return parsers.Records{}, nil, err
	}
	toolProjection, err := legacyStoreProjection(ctx, db, "tool_uses", []string{
		"id", "session_id", "exchange_number", "call_id", "tool_name", "tool_params_summary",
		"had_error", "error_message", "initiative_type",
	})
	if err != nil {
		return parsers.Records{}, nil, err
	}
	tools, err := queryRows(ctx, db,
		`SELECT `+toolProjection+` FROM tool_uses ORDER BY session_id, exchange_number, id`)
	if err != nil {
		return parsers.Records{}, nil, err
	}
	thinking, err := queryRows(ctx, db,
		`SELECT id, session_id, exchange_number, position_in_session, depth,
		        caution_ratio, word_count, is_after_compaction, full_text
		 FROM thinking_blocks ORDER BY session_id, exchange_number, position_in_session, id`)
	if err != nil {
		return parsers.Records{}, nil, err
	}
	memoryProjection, err := legacyStoreProjection(ctx, db, "memories", []string{
		"id", "layer", "content", "metadata", "origin", "source_agent", "source_model",
		"source_session", "source_sequence", "project", "status", "supersedes", "created_at",
	})
	if err != nil {
		return parsers.Records{}, nil, err
	}
	memories, err := queryRows(ctx, db,
		`SELECT `+memoryProjection+` FROM memories ORDER BY id`)
	if err != nil {
		return parsers.Records{}, nil, err
	}

	exchangesBySession := groupLegacyStoreRows(exchanges)
	toolsBySession := groupLegacyStoreRows(tools)
	thinkingBySession := groupLegacyStoreRows(thinking)

	records := parsers.Records{Seen: parsers.Seen{Sessions: len(sessions)}}
	var complaints []string
	for _, source := range sessions {
		native := source.text("session_id")
		var sessionExchanges, sessionTools, sessionThinking []row
		if native != "" {
			sessionExchanges = exchangesBySession[native]
			sessionTools = toolsBySession[native]
			sessionThinking = thinkingBySession[native]
			delete(exchangesBySession, native)
			delete(toolsBySession, native)
			delete(thinkingBySession, native)
		}
		records.Seen.Messages += len(sessionExchanges)
		session, discards := legacyStoreSession(source, sessionExchanges,
			sessionThinking, sessionTools)
		records.Sessions = append(records.Sessions, session)
		records.Discards = append(records.Discards, discards...)
	}
	records.Discards = append(records.Discards,
		legacyStoreOrphanDiscards(exchangesBySession, legacyStoreMissingExchangeSessionReason)...)
	records.Discards = append(records.Discards,
		legacyStoreOrphanDiscards(toolsBySession, legacyStoreMissingToolSessionReason)...)
	records.Discards = append(records.Discards,
		legacyStoreOrphanDiscards(thinkingBySession, legacyStoreMissingThinkingSessionReason)...)
	for _, memory := range memories {
		normalized, ok := legacyStoreMemory(memory)
		if !ok {
			records.Discards = append(records.Discards, parsers.Excluded(legacyStoreEmptyMemoryReason))
			continue
		}
		records.Memories = append(records.Memories, normalized)
	}
	records.Discards = append(records.Discards, legacyStoreExclusionDiscards(ctx, db)...)
	return records, complaints, nil
}

func openLegacyStoreSource(ctx context.Context, path string) (*sql.DB, error) {
	db, err := openForeignPath(ctx, path, true)
	if err != nil {
		return nil, err
	}
	for _, table := range legacyStoreSchema {
		if err := requireColumns(ctx, db, table.name, table.required); err != nil {
			db.Close()
			return nil, fmt.Errorf("Legacy store: %w", err)
		}
	}
	return db, nil
}

func legacyStoreProjection(ctx context.Context, db *sql.DB, table string,
	candidates []string) (string, error) {
	available, err := tableColumns(ctx, db, table)
	if err != nil {
		return "", err
	}
	selected := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		if available[candidate] {
			selected = append(selected, candidate)
		}
	}
	return strings.Join(selected, ", "), nil
}

func groupLegacyStoreRows(rows []row) map[string][]row {
	out := map[string][]row{}
	for _, item := range rows {
		id := item.text("session_id")
		out[id] = append(out[id], item)
	}
	return out
}

func legacyStoreOrphanDiscards(groups map[string][]row, reason string) []parsers.Discard {
	var discards []parsers.Discard
	for _, rows := range groups {
		for range rows {
			discards = append(discards, parsers.Discard{Reason: reason})
		}
	}
	return discards
}

func legacyStoreSession(source row, exchanges, thinking, tools []row) (parsers.Session, []parsers.Discard) {
	id := source.text("session_id")
	if id == "" {
		id = legacyStoreSource + ":empty-session:" + source.text("source_rowid")
	}
	session := parsers.Session{
		ID:               id,
		SourceAgent:      source.text("source_agent"),
		SourceSurface:    ingestprovenance.LegacyStore,
		Project:          source.text("project"),
		StartedAt:        source.text("started_at"),
		EndedAt:          source.text("ended_at"),
		Title:            source.text("title"),
		Metadata:         legacyStoreMetadata(source.text("metadata")),
		ExchangeKeyScope: legacyStoreSource,
	}
	if minutes, ok := source.number("duration_minutes"); ok {
		value := int(minutes)
		session.DurationMinutes = &value
	}
	thinkingByNumber := map[int][]parsers.Thinking{}
	for _, block := range thinking {
		number, _ := block.number("exchange_number")
		thinkingByNumber[int(number)] = append(thinkingByNumber[int(number)], legacyStoreThinking(block))
	}
	toolsByNumber := map[int][]parsers.ToolUse{}
	for _, tool := range tools {
		number, _ := tool.number("exchange_number")
		toolsByNumber[int(number)] = append(toolsByNumber[int(number)], legacyStoreTool(tool))
	}
	claimed := map[int]bool{}
	for i, item := range exchanges {
		original, _ := item.number("exchange_number")
		exchangeID, _ := item.number("id")
		exchange := parsers.Exchange{
			Number:            i + 1,
			SourceID:          strconv.FormatInt(int64(exchangeID), 10),
			IsAfterCompaction: legacyStoreFlag(item, "is_after_compaction"),
			HumanText:         item.text("human_text"),
			AgentText:         item.text("agent_text"),
			HumanTimestamp:    item.text("human_timestamp"),
			AgentTimestamp:    item.text("agent_timestamp"),
			Provenance:        legacyStoreProvenance(item),
		}
		if !claimed[int(original)] {
			exchange.Thinking = thinkingByNumber[int(original)]
			exchange.Tools = toolsByNumber[int(original)]
			claimed[int(original)] = true
			delete(thinkingByNumber, int(original))
			delete(toolsByNumber, int(original))
		}
		if latency, ok := item.number("response_latency_ms"); ok {
			value := int(latency)
			exchange.LatencyMS = &value
		}
		session.Exchanges = append(session.Exchanges, exchange)
	}
	leftoverNumbers := make([]int, 0, len(thinkingByNumber))
	for number := range thinkingByNumber {
		leftoverNumbers = append(leftoverNumbers, number)
	}
	sort.Ints(leftoverNumbers)
	for _, number := range leftoverNumbers {
		session.Thinking = append(session.Thinking, thinkingByNumber[number]...)
	}
	var discards []parsers.Discard
	for _, leftovers := range toolsByNumber {
		for range leftovers {
			discards = append(discards, parsers.Discard{Reason: legacyStoreMissingToolExchangeReason})
		}
	}
	return session, discards
}

func legacyStoreThinking(block row) parsers.Thinking {
	position, _ := block.number("position_in_session")
	if id, ok := block.number("id"); ok {
		position += id * 1e-12
	}
	words, _ := block.number("word_count")
	out := parsers.Thinking{
		Position:          position,
		Depth:             block.text("depth"),
		WordCount:         int(words),
		IsAfterCompaction: legacyStoreFlag(block, "is_after_compaction"),
		Text:              block.text("full_text"),
	}
	if ratio, ok := block.number("caution_ratio"); ok {
		out.CautionRatio = &ratio
	}
	return out
}

func legacyStoreTool(tool row) parsers.ToolUse {
	return parsers.ToolUse{
		CallID:         tool.text("call_id"),
		Name:           tool.text("tool_name"),
		ParamsSummary:  tool.text("tool_params_summary"),
		HadError:       legacyStoreFlag(tool, "had_error"),
		ErrorMessage:   tool.text("error_message"),
		InitiativeType: tool.text("initiative_type"),
	}
}

func legacyStoreProvenance(source row) parsers.Provenance {
	provenance := parsers.Provenance{
		Model:    source.text("model"),
		Provider: source.text("provider"),
	}
	if value, ok := source.number("tokens_in"); ok {
		count := int(value)
		provenance.TokensIn = &count
	}
	if value, ok := source.number("tokens_out"); ok {
		count := int(value)
		provenance.TokensOut = &count
	}
	if value, ok := source.number("tokens_reasoning"); ok {
		count := int(value)
		provenance.TokensReasoning = &count
	}
	if value, ok := source.number("cost_usd"); ok {
		provenance.CostUSD = &value
	}
	return provenance
}

func legacyStoreMetadata(encoded string) map[string]any {
	var metadata map[string]any
	if encoded == "" || json.Unmarshal([]byte(encoded), &metadata) != nil || metadata == nil {
		return map[string]any{}
	}
	return metadata
}

func legacyStoreMemory(source row) (parsers.Memory, bool) {
	content := source.text("content")
	if content == "" {
		return parsers.Memory{}, false
	}
	id, _ := source.number("id")
	identity := legacyStoreMemoryFile + strconv.FormatInt(int64(id), 10)
	metadata := legacyStoreMetadata(source.text("metadata"))
	metadata["_cron_source"] = legacyStoreSource
	metadata["file_path"] = identity
	metadata[legacyMemoryIDKey] = int64(id)
	if supersedes, ok := source.number("supersedes"); ok && supersedes != 0 {
		metadata[legacySupersedesKey] = int64(supersedes)
	}
	origin := source.text("origin")
	if origin == "" {
		origin = "cron"
	}
	memory := parsers.Memory{
		Layer:         source.text("layer"),
		Content:       content,
		Origin:        origin,
		SourceAgent:   source.text("source_agent"),
		SourceModel:   source.text("source_model"),
		SourceSurface: ingestprovenance.LegacyStore,
		Project:       source.text("project"),
		Metadata:      parsers.WithoutEmpty(metadata),
		Source:        legacyStoreSource,
		FilePath:      identity,
		CreatedAt:     source.text("created_at"),
		Status:        source.text("status"),
		SourceSession: source.text("source_session"),
		PreserveLayer: true,
		PreserveState: true,
	}
	if sequence, ok := source.number("source_sequence"); ok {
		value := int(sequence)
		memory.SourceSequence = &value
	}
	if supersedes, ok := source.number("supersedes"); ok && supersedes != 0 {
		memory.Supersedes = int64(supersedes)
	}
	return memory, true
}

func writeLegacyStoreSessions(ctx context.Context, tx *sql.Tx, sessions []parsers.Session) (Counts, error) {
	w := &writer{tx: tx, preserveSessionThinkingPosition: true}
	var counts Counts
	for _, session := range sessions {
		written, err := w.sessionIfMissing(ctx, session)
		if err != nil {
			return counts, err
		}
		counts.add(written)
	}
	return counts, nil
}

func legacyStoreExclusionDiscards(ctx context.Context, db *sql.DB) []parsers.Discard {
	var discards []parsers.Discard
	for _, exclusion := range legacyStoreExclusions {
		columns, err := tableColumns(ctx, db, exclusion.table)
		if err != nil || len(columns) == 0 {
			continue
		}
		var count int
		err = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+exclusion.table).Scan(&count)
		if err != nil || count == 0 {
			continue
		}
		for range count {
			discards = append(discards, parsers.Excluded(exclusion.reason))
		}
	}
	return discards
}

func legacyStoreFlag(item row, key string) bool {
	value, ok := item.number(key)
	return ok && value != 0
}

func remapLegacyStoreSupersedes(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE memories
		SET supersedes = (
		  SELECT other.id FROM memories AS other
		  WHERE json_extract(other.metadata, '$._cron_source') =
		        json_extract(memories.metadata, '$._cron_source')
		    AND json_extract(other.metadata, '$.`+legacyMemoryIDKey+`') =
		        json_extract(memories.metadata, '$.`+legacySupersedesKey+`')
		  LIMIT 1
		)
		WHERE json_extract(metadata, '$._cron_source') = ?
		  AND json_extract(metadata, '$.`+legacySupersedesKey+`') IS NOT NULL
		  AND supersedes IS NULL`, legacyStoreSource)
	if err != nil {
		return fmt.Errorf("remap legacy store supersedes: %w", err)
	}
	return nil
}
