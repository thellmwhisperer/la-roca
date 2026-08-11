package ingest

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strings"

	"github.com/thellmwhisperer/la-roca/internal/ingest/parsers"
)

// This file is the only one in the ingest that knows SQL. What
// arrives here is already normalized, so every decision left is about idempotency:
// what lands, what is left alone, and what is never rewritten.

// defaultLayer preserves content whose declared type the registry does not know.
const defaultLayer = "pattern"

// Counts is what one run wrote, per table. It is the delta the operator reads, and
// on a second pass over the same disk every one of these is zero.
type Counts struct {
	Sessions          int `json:"sessions"`
	SessionsUpdated   int `json:"sessions_updated"`
	Exchanges         int `json:"exchanges"`
	ThinkingBlocks    int `json:"thinking_blocks"`
	ToolUses          int `json:"tool_uses"`
	MemoriesInserted  int `json:"memories_inserted"`
	MemoriesUpdated   int `json:"memories_updated"`
	MemoriesUnchanged int `json:"memories_unchanged"`
	// ExchangesUnchanged and ExchangesChanged are what the sources that key on
	// their own exchange identity report: one already landed identical, the other
	// already landed and the source has since edited it. Neither is rewritten,
	// because an exchange that already answered a query cannot change under it.
	ExchangesUnchanged int `json:"exchanges_unchanged"`
	ExchangesChanged   int `json:"exchanges_changed"`
}

func (c *Counts) add(other Counts) {
	c.Sessions += other.Sessions
	c.SessionsUpdated += other.SessionsUpdated
	c.Exchanges += other.Exchanges
	c.ThinkingBlocks += other.ThinkingBlocks
	c.ToolUses += other.ToolUses
	c.MemoriesInserted += other.MemoriesInserted
	c.MemoriesUpdated += other.MemoriesUpdated
	c.MemoriesUnchanged += other.MemoriesUnchanged
	c.ExchangesUnchanged += other.ExchangesUnchanged
	c.ExchangesChanged += other.ExchangesChanged
}

// layerResolver turns a declared layer name into the physical one. It is the
// registry's job and it is passed in, so the writer does not decide vocabulary.
type layerResolver interface {
	Resolve(name, fallback string) string
}

// writer holds what every write of one run shares.
type writer struct {
	tx     *sql.Tx
	layers layerResolver
}

// WriteRecords writes one artefact's records and returns what it wrote.
func WriteRecords(ctx context.Context, tx *sql.Tx, layers layerResolver,
	records parsers.Records) (Counts, error) {
	w := &writer{tx: tx, layers: layers}
	var counts Counts
	for _, session := range records.Sessions {
		written, err := w.session(ctx, session)
		if err != nil {
			return counts, err
		}
		counts.add(written)
	}
	for _, memory := range records.Memories {
		written, err := w.memory(ctx, memory)
		if err != nil {
			return counts, err
		}
		counts.add(written)
	}
	return counts, nil
}

// exchangeKey is what a source that numbers its own exchanges remembers about one
// that already landed.
type exchangeKey struct {
	Number      int    `json:"exchange_number"`
	Fingerprint string `json:"fingerprint,omitempty"`
}

