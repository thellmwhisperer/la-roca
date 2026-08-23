package ingest

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"

	"github.com/thellmwhisperer/la-roca/pkg/ingestprovenance"
	"github.com/thellmwhisperer/la-roca/pkg/parsers"
)

// The pre-federation store held harvested conversations and agent-written
// memories in one SQLite database. This reader is the upgrade path into the
// federation. It is not a general compatibility layer. Conversations land in
// the corpus; memories keep the layer they were stored under and land in ops.

const (
	madreSource        = "legacy-store"
	madreMemoryFile    = "legacy-store:memory:"
	madreMemoryIDKey   = "madre_memory_id"
	madreSupersedesKey = "madre_supersedes"
)

var madreSchema = []foreignTable{
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

var madreExclusions = []struct {
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
	{"messages", "legacy store unused message rows"},
	{"proposal_annotations", "legacy store proposals"},
	{"proposals", "legacy store proposals"},
	{"queryplan_teach_examples", "legacy store query-plan examples"},
	{"run_logs", "legacy store run history"},
	{"runs", "legacy store run history"},
}

// ReadMadre projects a pre-federation La Roca database onto normalized
// records. The whole read happens before anything is written, on a query_only
// connection: a snapshot, never a live tail.
func ReadMadre(ctx context.Context, path string) (parsers.Records, []string, error) {
	db, err := openForeignSource(ctx, "Legacy store", path, madreSchema)
	if err != nil {
		return parsers.Records{}, nil, err
	}
	defer db.Close()

	sessions, err := queryRows(ctx, db,
		`SELECT session_id, source_agent, project, started_at, ended_at,
		        duration_minutes, title, metadata
		 FROM sessions ORDER BY started_at, session_id, rowid`)
	if err != nil {
		return parsers.Records{}, nil, err
	}
	exchanges, err := queryRows(ctx, db,
		`SELECT id, session_id, exchange_number, is_after_compaction, human_text,
		        agent_text, human_timestamp, agent_timestamp, response_latency_ms
		 FROM exchanges ORDER BY session_id, exchange_number, id`)
	if err != nil {
		return parsers.Records{}, nil, err
	}
	tools, err := queryRows(ctx, db,
		`SELECT id, session_id, exchange_number, tool_name, tool_params_summary,
		        had_error, error_message, initiative_type
		 FROM tool_uses ORDER BY session_id, exchange_number, id`)
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
	memories, err := queryRows(ctx, db,
		`SELECT id, layer, content, metadata, origin, source_agent, source_session,
		        source_sequence, project, status, supersedes, created_at
		 FROM memories ORDER BY id`)
	if err != nil {
		return parsers.Records{}, nil, err
	}

	exchangesBySession := groupMadreRows(exchanges)
	toolsBySession := groupMadreRows(tools)
	thinkingBySession := groupMadreRows(thinking)

	records := parsers.Records{Seen: parsers.Seen{Sessions: len(sessions)}}
	var complaints []string
	for _, source := range sessions {
		native := source.text("session_id")
		sessionExchanges := exchangesBySession[native]
		records.Seen.Messages += len(sessionExchanges)
		records.Sessions = append(records.Sessions, madreSession(source,
			sessionExchanges, thinkingBySession[native], toolsBySession[native]))
	}
	for _, memory := range memories {
		normalized, ok := madreMemory(memory)
		if !ok {
			continue
		}
		records.Memories = append(records.Memories, normalized)
	}
	records.Discards = append(records.Discards, madreExclusionDiscards(ctx, db)...)
	return records, complaints, nil
}

func groupMadreRows(rows []row) map[string][]row {
	out := map[string][]row{}
	for _, item := range rows {
		id := item.text("session_id")
		out[id] = append(out[id], item)
	}
	return out
}

func madreSession(source row, exchanges, thinking, tools []row) parsers.Session {
	id := source.text("session_id")
	if id == "" {
		id = madreSource + ":empty-session"
	}
	session := parsers.Session{
		ID:               id,
		SourceAgent:      source.text("source_agent"),
		SourceSurface:    ingestprovenance.LegacyStore,
		Project:          source.text("project"),
		StartedAt:        source.text("started_at"),
		EndedAt:          source.text("ended_at"),
		Title:            source.text("title"),
		ExchangeKeyScope: madreSource,
	}
	if minutes, ok := source.number("duration_minutes"); ok {
		value := int(minutes)
		session.DurationMinutes = &value
	}
	thinkingByNumber := map[int][]parsers.Thinking{}
	for _, block := range thinking {
		number, _ := block.number("exchange_number")
		thinkingByNumber[int(number)] = append(thinkingByNumber[int(number)], madreThinking(block))
	}
	toolsByNumber := map[int][]parsers.ToolUse{}
	for _, tool := range tools {
		number, _ := tool.number("exchange_number")
		toolsByNumber[int(number)] = append(toolsByNumber[int(number)], madreTool(tool))
	}
	claimed := map[int]bool{}
	for i, item := range exchanges {
		original, _ := item.number("exchange_number")
		exchangeID, _ := item.number("id")
		exchange := parsers.Exchange{
			Number:            i + 1,
			SourceID:          strconv.FormatInt(int64(exchangeID), 10),
			IsAfterCompaction: madreFlag(item, "is_after_compaction"),
			HumanText:         item.text("human_text"),
			AgentText:         item.text("agent_text"),
			HumanTimestamp:    item.text("human_timestamp"),
			AgentTimestamp:    item.text("agent_timestamp"),
		}
		if !claimed[int(original)] {
			exchange.Thinking = thinkingByNumber[int(original)]
			exchange.Tools = toolsByNumber[int(original)]
			claimed[int(original)] = true
			delete(thinkingByNumber, int(original))
		}
		if latency, ok := item.number("response_latency_ms"); ok {
			value := int(latency)
			exchange.LatencyMS = &value
		}
		session.Exchanges = append(session.Exchanges, exchange)
	}
	for _, leftover := range thinkingByNumber {
		session.Thinking = append(session.Thinking, leftover...)
	}
	return session
}

func madreThinking(block row) parsers.Thinking {
	position, _ := block.number("position_in_session")
	if id, ok := block.number("id"); ok {
		position += id * 1e-12
	}
	words, _ := block.number("word_count")
	out := parsers.Thinking{
		Position:          position,
		Depth:             block.text("depth"),
		WordCount:         int(words),
		IsAfterCompaction: madreFlag(block, "is_after_compaction"),
		Text:              block.text("full_text"),
	}
	if ratio, ok := block.number("caution_ratio"); ok {
		out.CautionRatio = &ratio
	}
	return out
}

func madreTool(tool row) parsers.ToolUse {
	return parsers.ToolUse{
		Name:           tool.text("tool_name"),
		ParamsSummary:  tool.text("tool_params_summary"),
		HadError:       madreFlag(tool, "had_error"),
		ErrorMessage:   tool.text("error_message"),
		InitiativeType: tool.text("initiative_type"),
	}
}

func madreMemory(source row) (parsers.Memory, bool) {
	content := source.text("content")
	if content == "" {
		return parsers.Memory{}, false
	}
	id, _ := source.number("id")
	identity := madreMemoryFile + strconv.FormatInt(int64(id), 10)
	metadata := map[string]any{
		"_cron_source":   madreSource,
		"file_path":      identity,
		madreMemoryIDKey: int64(id),
	}
	if supersedes, ok := source.number("supersedes"); ok && supersedes != 0 {
		metadata[madreSupersedesKey] = int64(supersedes)
	}
	origin := source.text("origin")
	if origin == "" {
		origin = "cron"
	}
	status := source.text("status")
	if status == "" {
		status = "active"
	}
	memory := parsers.Memory{
		Layer:         source.text("layer"),
		Content:       content,
		Origin:        origin,
		SourceAgent:   source.text("source_agent"),
		SourceSurface: ingestprovenance.LegacyStore,
		Project:       source.text("project"),
		Metadata:      parsers.WithoutEmpty(metadata),
		Source:        madreSource,
		FilePath:      identity,
		CreatedAt:     source.text("created_at"),
		Status:        status,
		SourceSession: source.text("source_session"),
		PreserveLayer: true,
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

func madreExclusionDiscards(ctx context.Context, db *sql.DB) []parsers.Discard {
	var discards []parsers.Discard
	for _, exclusion := range madreExclusions {
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

func madreFlag(item row, key string) bool {
	value, ok := item.number(key)
	return ok && value != 0
}

func remapMadreSupersedes(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE memories
		SET supersedes = (
		  SELECT other.id FROM memories AS other
		  WHERE json_extract(other.metadata, '$._cron_source') =
		        json_extract(memories.metadata, '$._cron_source')
		    AND json_extract(other.metadata, '$.`+madreMemoryIDKey+`') =
		        json_extract(memories.metadata, '$.`+madreSupersedesKey+`')
		  LIMIT 1
		)
		WHERE json_extract(metadata, '$._cron_source') = ?
		  AND json_extract(metadata, '$.`+madreSupersedesKey+`') IS NOT NULL
		  AND supersedes IS NULL`, madreSource)
	if err != nil {
		return fmt.Errorf("remap legacy store supersedes: %w", err)
	}
	return nil
}