func (w *writer) session(ctx context.Context, session parsers.Session) (Counts, error) {
	var counts Counts
	if session.ID == "" {
		return counts, nil
	}

	current, exists, err := w.currentSession(ctx, session.ID)
	if err != nil {
		return counts, err
	}
	storedMetadata, staleSnapshot := staleSnapshotMetadata(session, current)
	if staleSnapshot {
		session.Snapshot = false
	}

	assigned, next, err := w.exchangeIdentities(ctx, session, current)
	if err != nil {
		return counts, err
	}

	metadata := map[string]any{}
	maps.Copy(metadata, session.Metadata)
	if staleSnapshot {
		metadata = mergeMetadata(metadata, storedMetadata)
	}
	if session.ParentID != "" {
		metadata["parent_session_id"] = session.ParentID
	}

	// The exchanges are written first only in the sense that their identities are
	// resolved first: the session row has to exist before any exchange can
	// reference it.
	if exists {
		counts.SessionsUpdated = 1
		if err := w.refreshSession(ctx, session, current); err != nil {
			return counts, err
		}
	} else {
		counts.Sessions = 1
		if err := w.insertSession(ctx, session); err != nil {
			return counts, err
		}
	}

	for _, exchange := range session.Exchanges {
		number := exchange.Number
		if exchange.SourceID != "" {
			known, landed := assigned[exchange.SourceID]
			if landed {
				// It is already in. Whether the source has edited it since is
				// reported, and fields that already landed are still not rewritten.
				thinking, err := w.enrichExchange(ctx, session.ID, known.Number, exchange)
				if err != nil {
					return counts, err
				}
				counts.ThinkingBlocks += thinking
				if known.Fingerprint == exchange.Fingerprint {
					counts.ExchangesUnchanged++
				} else {
					counts.ExchangesChanged++
				}
				continue
			}
			number = next
			next++
		}
		landed, err := w.exchange(ctx, session.ID, number, exchange)
		if err != nil {
			return counts, err
		}
		if !landed {
			thinking, err := w.enrichExchange(ctx, session.ID, number, exchange)
			if err != nil {
				return counts, err
			}
			counts.ThinkingBlocks += thinking
			continue
		}
		counts.Exchanges++
		if exchange.SourceID != "" {
			assigned[exchange.SourceID] = exchangeKey{
				Number: number, Fingerprint: exchange.Fingerprint,
			}
		}
		thinking, tools, err := w.children(ctx, session.ID, number, exchange)
		if err != nil {
			return counts, err
		}
		counts.ThinkingBlocks += thinking
		counts.ToolUses += tools
	}

	// A compact summary hangs off the session and off no exchange, so it is the
	// one thinking block with a natural key of its own.
	for _, block := range session.Thinking {
		landed, err := w.sessionThinking(ctx, session.ID, block)
		if err != nil {
			return counts, err
		}
		if landed {
			counts.ThinkingBlocks++
		}
	}

	if len(assigned) > 0 {
		putExchangeMap(metadata, session.ExchangeKeyScope, assigned)
	}
	if len(metadata) > 0 {
		if err := w.patchMetadata(ctx, session.ID, metadata); err != nil {
			return counts, err
		}
	}
	return counts, nil
}

// currentSession is what the database already holds for this id.
func (w *writer) currentSession(ctx context.Context, id string) (row, bool, error) {
	var agent, metadata sql.NullString
	err := w.tx.QueryRowContext(ctx,
		`SELECT source_agent, metadata FROM sessions WHERE session_id = ?`, id).
		Scan(&agent, &metadata)
	if errors.Is(err, sql.ErrNoRows) {
		return row{}, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("look up the session %s: %w", id, err)
	}
	return row{"source_agent": agent.String, "metadata": metadata.String}, true, nil
}

func staleSnapshotMetadata(session parsers.Session, current row) (map[string]any, bool) {
	if !session.Snapshot || session.SnapshotUpdatedAt == "" {
		return nil, false
	}
	var stored map[string]any
	if err := json.Unmarshal([]byte(current.text("metadata")), &stored); err != nil {
		return nil, false
	}
	updated, _ := stored["updated_at"].(string)
	return stored, parsers.ClaudeWebTimestampBefore(session.SnapshotUpdatedAt, updated)
}

func mergeMetadata(base, preferred map[string]any) map[string]any {
	merged := map[string]any{}
	maps.Copy(merged, base)
	for key, value := range preferred {
		baseMap, baseOK := merged[key].(map[string]any)
		preferredMap, preferredOK := value.(map[string]any)
		if baseOK && preferredOK {
			merged[key] = mergeMetadata(baseMap, preferredMap)
			continue
		}
		merged[key] = value
	}
	return merged
}

// insertSession registers a session on first sight. ON CONFLICT DO NOTHING scopes
// the idempotency to the primary key, so two processes racing over the same file
// insert it once.
func (w *writer) insertSession(ctx context.Context, session parsers.Session) error {
	_, err := w.tx.ExecContext(ctx, `
		INSERT INTO sessions
		  (session_id, source_agent, project, started_at, ended_at, duration_minutes, title, metadata)
		VALUES (?, ?, ?, ?, ?, ?, ?, '{}')
		ON CONFLICT(session_id) DO NOTHING`,
		session.ID, nullIfEmpty(session.SourceAgent), nullIfEmpty(session.Project),
		nullIfEmpty(session.StartedAt), nullIfEmpty(session.EndedAt),
		nullInt(session.DurationMinutes), nullIfEmpty(session.Title))
	if err != nil {
		return fmt.Errorf("register the session %s: %w", session.ID, err)
	}
	return nil
}

// refreshSession merges what was just observed into the row that is there.
//
// Two policies, and the difference matters. A snapshot artefact (a desktop
// metadata file, a database row) states the session's current state, so its
// non-empty fields win. Re-parsing a grown transcript does not: there the identity
// fields only fill NULLs, because a transcript re-read cannot know better than the
// metadata file that named the session.
//
// The title never overwrites a title that is already there: the first writer with
// a real one keeps it, or two sources would take turns renaming the session.
func (w *writer) refreshSession(ctx context.Context, session parsers.Session, current row) error {
	agent := w.agentAfterRefresh(session, current.text("source_agent"))
	project := any(nil)
	if session.Project != "" {
		project = session.Project
	}
	// The two policies differ in these four columns and in nothing else: a
	// snapshot states the project, the start, the end and the duration, while a
	// transcript re-read only fills their absence. ended_at and duration are
	// identity fields like the other two: a transcript re-read cannot know better
	// than the metadata file that named the session, so re-parsing it must not
	// clobber a value a snapshot already set. The argument order is the same
	// either way.
	setProject, setStarted, setEnded, setDuration :=
		"COALESCE(project, ?)", "COALESCE(started_at, ?)", "COALESCE(ended_at, ?)", "COALESCE(duration_minutes, ?)"
	if session.Snapshot {
		setProject, setStarted, setEnded, setDuration =
			"COALESCE(?, project)", "COALESCE(?, started_at)", "COALESCE(?, ended_at)", "COALESCE(?, duration_minutes)"
	}
	statement := fmt.Sprintf(`
		UPDATE sessions SET
		  source_agent = COALESCE(?, source_agent),
		  project = %s,
		  started_at = %s,
		  ended_at = %s,
		  duration_minutes = %s,
		  title = CASE WHEN TRIM(COALESCE(title, ''), CHAR(9,10,13,32,160)) <> '' THEN title
		               WHEN TRIM(COALESCE(?, ''), CHAR(9,10,13,32,160)) <> '' THEN ?
		               ELSE title END
		WHERE session_id = ?`, setProject, setStarted, setEnded, setDuration)
	_, err := w.tx.ExecContext(ctx, statement,
		nullIfEmpty(agent), project, nullIfEmpty(session.StartedAt),
		nullIfEmpty(session.EndedAt), nullInt(session.DurationMinutes),
		nullIfEmpty(session.Title), nullIfEmpty(session.Title), session.ID)
	if err != nil {
		return fmt.Errorf("refresh the session %s: %w", session.ID, err)
	}
	return nil
}

// agentAfterRefresh decides who a known session is attributed to.
//
// A snapshot's agent wins. A transcript re-read only fills an absence, with one
// exception: a source that has learned a more precise name inside its own family
// may replace the generic one it wrote before, which is how a Codex session filed
// as `codex` becomes `codex-<nickname>` once the state database is read. A name
// from another family never takes the row.
func (w *writer) agentAfterRefresh(session parsers.Session, current string) string {
	if session.SourceAgent == "" {
		return ""
	}
	if current == "" || session.Snapshot {
		return session.SourceAgent
	}
	if session.AgentMayUpgrade && agentFamily(current) == agentFamily(session.SourceAgent) {
		return session.SourceAgent
	}
	return ""
}

func agentFamily(agent string) string {
	family, _, _ := strings.Cut(agent, "-")
	return family
}

// exchangeIdentities reads what a source that numbers its own exchanges already
// landed, and where the next number starts.
func (w *writer) exchangeIdentities(ctx context.Context, session parsers.Session,
	current row) (map[string]exchangeKey, int, error) {
	assigned := map[string]exchangeKey{}
	if !keysItsOwnExchanges(session) {
		return assigned, 0, nil
	}
	assigned = readExchangeMap(current.text("metadata"), session.ExchangeKeyScope)

	var highest sql.NullInt64
	err := w.tx.QueryRowContext(ctx,
		`SELECT MAX(exchange_number) FROM exchanges WHERE session_id = ?`, session.ID).
		Scan(&highest)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, 0, fmt.Errorf("read the exchange numbers of %s: %w", session.ID, err)
	}
	next := int(highest.Int64)
	for _, known := range assigned {
		if known.Number > next {
			next = known.Number
		}
	}
	return assigned, next + 1, nil
}

func keysItsOwnExchanges(session parsers.Session) bool {
	for _, exchange := range session.Exchanges {
		if exchange.SourceID != "" {
			return true
		}
	}
	return session.ExchangeKeyScope != ""
}

// exchange inserts one exchange and says whether it landed.
//
// INSERT OR IGNORE plus changes() is the whole record-level contract: the unique
// index over (session_id, exchange_number) decides whether the normalized parent
// is new or eligible only for additive enrichment.
func (w *writer) exchange(ctx context.Context, sessionID string, number int,
	exchange parsers.Exchange) (bool, error) {
	provenance := exchange.Provenance
	result, err := w.tx.ExecContext(ctx, `
		INSERT OR IGNORE INTO exchanges
		  (session_id, exchange_number, is_after_compaction, human_text, agent_text,
		   human_timestamp, agent_timestamp, response_latency_ms,
		   model, provider, tokens_in, tokens_out, tokens_reasoning, cost_usd)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		sessionID, number, boolToInt(exchange.IsAfterCompaction),
		nullIfEmpty(exchange.HumanText), nullIfEmpty(exchange.AgentText),
		nullIfEmpty(exchange.HumanTimestamp), nullIfEmpty(exchange.AgentTimestamp),
		nullInt(exchange.LatencyMS),
		nullIfEmpty(provenance.Model), nullIfEmpty(provenance.Provider),
		nullInt(provenance.TokensIn), nullInt(provenance.TokensOut),
		nullInt(provenance.TokensReasoning), nullFloat(provenance.CostUSD))
	var affected int64
	if err == nil {
		affected, err = result.RowsAffected()
	}
	if err != nil {
		return false, fmt.Errorf("insert the exchange %s/%d: %w", sessionID, number, err)
	}
	if affected > 0 {
		return true, nil
	}
	return false, nil
}

func (w *writer) enrichExchange(ctx context.Context, sessionID string, number int,
	exchange parsers.Exchange) (int, error) {
	provenance := exchange.Provenance
	_, err := w.tx.ExecContext(ctx, `
		UPDATE exchanges SET
		  agent_text = COALESCE(agent_text, ?),
		  model = COALESCE(model, ?),
		  provider = COALESCE(provider, ?),
		  tokens_in = COALESCE(tokens_in, ?),
		  tokens_out = COALESCE(tokens_out, ?),
		  tokens_reasoning = COALESCE(tokens_reasoning, ?),
		  cost_usd = COALESCE(cost_usd, ?)
		WHERE session_id = ? AND exchange_number = ?`,
		nullIfEmpty(exchange.AgentText),
		nullIfEmpty(provenance.Model), nullIfEmpty(provenance.Provider),
		nullInt(provenance.TokensIn), nullInt(provenance.TokensOut),
		nullInt(provenance.TokensReasoning), nullFloat(provenance.CostUSD),
		sessionID, number)
	if err != nil {
		return 0, fmt.Errorf("enrich the exchange %s/%d: %w", sessionID, number, err)
	}
	inserted := 0
	for _, block := range exchange.Thinking {
		result, err := w.tx.ExecContext(ctx, `
			INSERT INTO thinking_blocks
			  (session_id, exchange_number, position_in_session, depth, word_count,
			   is_after_compaction, full_text)
			SELECT ?, ?, ?, ?, ?, ?, ?
			WHERE NOT EXISTS (
			  SELECT 1 FROM thinking_blocks
			  WHERE session_id = ? AND exchange_number = ? AND full_text = ?
			)`,
			sessionID, number, block.Position, nullIfEmpty(block.Depth), block.WordCount,
			boolToInt(block.IsAfterCompaction), block.Text,
			sessionID, number, block.Text)
		if err != nil {
			return inserted, fmt.Errorf("enrich a thinking block of %s/%d: %w", sessionID, number, err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return inserted, fmt.Errorf("count an enriched thinking block of %s/%d: %w", sessionID, number, err)
		}
		inserted += int(affected)
	}
	return inserted, nil
}

func (w *writer) children(ctx context.Context, sessionID string, number int,
	exchange parsers.Exchange) (int, int, error) {
	for _, block := range exchange.Thinking {
		_, err := w.tx.ExecContext(ctx, `
			INSERT INTO thinking_blocks
			  (session_id, exchange_number, position_in_session, depth, word_count,
			   is_after_compaction, full_text)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			sessionID, number, block.Position, nullIfEmpty(block.Depth), block.WordCount,
			boolToInt(block.IsAfterCompaction), block.Text)
		if err != nil {
			return 0, 0, fmt.Errorf("insert a thinking block of %s/%d: %w", sessionID, number, err)
		}
	}
	for _, tool := range exchange.Tools {
		_, err := w.tx.ExecContext(ctx, `
			INSERT INTO tool_uses
			  (session_id, exchange_number, tool_name, tool_params_summary, had_error, error_message)
			VALUES (?, ?, ?, ?, ?, ?)`,
			sessionID, number, tool.Name, nullIfEmpty(tool.ParamsSummary),
			boolToInt(tool.HadError), nullIfEmpty(tool.ErrorMessage))
		if err != nil {
			return 0, 0, fmt.Errorf("insert a tool use of %s/%d: %w", sessionID, number, err)
		}
	}
	return len(exchange.Thinking), len(exchange.Tools), nil
}

// sessionThinking writes a block that hangs off the session. Its natural key is
// its own text, because it has no exchange to be numbered under.
func (w *writer) sessionThinking(ctx context.Context, sessionID string,
	block parsers.Thinking) (bool, error) {
	var one int
	err := w.tx.QueryRowContext(ctx,
		`SELECT 1 FROM thinking_blocks WHERE session_id = ? AND full_text = ? LIMIT 1`,
		sessionID, block.Text).Scan(&one)
	if err == nil {
		return false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, fmt.Errorf("look up a thinking block of %s: %w", sessionID, err)
	}
	_, err = w.tx.ExecContext(ctx, `
		INSERT INTO thinking_blocks (session_id, depth, word_count, full_text)
		VALUES (?, ?, ?, ?)`,
		sessionID, nullIfEmpty(block.Depth), block.WordCount, block.Text)
	if err != nil {
		return false, fmt.Errorf("insert a thinking block of %s: %w", sessionID, err)
	}
	return true, nil
}

func (w *writer) patchMetadata(ctx context.Context, sessionID string, payload map[string]any) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode the metadata of %s: %w", sessionID, err)
	}
	_, err = w.tx.ExecContext(ctx, `
		UPDATE sessions SET metadata = json_patch(COALESCE(metadata, '{}'), ?)
		WHERE session_id = ?`, string(encoded), sessionID)
	if err != nil {
		return fmt.Errorf("patch the metadata of %s: %w", sessionID, err)
	}
	return nil
}

// memory writes one curated text.
//
// The natural key is the pair the metadata carries, `_cron_source` and
// `file_path`, and it is that pair and not the content: a file whose text changed
// is the same memory with new text, and inserting a second row would leave the
// corpus answering with the old one.
func (w *writer) memory(ctx context.Context, memory parsers.Memory) (Counts, error) {
	var counts Counts
	if memory.Content == "" {
		return counts, nil
	}
	metadata, err := json.Marshal(memory.Metadata)
	if err != nil {
		return counts, fmt.Errorf("encode the metadata of %s: %w", memory.FilePath, err)
	}

	var id int64
	var stored, storedMetadata string
	err = w.tx.QueryRowContext(ctx, `
		SELECT id, content, metadata FROM memories
		WHERE json_extract(metadata, '$._cron_source') = ?
		  AND json_extract(metadata, '$.file_path') = ?
		ORDER BY id LIMIT 1`, memory.Source, memory.FilePath).Scan(&id, &stored, &storedMetadata)
	freshness := claudeWebMemoryFreshness(memory, storedMetadata)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		layer := w.layers.Resolve(memory.Layer, defaultLayer)
		_, err := w.tx.ExecContext(ctx, `
			INSERT INTO memories
			  (layer, content, metadata, origin, source_agent, project, status, created_at)
			VALUES (?, ?, ?, ?, ?, ?, 'active', COALESCE(NULLIF(?, ''), datetime('now')))`,
			layer, memory.Content, string(metadata), memory.Origin,
			nullIfEmpty(memory.SourceAgent), nullIfEmpty(memory.Project), memory.CreatedAt)
		if err != nil {
			return counts, fmt.Errorf("insert the memory of %s: %w", memory.FilePath, err)
		}
		counts.MemoriesInserted = 1
	case err != nil:
		return counts, fmt.Errorf("look up the memory of %s: %w", memory.FilePath, err)
	case freshness < 0 || stored == memory.Content && freshness <= 0:
		// Same file, same text: nothing to do, and nothing written either. This is
		// what makes a second pass leave the database byte for byte as it was.
		counts.MemoriesUnchanged = 1
	default:
		_, err := w.tx.ExecContext(ctx,
			`UPDATE memories SET content = ?, metadata = ?,
			 created_at = COALESCE(NULLIF(?, ''), created_at) WHERE id = ?`,
			memory.Content, string(metadata), memory.CreatedAt, id)
		if err != nil {
			return counts, fmt.Errorf("update the memory of %s: %w", memory.FilePath, err)
		}
		counts.MemoriesUpdated = 1
	}
	return counts, nil
}

func claudeWebMemoryFreshness(memory parsers.Memory, storedMetadata string) int {
	identity, _ := memory.Metadata["memory_uuid"].(string)
	incoming, _ := memory.Metadata["updated_at"].(string)
	if memory.Source != "claude-web" || identity == "" || incoming == "" {
		return 0
	}
	var stored map[string]any
	if json.Unmarshal([]byte(storedMetadata), &stored) != nil {
		return 0
	}
	updated, _ := stored["updated_at"].(string)
	if parsers.ClaudeWebTimestampBefore(incoming, updated) {
		return -1
	}
	if parsers.ClaudeWebTimestampBefore(updated, incoming) {
		return 1
	}
	return 0
}

// readExchangeMap reads the exchange map out of a session's metadata, from the
// scope the source keeps it in.
func readExchangeMap(metadata, scope string) map[string]exchangeKey {
	assigned := map[string]exchangeKey{}
	if metadata == "" {
		return assigned
	}
	var document map[string]any
	if err := json.Unmarshal([]byte(metadata), &document); err != nil {
		return assigned
	}
	if scope != "" {
		nested, _ := document[scope].(map[string]any)
		document = nested
	}
	ids, _ := document["source_exchange_ids"].(map[string]any)
	fingerprints, _ := document["source_exchange_fingerprints"].(map[string]any)
	for id, value := range ids {
		key := exchangeKey{}
		switch typed := value.(type) {
		case float64:
			// OpenCode stores the bare number and the hashes beside it; Pi stores
			// the pair together. Both shapes remain readable across versions.
			key.Number = int(typed)
			if hash, ok := fingerprints[id].(string); ok {
				key.Fingerprint = hash
			}
		case map[string]any:
			if number, ok := typed["exchange_number"].(float64); ok {
				key.Number = int(number)
			}
			if hash, ok := typed["fingerprint"].(string); ok {
				key.Fingerprint = hash
			}
		}
		if key.Number > 0 {
			assigned[id] = key
		}
	}
	return assigned
}

// putExchangeMap writes the map back in the shape its own source keeps it in.
//
// Each adapter writes the exchange-map shape it also reads; using one shape for
// both would leave the other adapter unable to find its entries.
func putExchangeMap(metadata map[string]any, scope string, assigned map[string]exchangeKey) {
	ids := map[string]any{}
	fingerprints := map[string]any{}
	for id, key := range assigned {
		if scope == "" {
			ids[id] = map[string]any{
				"exchange_number": key.Number,
				"fingerprint":     key.Fingerprint,
			}
		} else {
			ids[id] = key.Number
		}
		fingerprints[id] = key.Fingerprint
	}
	into := metadata
	if scope != "" {
		nested, _ := metadata[scope].(map[string]any)
		if nested == nil {
			nested = map[string]any{}
		}
		metadata[scope] = nested
		into = nested
	}
	maps.Copy(into, map[string]any{
		"source_exchange_ids":          ids,
		"source_exchange_fingerprints": fingerprints,
	})
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func nullInt(value *int) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullFloat(value *float64) any {
	if value == nil {
		return nil
	}
	return *value
}
