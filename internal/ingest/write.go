package ingest

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strings"
	"time"

	"github.com/thellmwhisperer/la-roca/pkg/parsers"

	sqlite "modernc.org/sqlite"
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
	SessionsSkipped   int `json:"sessions_skipped"`
	Exchanges         int `json:"exchanges"`
	ThinkingBlocks    int `json:"thinking_blocks"`
	ToolUses          int `json:"tool_uses"`
	MemoriesInserted  int `json:"memories_inserted"`
	MemoriesUpdated   int `json:"memories_updated"`
	MemoriesUnchanged int `json:"memories_unchanged"`
	// ExchangesUnchanged and ExchangesChanged are what the sources that key on
	// their own exchange identity report. Ordinary changed readings are frozen;
	// a parser revision may explicitly replace a changed historical projection
	// when the source identity proves it is the same record under a new unit.
	ExchangesUnchanged      int `json:"exchanges_unchanged"`
	ExchangesChanged        int `json:"exchanges_changed"`
	ExchangesDeleted        int `json:"exchanges_deleted"`
	AnchorConflicts         int `json:"anchor_conflicts"`
	ThinkingBlocksDiscarded int `json:"thinking_blocks_discarded"`
}

func (c *Counts) add(other Counts) {
	c.Sessions += other.Sessions
	c.SessionsUpdated += other.SessionsUpdated
	c.SessionsSkipped += other.SessionsSkipped
	c.Exchanges += other.Exchanges
	c.ThinkingBlocks += other.ThinkingBlocks
	c.ToolUses += other.ToolUses
	c.MemoriesInserted += other.MemoriesInserted
	c.MemoriesUpdated += other.MemoriesUpdated
	c.MemoriesUnchanged += other.MemoriesUnchanged
	c.ExchangesUnchanged += other.ExchangesUnchanged
	c.ExchangesChanged += other.ExchangesChanged
	c.ExchangesDeleted += other.ExchangesDeleted
	c.AnchorConflicts += other.AnchorConflicts
	c.ThinkingBlocksDiscarded += other.ThinkingBlocksDiscarded
}

// layerResolver turns a declared layer name into the physical one. It is the
// registry's job and it is passed in, so the writer does not decide vocabulary.
type layerResolver interface {
	Resolve(name, fallback string) string
}

// writer holds what every write of one run shares.
type writer struct {
	tx                              *sql.Tx
	layers                          layerResolver
	hermesReservedMemories          *sql.DB
	preserveSessionThinkingPosition bool
}

// WriteRecords writes one artefact's records and returns what it wrote.
func WriteRecords(ctx context.Context, tx *sql.Tx, layers layerResolver,
	records parsers.Records) (Counts, error) {
	return writeRecords(ctx, tx, layers, nil, records)
}

// WriteSessions writes normalized conversations without the memory-specific
// dependencies used by WriteRecords. The public corpus writer and ingest both
// enter the same session insert path through this seam.
func WriteSessions(ctx context.Context, tx *sql.Tx,
	sessions []parsers.Session) (Counts, error) {
	return (&writer{tx: tx}).sessions(ctx, sessions)
}

func writeRecords(ctx context.Context, tx *sql.Tx, layers layerResolver,
	hermesReservedMemories *sql.DB, records parsers.Records) (Counts, error) {
	w := &writer{tx: tx, layers: layers, hermesReservedMemories: hermesReservedMemories}
	var counts Counts
	written, err := w.sessions(ctx, records.Sessions)
	counts.add(written)
	if err != nil {
		return counts, err
	}
	for _, memory := range records.Memories {
		written, err := w.memory(ctx, memory)
		if err != nil {
			return counts, err
		}
		counts.add(written)
	}
	if err := w.supersedeVanishedHermesBlocks(ctx, records.ObservedMemoryFiles, records.Memories, &counts); err != nil {
		return counts, err
	}
	return counts, nil
}

func (w *writer) sessions(ctx context.Context, sessions []parsers.Session) (Counts, error) {
	var counts Counts
	for _, session := range sessions {
		written, err := w.session(ctx, session)
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
	// Signal is how much the reading whose provenance the row carries had stated,
	// remembered across runs so a poorer reading of the same exchange ingested
	// later fills what is missing and takes nothing away.
	//
	// It is nil when nothing recorded it, which is every row a build before it
	// wrote. That is not the same as a reading that stated nothing, and reading it
	// as one would let any later reading take a provenance whose richness nobody
	// can vouch for, so an unrecorded signal orders nothing.
	Signal *int `json:"signal,omitempty"`
}

func (w *writer) session(ctx context.Context, session parsers.Session) (Counts, error) {
	return w.sessionWithPolicy(ctx, session, false)
}

func (w *writer) sessionIfMissing(ctx context.Context, session parsers.Session) (Counts, error) {
	return w.sessionWithPolicy(ctx, session, true)
}

func (w *writer) sessionWithPolicy(ctx context.Context, session parsers.Session,
	skipExisting bool) (Counts, error) {
	var counts Counts
	if session.ID == "" {
		return counts, nil
	}

	current, exists, err := w.currentSession(ctx, session.ID)
	if err != nil {
		return counts, err
	}
	if exists && skipExisting {
		counts.SessionsSkipped = 1
		return counts, nil
	}
	storedMetadata, staleSnapshot := staleSnapshotMetadata(session, current)
	if staleSnapshot {
		session.Snapshot = false
	}

	assigned, next, err := w.exchangeIdentities(ctx, session, current)
	if err != nil {
		return counts, err
	}
	deduplicateSourceAssignments(assigned, session.Exchanges)
	matcher, err := w.exchangeMatcher(ctx, session.ID, historyFallbackNumbers(current))
	if err != nil {
		return counts, err
	}
	claimAssignedExchanges(matcher, assigned, session.Exchanges)

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
		inserted, err := w.registerSession(ctx, session, skipExisting)
		if err != nil {
			if skipExisting && isExactPayloadConflict(err) {
				counts.SessionsSkipped = 1
				return counts, nil
			}
			return counts, err
		}
		if skipExisting && !inserted {
			counts.SessionsSkipped = 1
			return counts, nil
		}
		counts.Sessions = 1
	}

	for _, exchange := range session.Exchanges {
		number := exchange.Number
		known, identityKnown := assigned[exchange.SourceID]
		if exchange.SourceID != "" {
			if identityKnown {
				number = known.Number
			} else {
				number = next
			}
		}
		if identityKnown && exchange.RewriteOnIdentityChange &&
			known.Fingerprint != exchange.Fingerprint {
			stored, exists := matcher.byNumber[known.Number]
			// A message-level user row has no agent projection. This check makes
			// the rewrite a one-time migration of the old paired shape rather than
			// permission to mutate a user message that later changed.
			if exists && (stored.agentText != "" || stored.agentTimestamp != "") {
				thinking, tools, err := w.replaceExchange(ctx, session.ID, stored, exchange)
				if err != nil {
					return counts, err
				}
				matcher.claim(stored, number, exchange)
				assigned[exchange.SourceID] = exchangeKey{
					Number: stored.number, Fingerprint: exchange.Fingerprint, Signal: exchange.Signal,
				}
				counts.ExchangesChanged++
				counts.ThinkingBlocks += thinking
				counts.ToolUses += tools
				continue
			}
		}
		// A parser-provided number is the source identity inside this session.
		// A live transcript may be read while its last answer is partial and then
		// read again after it grows. Reusing that identity replaces the projection
		// and its children; allocating a fresh number would append a second copy of
		// the same source turn.
		if session.ExchangeNumbersAuthoritative && !identityKnown && exchange.SourceID == "" {
			if stored, exists := matcher.byNumber[number]; exists {
				_, conflicts := compareContent(stored, exchange)
				if conflicts {
					thinking, tools, err := w.replaceExchange(ctx, session.ID, stored, exchange)
					if err != nil {
						return counts, err
					}
					matcher.claim(stored, number, exchange)
					counts.ExchangesChanged++
					counts.ThinkingBlocks += thinking
					counts.ToolUses += tools
					continue
				}
			}
		}
		var matched storedExchange
		outcome := exchangeUnmatched
		if identityKnown {
			stored, mapped := matcher.byNumber[known.Number]
			if !mapped {
				delete(assigned, exchange.SourceID)
				identityKnown = false
				number = next
			} else {
				_, conflicts := compareContent(stored, exchange)
				if conflicts {
					matcher.claim(stored, number, exchange)
					counts.ExchangesChanged++
					continue
				}
				matched, outcome = stored, exchangeMatched
			}
		}
		if !identityKnown {
			matched, outcome = matcher.match(number, exchange, session.HistoryFallback)
		}
		if outcome == exchangeMatched {
			if !matched.numberValid {
				counts.ThinkingBlocksDiscarded += len(exchange.Thinking)
			}
			// A reading that stated more about this answer than the one the row
			// carries owns its provenance, in this run or in one months later.
			// Anything else, an unrecorded richness included, only fills what the
			// row is missing.
			richer := statedMore(exchange.Signal, known.Signal)
			thinking, tools, err := w.enrichExchange(ctx, session.ID, matched, exchange, richer)
			if err != nil {
				return counts, err
			}
			matcher.claim(matched, number, exchange)
			counts.ThinkingBlocks += thinking
			counts.ToolUses += tools
			if exchange.SourceID != "" && matched.numberValid {
				// The row keeps the richness of whichever reading its provenance came
				// from, so a fill leaves that record exactly as it found it.
				signal := known.Signal
				if richer {
					signal = exchange.Signal
				}
				assigned[exchange.SourceID] = exchangeKey{
					Number: matched.number, Fingerprint: exchange.Fingerprint, Signal: signal,
				}
			}
			if identityKnown {
				if known.Fingerprint == exchange.Fingerprint {
					counts.ExchangesUnchanged++
				} else {
					counts.ExchangesChanged++
				}
			}
			continue
		}
		if outcome == exchangeAnchorConflict || outcome == exchangeAmbiguous {
			counts.AnchorConflicts++
			continue
		}
		// A known source identity or a claimed historical row may already be
		// the historical row. Leaving it untouched is safer than manufacturing a
		// second copy that a later ingest cannot distinguish from the first.
		if identityKnown || outcome == exchangeAlreadyClaimed {
			if identityKnown {
				if known.Fingerprint == exchange.Fingerprint {
					counts.ExchangesUnchanged++
				} else {
					counts.ExchangesChanged++
				}
			}
			continue
		}
		if matcher.occupied(number) {
			number = matcher.freshNumber()
		}
		if number >= next {
			next = number + 1
		}
		exchangeID, landed, err := w.exchange(ctx, session.ID, number, exchange)
		if err != nil {
			return counts, err
		}
		if !landed {
			continue
		}
		matcher.occupy(exchangeID, number, exchange, session.HistoryFallback)
		if exchange.SourceID != "" {
			// A later record with another explicit source identity must not be
			// reconciled onto a row inserted earlier in this same read merely
			// because OpenCode gave sibling messages the same completion instant.
			matcher.claim(matcher.byNumber[number], number, exchange)
		}
		counts.Exchanges++
		if exchange.SourceID != "" {
			assigned[exchange.SourceID] = exchangeKey{
				Number: number, Fingerprint: exchange.Fingerprint, Signal: exchange.Signal,
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
	rewroteAssignments := false
	if session.Snapshot && session.PruneUnmappedExchanges {
		currentAssignments, complete := currentSourceAssignments(assigned, session.Exchanges)
		if complete {
			deleted, err := w.pruneUnmappedExchanges(ctx, session.ID, currentAssignments)
			if err != nil {
				return counts, err
			}
			counts.ExchangesDeleted += deleted
			assigned = currentAssignments
			rewroteAssignments = true
		}
	}

	if len(assigned) > 0 || rewroteAssignments {
		putExchangeMap(metadata, session.ExchangeKeyScope, assigned)
	}
	if len(metadata) > 0 {
		if err := w.patchMetadata(ctx, session.ID, metadata); err != nil {
			return counts, err
		}
	}
	return counts, nil
}

func currentSourceAssignments(assigned map[string]exchangeKey,
	exchanges []parsers.Exchange) (map[string]exchangeKey, bool) {
	current := make(map[string]exchangeKey, len(exchanges))
	numbers := map[int]bool{}
	for _, exchange := range exchanges {
		known, exists := assigned[exchange.SourceID]
		if exchange.SourceID == "" || !exists || known.Number <= 0 || numbers[known.Number] {
			return nil, false
		}
		numbers[known.Number] = true
		current[exchange.SourceID] = known
	}
	return current, true
}

func (w *writer) pruneUnmappedExchanges(ctx context.Context, sessionID string,
	assigned map[string]exchangeKey) (int, error) {
	keep := make([]int, 0, len(assigned))
	seen := map[int]bool{}
	for _, known := range assigned {
		if known.Number > 0 && !seen[known.Number] {
			seen[known.Number] = true
			keep = append(keep, known.Number)
		}
	}
	encoded, err := json.Marshal(keep)
	if err != nil {
		return 0, fmt.Errorf("encode exchange ownership of %s: %w", sessionID, err)
	}
	for _, table := range []string{"thinking_blocks", "tool_uses"} {
		_, err := w.tx.ExecContext(ctx, `DELETE FROM `+table+`
			WHERE session_id = ? AND exchange_number IS NOT NULL
			  AND exchange_number NOT IN (SELECT CAST(value AS INTEGER) FROM json_each(?))`,
			sessionID, string(encoded))
		if err != nil {
			return 0, fmt.Errorf("prune unmapped %s rows of %s: %w", table, sessionID, err)
		}
	}
	result, err := w.tx.ExecContext(ctx, `DELETE FROM exchanges
		WHERE session_id = ? AND (exchange_number IS NULL OR
		  exchange_number NOT IN (SELECT CAST(value AS INTEGER) FROM json_each(?)))`,
		sessionID, string(encoded))
	if err != nil {
		return 0, fmt.Errorf("prune unmapped exchanges of %s: %w", sessionID, err)
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("count pruned exchanges of %s: %w", sessionID, err)
	}
	return int(deleted), nil
}

// deduplicateSourceAssignments repairs an interrupted or older reading that
// mapped several explicit source records onto one exchange number. The first
// current source record owns the existing row; later identities become unknown
// and therefore land as their own exchanges during this read.
func deduplicateSourceAssignments(assigned map[string]exchangeKey,
	exchanges []parsers.Exchange) {
	owners := map[int]string{}
	for _, exchange := range exchanges {
		known, exists := assigned[exchange.SourceID]
		if !exists || exchange.SourceID == "" {
			continue
		}
		if _, owned := owners[known.Number]; !owned {
			owners[known.Number] = exchange.SourceID
		}
	}
	for sourceID, known := range assigned {
		if owner := owners[known.Number]; owner != "" && owner != sourceID {
			delete(assigned, sourceID)
		}
	}
}

// claimAssignedExchanges reserves rows already owned by current source identities
// before matching any newly discovered identity. Source order must not let an
// earlier new message steal the timestamp anchor of a later mapped message.
func claimAssignedExchanges(matcher *exchangeMatcher, assigned map[string]exchangeKey,
	exchanges []parsers.Exchange) {
	for _, exchange := range exchanges {
		known, exists := assigned[exchange.SourceID]
		stored, landed := matcher.byNumber[known.Number]
		if exchange.SourceID != "" && exists && landed {
			matcher.claim(stored, known.Number, exchange)
		}
	}
}

// replaceExchange changes the projection of one explicitly identified source
// record. OpenCode used this once when its historical user+assistant pair became
// one exchange per durable message; the old assistant children must leave the
// user row before their own message rows are inserted.
func (w *writer) replaceExchange(ctx context.Context, sessionID string,
	stored storedExchange, exchange parsers.Exchange) (int, int, error) {
	values := append(exchangeColumnValues(exchange), stored.id, sessionID)
	_, err := w.tx.ExecContext(ctx, `
		UPDATE exchanges SET
		  is_after_compaction = ?, human_text = ?, agent_text = ?,
		  human_timestamp = ?, agent_timestamp = ?, response_latency_ms = ?,
		  model = ?, provider = ?, tokens_in = ?, tokens_out = ?,
		  tokens_reasoning = ?, cost_usd = ?
		WHERE id = ? AND session_id = ?`, values...)
	if err != nil {
		return 0, 0, fmt.Errorf("replace exchange row %d of %s: %w", stored.id, sessionID, err)
	}
	if !stored.numberValid {
		return 0, 0, nil
	}
	if _, err := w.tx.ExecContext(ctx, `DELETE FROM thinking_blocks
		WHERE session_id = ? AND exchange_number = ?`, sessionID, stored.number); err != nil {
		return 0, 0, fmt.Errorf("replace thinking of %s/%d: %w", sessionID, stored.number, err)
	}
	if _, err := w.tx.ExecContext(ctx, `DELETE FROM tool_uses
		WHERE session_id = ? AND exchange_number = ?`, sessionID, stored.number); err != nil {
		return 0, 0, fmt.Errorf("replace tools of %s/%d: %w", sessionID, stored.number, err)
	}
	return w.children(ctx, sessionID, stored.number, exchange)
}

type storedExchange struct {
	id                             int64
	number                         int
	numberValid                    bool
	historyFallback                bool
	humanText, agentText           string
	humanTimestamp, agentTimestamp string
}

type timestampPair struct {
	human, agent timestampInstant
}

type timestampInstant struct {
	seconds     int64
	nanoseconds int
	present     bool
}

type exchangeMatch uint8

const (
	exchangeUnmatched exchangeMatch = iota
	exchangeMatched
	exchangeAmbiguous
	exchangeAnchorConflict
	exchangeAlreadyClaimed
)

type exchangeIdentity struct {
	sourceID   string
	timestamps timestampPair
	number     int
	kind       exchangeIdentityKind
}

type exchangeIdentityKind uint8

const (
	identityBySource exchangeIdentityKind = iota + 1
	identityByTimestamps
	identityByNumber
)

type exchangeMatcher struct {
	byNumber     map[int]storedExchange
	byTimestamps map[timestampPair][]storedExchange
	byContent    map[[sha256.Size]byte][]storedExchange
	byHuman      map[string][]storedExchange
	claimed      map[int64]exchangeIdentity
	nextNumber   int
}

type humanPrompt struct {
	text      string
	timestamp timestampInstant
}

func (w *writer) exchangeMatcher(ctx context.Context, sessionID string,
	historyNumbers map[int]bool) (*exchangeMatcher, error) {
	m := &exchangeMatcher{
		byNumber:     map[int]storedExchange{},
		byTimestamps: map[timestampPair][]storedExchange{},
		byContent:    map[[sha256.Size]byte][]storedExchange{},
		byHuman:      map[string][]storedExchange{},
		claimed:      map[int64]exchangeIdentity{},
		nextNumber:   1,
	}
	rows, err := w.tx.QueryContext(ctx, `
		SELECT id, exchange_number, COALESCE(human_text, ''), COALESCE(agent_text, ''),
		       COALESCE(human_timestamp, ''), COALESCE(agent_timestamp, '')
		FROM exchanges WHERE session_id = ?`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("read the exchange anchors of %s: %w", sessionID, err)
	}
	defer rows.Close()
	for rows.Next() {
		var stored storedExchange
		var number sql.NullInt64
		if err := rows.Scan(&stored.id, &number, &stored.humanText, &stored.agentText,
			&stored.humanTimestamp, &stored.agentTimestamp); err != nil {
			return nil, fmt.Errorf("read an exchange anchor of %s: %w", sessionID, err)
		}
		if number.Valid {
			stored.number = int(number.Int64)
			stored.numberValid = true
			stored.historyFallback = historyNumbers[stored.number]
		}
		m.addStored(stored)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read the exchange anchors of %s: %w", sessionID, err)
	}
	return m, nil
}

func (m *exchangeMatcher) occupied(number int) bool {
	_, exists := m.byNumber[number]
	return exists
}

func (m *exchangeMatcher) occupy(id int64, number int, exchange parsers.Exchange,
	historyFallback bool) {
	m.byNumber[number] = storedExchange{
		id: id, number: number, numberValid: true, historyFallback: historyFallback,
		humanText: exchange.HumanText, agentText: exchange.AgentText,
		humanTimestamp: exchange.HumanTimestamp, agentTimestamp: exchange.AgentTimestamp,
	}
	if number >= m.nextNumber {
		m.nextNumber = number + 1
	}
}

func (m *exchangeMatcher) freshNumber() int {
	for m.occupied(m.nextNumber) {
		m.nextNumber++
	}
	return m.nextNumber
}

func (m *exchangeMatcher) addStored(stored storedExchange) {
	if stored.numberValid {
		m.byNumber[stored.number] = stored
		if stored.number >= m.nextNumber {
			m.nextNumber = stored.number + 1
		}
	}
	if key, ok := timestampAnchor(stored.humanTimestamp, stored.agentTimestamp); ok {
		m.byTimestamps[key] = append(m.byTimestamps[key], stored)
	}
	if key, ok := contentAnchor(stored.humanText, stored.agentText); ok {
		m.byContent[key] = append(m.byContent[key], stored)
	}
	if _, ok := humanPromptAnchor(stored.humanText, stored.humanTimestamp); ok {
		m.byHuman[stored.humanText] = append(m.byHuman[stored.humanText], stored)
	}
}

func (m *exchangeMatcher) match(number int, exchange parsers.Exchange,
	historyFallback bool) (storedExchange, exchangeMatch) {
	identity := incomingIdentity(number, exchange)
	timestampsPresent := false
	timestampsAmbiguous := false
	if key, ok := timestampAnchor(exchange.HumanTimestamp, exchange.AgentTimestamp); ok {
		timestampsPresent = true
		stored := m.byTimestamps[key]
		candidates, sameClaim, claimed := m.unclaimed(stored, identity)
		if len(stored) > 1 && claimed {
			if len(candidates) != 1 || !compatibleContent(candidates[0], exchange) {
				return storedExchange{}, exchangeAnchorConflict
			}
			return candidates[0], exchangeMatched
		}
		if sameClaim {
			return storedExchange{}, exchangeAlreadyClaimed
		}
		if len(candidates) == 1 {
			_, conflicts := compareContent(candidates[0], exchange)
			if conflicts {
				return storedExchange{}, exchangeAnchorConflict
			}
			return candidates[0], exchangeMatched
		}
		if numbered, ok := numberedOriginal(candidates, number, exchange); ok {
			return numbered, exchangeMatched
		}
		if len(candidates) == 0 && len(stored) > 0 {
			return storedExchange{}, exchangeUnmatched
		}
		timestampsAmbiguous = len(candidates) > 1
	}
	if key, ok := contentAnchor(exchange.HumanText, exchange.AgentText); ok {
		stored := compatibleCandidates(m.byContent[key], exchange)
		candidates, sameClaim, _ := m.unclaimed(stored, identity)
		if sameClaim {
			return storedExchange{}, exchangeAlreadyClaimed
		}
		if len(candidates) == 1 {
			return candidates[0], exchangeMatched
		}
		if len(candidates) > 1 {
			return storedExchange{}, exchangeAmbiguous
		}
	}
	if key, ok := humanPromptAnchor(exchange.HumanText, exchange.HumanTimestamp); ok {
		stored := historyPromptCandidates(m.byHuman[key.text], key, historyFallback)
		candidates, sameClaim, _ := m.unclaimed(stored, identity)
		if sameClaim {
			return storedExchange{}, exchangeAlreadyClaimed
		}
		if closest, ok := closestHistoryPrompt(candidates, key, historyFallback); ok {
			if compatibleContent(closest, exchange) {
				return closest, exchangeMatched
			}
			return storedExchange{}, exchangeAnchorConflict
		}
		if len(candidates) > 1 {
			return storedExchange{}, exchangeAmbiguous
		}
	}
	if !timestampsPresent {
		if stored, ok := m.byNumber[number]; ok {
			matched, conflicts := compareContent(stored, exchange)
			if matched && conflicts {
				return storedExchange{}, exchangeAnchorConflict
			}
			if !matched || conflicts {
				return storedExchange{}, exchangeUnmatched
			}
			if claim, claimed := m.claimed[stored.id]; claimed {
				if claim == identity {
					return storedExchange{}, exchangeAlreadyClaimed
				}
				return storedExchange{}, exchangeUnmatched
			}
			return stored, exchangeMatched
		}
	}
	if timestampsAmbiguous {
		return storedExchange{}, exchangeAmbiguous
	}
	return storedExchange{}, exchangeUnmatched
}

const codexHistoryPromptGap = 30 * time.Second

func historyPromptCandidates(candidates []storedExchange, incoming humanPrompt,
	incomingFallback bool) []storedExchange {
	matched := make([]storedExchange, 0, len(candidates))
	for _, candidate := range candidates {
		stored, ok := humanPromptAnchor(candidate.humanText, candidate.humanTimestamp)
		if !ok || (!incomingFallback && !candidate.historyFallback) {
			continue
		}
		if candidate.historyFallback && incomingFallback {
			if stored.timestamp == incoming.timestamp {
				matched = append(matched, candidate)
			}
			continue
		}
		historyAt, rolloutAt := incoming.timestamp, stored.timestamp
		if candidate.historyFallback {
			historyAt, rolloutAt = stored.timestamp, incoming.timestamp
		}
		gap := instantTime(rolloutAt).Sub(instantTime(historyAt))
		if gap >= 0 && gap <= codexHistoryPromptGap {
			matched = append(matched, candidate)
		}
	}
	return matched
}

func closestHistoryPrompt(candidates []storedExchange, incoming humanPrompt,
	incomingFallback bool) (storedExchange, bool) {
	var closest storedExchange
	best := codexHistoryPromptGap + time.Nanosecond
	unique := false
	for _, candidate := range candidates {
		stored, _ := humanPromptAnchor(candidate.humanText, candidate.humanTimestamp)
		gap := instantTime(stored.timestamp).Sub(instantTime(incoming.timestamp))
		if candidate.historyFallback {
			gap = -gap
		}
		if incomingFallback && candidate.historyFallback {
			gap = 0
		}
		if gap < best {
			closest, best, unique = candidate, gap, true
		} else if gap == best {
			unique = false
		}
	}
	return closest, unique
}

// numberedOriginal recognizes the historical repair shape: one numbered row
// plus one or more numberless copies at the same instant. The number is a safe
// tiebreaker only when every copy agrees with the original wherever both sides
// carry text; groups of numbered peers remain ambiguous.
func numberedOriginal(candidates []storedExchange, number int,
	exchange parsers.Exchange) (storedExchange, bool) {
	var numbered storedExchange
	found := false
	for _, candidate := range candidates {
		if !candidate.numberValid {
			continue
		}
		if found || candidate.number != number {
			return storedExchange{}, false
		}
		numbered, found = candidate, true
	}
	if !found || len(candidates) < 2 {
		return storedExchange{}, false
	}
	if _, conflicts := compareContent(numbered, exchange); conflicts {
		return storedExchange{}, false
	}
	for _, candidate := range candidates {
		if candidate.numberValid {
			continue
		}
		duplicate := parsers.Exchange{
			HumanText: candidate.humanText, AgentText: candidate.agentText,
		}
		if _, conflicts := compareContent(numbered, duplicate); conflicts {
			return storedExchange{}, false
		}
	}
	return numbered, true
}

func (m *exchangeMatcher) claim(stored storedExchange, incomingNumber int,
	exchange parsers.Exchange) {
	m.claimed[stored.id] = incomingIdentity(incomingNumber, exchange)
}

func (m *exchangeMatcher) unclaimed(candidates []storedExchange,
	identity exchangeIdentity) ([]storedExchange, bool, bool) {
	available := make([]storedExchange, 0, len(candidates))
	var same, claimedAny bool
	for _, candidate := range candidates {
		claim, claimed := m.claimed[candidate.id]
		if !claimed {
			available = append(available, candidate)
		} else {
			claimedAny = true
			if claim == identity {
				same = true
			}
		}
	}
	return available, same, claimedAny
}

func incomingIdentity(number int, exchange parsers.Exchange) exchangeIdentity {
	if exchange.SourceID != "" {
		return exchangeIdentity{kind: identityBySource, sourceID: exchange.SourceID}
	}
	if timestamps, ok := timestampAnchor(exchange.HumanTimestamp, exchange.AgentTimestamp); ok {
		return exchangeIdentity{kind: identityByTimestamps, timestamps: timestamps}
	}
	return exchangeIdentity{kind: identityByNumber, number: number}
}

func compatibleCandidates(candidates []storedExchange, exchange parsers.Exchange) []storedExchange {
	matched := make([]storedExchange, 0, len(candidates))
	for _, candidate := range candidates {
		if compatibleContent(candidate, exchange) {
			matched = append(matched, candidate)
		}
	}
	return matched
}

func compatibleContent(stored storedExchange, exchange parsers.Exchange) bool {
	matched, conflicts := compareContent(stored, exchange)
	return matched && !conflicts
}

func compareContent(stored storedExchange, exchange parsers.Exchange) (bool, bool) {
	matched := false
	for _, pair := range [][2]string{
		{stored.humanText, exchange.HumanText},
		{stored.agentText, exchange.AgentText},
	} {
		if pair[0] == "" || pair[1] == "" {
			continue
		}
		if pair[0] != pair[1] {
			return matched, true
		}
		matched = true
	}
	return matched, false
}

func timestampAnchor(human, agent string) (timestampPair, bool) {
	if human == "" && agent == "" {
		return timestampPair{}, false
	}
	humanInstant, humanOK := parseTimestampInstant(human)
	agentInstant, agentOK := parseTimestampInstant(agent)
	if !humanOK || !agentOK {
		return timestampPair{}, false
	}
	return timestampPair{human: humanInstant, agent: agentInstant}, true
}

func humanPromptAnchor(text, timestamp string) (humanPrompt, bool) {
	instant, ok := parseTimestampInstant(timestamp)
	if strings.TrimSpace(text) == "" || !ok || !instant.present {
		return humanPrompt{}, false
	}
	return humanPrompt{text: text, timestamp: instant}, true
}

func instantTime(value timestampInstant) time.Time {
	return time.Unix(value.seconds, int64(value.nanoseconds))
}

func parseTimestampInstant(value string) (timestampInstant, bool) {
	if value == "" {
		return timestampInstant{}, true
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return timestampInstant{}, false
	}
	return timestampInstant{
		seconds: parsed.Unix(), nanoseconds: parsed.Nanosecond(), present: true,
	}, true
}

func contentAnchor(human, agent string) ([sha256.Size]byte, bool) {
	if human == "" && agent == "" {
		return [sha256.Size]byte{}, false
	}
	hash := sha256.New()
	var length [8]byte
	for _, component := range []string{human, agent} {
		binary.BigEndian.PutUint64(length[:], uint64(len(component)))
		_, _ = hash.Write(length[:])
		_, _ = hash.Write([]byte(component))
	}
	var anchor [sha256.Size]byte
	copy(anchor[:], hash.Sum(nil))
	return anchor, true
}

// currentSession is what the database already holds for this id.
func (w *writer) currentSession(ctx context.Context, id string) (row, bool, error) {
	var agent, surface, metadata sql.NullString
	err := w.tx.QueryRowContext(ctx,
		`SELECT source_agent, source_surface, metadata FROM sessions WHERE session_id = ?`, id).
		Scan(&agent, &surface, &metadata)
	if errors.Is(err, sql.ErrNoRows) {
		return row{}, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("look up the session %s: %w", id, err)
	}
	return row{"source_agent": agent.String, "source_surface": surface.String,
		"metadata": metadata.String}, true, nil
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
	_, err := w.registerSession(ctx, session, false)
	return err
}

// registerSession inserts a session row. When anyUnique is set, an exact-payload
// collision is treated like a primary-key miss: the statement is ignored and
// the caller skips children instead of aborting the batch.
func (w *writer) registerSession(ctx context.Context, session parsers.Session,
	anyUnique bool) (bool, error) {
	conflict := "ON CONFLICT(session_id) DO NOTHING"
	if anyUnique {
		conflict = "ON CONFLICT DO NOTHING"
	}
	result, err := w.tx.ExecContext(ctx, `
		INSERT INTO sessions
		  (session_id, source_agent, source_surface, project, started_at, ended_at,
		   duration_minutes, title, metadata)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, '{}')
		`+conflict,
		session.ID, nullIfEmpty(session.SourceAgent), nullIfEmpty(session.SourceSurface),
		nullIfEmpty(session.Project),
		nullIfEmpty(session.StartedAt), nullIfEmpty(session.EndedAt),
		nullInt(session.DurationMinutes), nullIfEmpty(session.Title))
	if err != nil {
		return false, fmt.Errorf("register the session %s: %w", session.ID, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("register the session %s: %w", session.ID, err)
	}
	return affected > 0, nil
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
	surface := any(nil)
	if session.SourceSurface != "" {
		surface = session.SourceSurface
	}
	project := any(nil)
	if session.Project != "" {
		project = session.Project
	}
	// The two policies differ in these five columns and in nothing else: a
	// snapshot states the project, the start, the end and the duration, while a
	// transcript re-read only fills their absence. ended_at and duration are
	// identity fields like the other two: a transcript re-read cannot know better
	// than the metadata file that named the session, so re-parsing it must not
	// clobber a value a snapshot already set. The argument order is the same
	// either way.
	setSurface, setProject, setStarted, setEnded, setDuration :=
		"COALESCE(source_surface, ?)",
		"COALESCE(project, ?)", "COALESCE(started_at, ?)", "COALESCE(ended_at, ?)", "COALESCE(duration_minutes, ?)"
	if session.Snapshot {
		setSurface, setProject, setStarted, setEnded, setDuration =
			"COALESCE(?, source_surface)",
			"COALESCE(?, project)", "COALESCE(?, started_at)", "COALESCE(?, ended_at)", "COALESCE(?, duration_minutes)"
	}
	statement := fmt.Sprintf(`
		UPDATE sessions SET
		  source_agent = COALESCE(?, source_agent),
		  source_surface = %s,
		  project = %s,
		  started_at = %s,
		  ended_at = %s,
		  duration_minutes = %s,
		  title = CASE WHEN TRIM(COALESCE(title, ''), CHAR(9,10,13,32,160)) <> '' THEN title
		               WHEN TRIM(COALESCE(?, ''), CHAR(9,10,13,32,160)) <> '' THEN ?
		               ELSE title END
		WHERE session_id = ?`, setSurface, setProject, setStarted, setEnded, setDuration)
	_, err := w.tx.ExecContext(ctx, statement,
		nullIfEmpty(agent), surface, project, nullIfEmpty(session.StartedAt),
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
// The matcher has already ruled out a historical row and selected an unoccupied
// number. INSERT OR IGNORE plus the exact-payload unique index remains the final
// guard against a concurrent exact duplicate.
func (w *writer) exchange(ctx context.Context, sessionID string, number int,
	exchange parsers.Exchange) (int64, bool, error) {
	values := append([]any{sessionID, number}, exchangeColumnValues(exchange)...)
	result, err := w.tx.ExecContext(ctx, `
		INSERT OR IGNORE INTO exchanges
		  (session_id, exchange_number, is_after_compaction, human_text, agent_text,
		   human_timestamp, agent_timestamp, response_latency_ms,
		   model, provider, tokens_in, tokens_out, tokens_reasoning, cost_usd)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, values...)
	var affected int64
	if err == nil {
		affected, err = result.RowsAffected()
	}
	if err != nil {
		return 0, false, fmt.Errorf("insert the exchange %s/%d: %w", sessionID, number, err)
	}
	if affected > 0 {
		id, err := result.LastInsertId()
		if err != nil {
			return 0, false, fmt.Errorf("read the inserted exchange id %s/%d: %w",
				sessionID, number, err)
		}
		return id, true, nil
	}
	return 0, false, nil
}

func exchangeColumnValues(exchange parsers.Exchange) []any {
	values := []any{
		boolToInt(exchange.IsAfterCompaction), nullIfEmpty(exchange.HumanText),
		nullIfEmpty(exchange.AgentText), nullIfEmpty(exchange.HumanTimestamp),
		nullIfEmpty(exchange.AgentTimestamp), nullInt(exchange.LatencyMS),
	}
	return append(values, exchangeProvenanceValues(exchange.Provenance)...)
}

func exchangeProvenanceValues(provenance parsers.Provenance) []any {
	return []any{
		nullIfEmpty(provenance.Model), nullIfEmpty(provenance.Provider),
		nullInt(provenance.TokensIn), nullInt(provenance.TokensOut),
		nullInt(provenance.TokensReasoning), nullFloat(provenance.CostUSD),
	}
}

// enrichExchange fills what the row is missing from a later reading of the same
// exchange, and lets a reading that stated more about the answer than the one the
// row carries state its provenance instead. Even then it overwrites nothing the
// source left unsaid: a NULL is the absence of a statement, not a zero.
func (w *writer) enrichExchange(ctx context.Context, sessionID string, stored storedExchange,
	exchange parsers.Exchange, richer bool) (int, int, error) {
	provenance := exchange.Provenance
	provenanceColumns := `
		  model = COALESCE(model, ?),
		  provider = COALESCE(provider, ?),
		  tokens_in = COALESCE(tokens_in, ?),
		  tokens_out = COALESCE(tokens_out, ?),
		  tokens_reasoning = COALESCE(tokens_reasoning, ?),
		  cost_usd = COALESCE(cost_usd, ?)`
	if richer {
		provenanceColumns = `
		  model = COALESCE(?, model),
		  provider = COALESCE(?, provider),
		  tokens_in = COALESCE(?, tokens_in),
		  tokens_out = COALESCE(?, tokens_out),
		  tokens_reasoning = COALESCE(?, tokens_reasoning),
		  cost_usd = COALESCE(?, cost_usd)`
	}
	values := []any{
		nullIfEmpty(exchange.AgentText), nullIfEmpty(exchange.AgentTimestamp),
		nullInt(exchange.LatencyMS), boolToInt(exchange.IsAfterCompaction),
	}
	values = append(values, exchangeProvenanceValues(provenance)...)
	values = append(values, stored.id, sessionID)
	_, err := w.tx.ExecContext(ctx, `
		UPDATE exchanges SET
		  agent_text = COALESCE(agent_text, ?),
		  agent_timestamp = COALESCE(agent_timestamp, ?),
		  response_latency_ms = COALESCE(response_latency_ms, ?),
		  is_after_compaction = MAX(COALESCE(is_after_compaction, 0), ?),`+
		provenanceColumns+`
		WHERE id = ? AND session_id = ?`, values...)
	if err != nil {
		return 0, 0, fmt.Errorf("enrich exchange row %d of %s: %w", stored.id, sessionID, err)
	}
	if !stored.numberValid {
		return 0, 0, nil
	}
	number := stored.number
	inserted := 0
	for _, block := range exchange.Thinking {
		landed, err := w.insertThinking(ctx, sessionID, number, block.Position, block)
		if err != nil {
			return inserted, 0, fmt.Errorf("enrich a thinking block of %s/%d: %w", sessionID, number, err)
		}
		if landed {
			inserted++
		}
	}
	if stored.agentText != "" || stored.agentTimestamp != "" {
		return inserted, 0, nil
	}
	tools, err := w.insertTools(ctx, sessionID, number, exchange.Tools)
	return inserted, tools, err
}

func (w *writer) insertTools(ctx context.Context, sessionID string, number int,
	tools []parsers.ToolUse) (int, error) {
	inserted := 0
	for _, tool := range tools {
		_, err := w.tx.ExecContext(ctx, `
			INSERT INTO tool_uses
			  (session_id, exchange_number, tool_name, tool_params_summary, had_error,
			   error_message, initiative_type)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			sessionID, number, tool.Name, nullIfEmpty(tool.ParamsSummary),
			boolToInt(tool.HadError), nullIfEmpty(tool.ErrorMessage),
			nullIfEmpty(tool.InitiativeType))
		if err != nil {
			if isExactPayloadConflict(err) || isUniqueConstraint(err) {
				continue
			}
			return inserted, fmt.Errorf("insert a tool use of %s/%d: %w", sessionID, number, err)
		}
		inserted++
	}
	return inserted, nil
}

func (w *writer) children(ctx context.Context, sessionID string, number int,
	exchange parsers.Exchange) (int, int, error) {
	inserted := 0
	for _, block := range exchange.Thinking {
		landed, err := w.insertThinking(ctx, sessionID, number, block.Position, block)
		if err != nil {
			return 0, 0, fmt.Errorf("insert a thinking block of %s/%d: %w", sessionID, number, err)
		}
		if landed {
			inserted++
		}
	}
	tools, err := w.insertTools(ctx, sessionID, number, exchange.Tools)
	if err != nil {
		return 0, 0, err
	}
	return inserted, tools, nil
}

func (w *writer) insertThinking(ctx context.Context, sessionID string, number, position any,
	block parsers.Thinking) (bool, error) {
	depth, compacted := nullIfEmpty(block.Depth), boolToInt(block.IsAfterCompaction)
	caution := nullFloat(block.CautionRatio)
	result, err := w.tx.ExecContext(ctx, `
		INSERT INTO thinking_blocks
		  (session_id, exchange_number, position_in_session, depth, caution_ratio,
		   word_count, is_after_compaction, full_text)
		SELECT ?, ?, ?, ?, ?, ?, ?, ?
		WHERE NOT EXISTS (
		  SELECT 1 FROM thinking_blocks
		  WHERE session_id IS ? AND exchange_number IS ? AND position_in_session IS ?
		    AND depth IS ? AND caution_ratio IS ? AND word_count IS ?
		    AND is_after_compaction IS ? AND full_text IS ?
		)`, sessionID, number, position, depth, caution, block.WordCount, compacted, block.Text,
		sessionID, number, position, depth, caution, block.WordCount, compacted, block.Text)
	if err != nil {
		if isExactPayloadConflict(err) || isUniqueConstraint(err) {
			return false, nil
		}
		return false, err
	}
	affected, err := result.RowsAffected()
	return affected != 0, err
}

// sessionThinking writes a block that hangs off the session rather than an exchange.
func (w *writer) sessionThinking(ctx context.Context, sessionID string,
	block parsers.Thinking) (bool, error) {
	var position any
	if w.preserveSessionThinkingPosition {
		position = block.Position
	}
	landed, err := w.insertThinking(ctx, sessionID, nil, position, block)
	if err != nil {
		return false, fmt.Errorf("insert a thinking block of %s: %w", sessionID, err)
	}
	return landed, nil
}

func (w *writer) patchMetadata(ctx context.Context, sessionID string, payload map[string]any) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode the metadata of %s: %w", sessionID, err)
	}
	_, err = w.tx.ExecContext(ctx, `
		UPDATE sessions SET metadata = json_patch(COALESCE(metadata, '{}'), ?)
		WHERE session_id = ?`, string(encoded), sessionID)
	if isSessionExactPayloadConflict(err) {
		// The patched payload would match another session row. Exact-dedup
		// forbids that duplicate; leaving this row's metadata unchanged keeps
		// the artefact writable and the unique index intact.
		return nil
	}
	if err != nil {
		return fmt.Errorf("patch the metadata of %s: %w", sessionID, err)
	}
	return nil
}

// SQLITE_CONSTRAINT_UNIQUE is 2067; the primary constraint class is 19.
const sqliteConstraintUnique = 2067

func isUniqueConstraint(err error) bool {
	var serr *sqlite.Error
	if errors.As(err, &serr) {
		return serr.Code() == sqliteConstraintUnique || serr.Code()&0xff == 19
	}
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

func isExactPayloadConflict(err error) bool {
	return err != nil && strings.Contains(err.Error(), "_exact_payload")
}

func isSessionExactPayloadConflict(err error) bool {
	if err == nil || !strings.Contains(err.Error(), "idx_sessions_exact_payload") {
		return false
	}
	var serr *sqlite.Error
	if errors.As(err, &serr) {
		return serr.Code() == sqliteConstraintUnique || serr.Code()&0xff == 19
	}
	return true
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
	var stored, storedMetadata, storedProject string
	err = w.tx.QueryRowContext(ctx, `
		SELECT id, content, metadata, COALESCE(project, '') FROM memories
		WHERE json_extract(metadata, '$._cron_source') = ?
		  AND json_extract(metadata, '$.file_path') = ?
		ORDER BY id LIMIT 1`, memory.Source, memory.FilePath).
		Scan(&id, &stored, &storedMetadata, &storedProject)
	if errors.Is(err, sql.ErrNoRows) && memory.Source == "hermes" {
		err = w.tx.QueryRowContext(ctx, `
			SELECT id, content, metadata, COALESCE(project, '') FROM memories
			WHERE id BETWEEN 1152921504606847051 AND 1152921504606847059
			  AND content = ? AND status = 'active' ORDER BY id LIMIT 1`, memory.Content).
			Scan(&id, &stored, &storedMetadata, &storedProject)
	}
	if errors.Is(err, sql.ErrNoRows) && memory.Source == "hermes" &&
		w.hermesReservedMemories != nil {
		var found int
		err = w.hermesReservedMemories.QueryRowContext(ctx, `
			SELECT 1 FROM memories
			WHERE id BETWEEN 1152921504606847051 AND 1152921504606847059
			  AND content = ? AND status = 'active' LIMIT 1`, memory.Content).Scan(&found)
		if err == nil {
			counts.MemoriesUnchanged = 1
			return counts, nil
		}
	}
	freshness := claudeWebMemoryFreshness(memory, storedMetadata)
	authoritative := memory.ProjectFromCwd && memory.Project != ""
	projectChange := memory.Project != "" && storedProject != memory.Project &&
		(storedProject == "" || authoritative)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		layer := memory.Layer
		if !memory.PreserveLayer {
			layer = w.layers.Resolve(memory.Layer, defaultLayer)
		}
		var status any = memory.Status
		if memory.PreserveState {
			status = nullIfEmpty(memory.Status)
		} else if memory.Status == "" {
			status = "active"
		}
		createdAt := "COALESCE(NULLIF(?, ''), datetime('now'))"
		if memory.PreserveState {
			createdAt = "NULLIF(?, '')"
		}
		_, err := w.tx.ExecContext(ctx, fmt.Sprintf(`
			INSERT INTO memories
			  (layer, content, metadata, origin, source_agent, source_model, source_surface,
			   source_session, source_sequence, project, status, supersedes, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, %s)`, createdAt),
			layer, memory.Content, string(metadata), memory.Origin,
			nullIfEmpty(memory.SourceAgent), nullIfEmpty(memory.SourceModel),
			nullIfEmpty(memory.SourceSurface), nullIfEmpty(memory.SourceSession),
			nullInt(memory.SourceSequence), nullIfEmpty(memory.Project), status,
			nil, memory.CreatedAt)
		if err != nil {
			if isExactPayloadConflict(err) || isUniqueConstraint(err) {
				counts.MemoriesUnchanged = 1
				return counts, nil
			}
			return counts, fmt.Errorf("insert the memory of %s: %w", memory.FilePath, err)
		}
		counts.MemoriesInserted = 1
	case err != nil:
		return counts, fmt.Errorf("look up the memory of %s: %w", memory.FilePath, err)
	case (freshness < 0 || stored == memory.Content && freshness <= 0) && projectChange:
		_, err := w.tx.ExecContext(ctx,
			`UPDATE memories SET project = ? WHERE id = ?`, memory.Project, id)
		if err != nil {
			return counts, fmt.Errorf("attribute the memory of %s: %w", memory.FilePath, err)
		}
		counts.MemoriesUpdated = 1
	case stored == memory.Content && hermesNeedsIdentityStamp(memory, storedMetadata):
		_, err := w.tx.ExecContext(ctx, `
			UPDATE memories SET metadata = ?,
			 source_agent = COALESCE(NULLIF(source_agent, ''), ?),
			 source_surface = COALESCE(source_surface, ?)
			WHERE id = ?`, string(metadata), nullIfEmpty(memory.SourceAgent),
			nullIfEmpty(memory.SourceSurface), id)
		if err != nil {
			return counts, fmt.Errorf("stamp the memory of %s: %w", memory.FilePath, err)
		}
		counts.MemoriesUpdated = 1
	case freshness < 0 || stored == memory.Content && freshness <= 0:
		// Same file, same text: nothing to do, and nothing written either. This is
		// what makes a second pass leave the database byte for byte as it was.
		counts.MemoriesUnchanged = 1
	default:
		project := memory.Project
		if !authoritative && storedProject != "" {
			project = storedProject
		}
		_, err := w.tx.ExecContext(ctx,
			`UPDATE memories SET content = ?, metadata = ?,
			 source_model = COALESCE(source_model, ?),
			 source_surface = COALESCE(source_surface, ?),
			 project = ?,
			 created_at = COALESCE(NULLIF(?, ''), created_at) WHERE id = ?`,
			memory.Content, string(metadata), nullIfEmpty(memory.SourceModel),
			nullIfEmpty(memory.SourceSurface), nullIfEmpty(project), memory.CreatedAt, id)
		if err != nil {
			return counts, fmt.Errorf("update the memory of %s: %w", memory.FilePath, err)
		}
		counts.MemoriesUpdated = 1
	}
	return counts, nil
}

func hermesNeedsIdentityStamp(memory parsers.Memory, storedMetadata string) bool {
	if memory.Source != "hermes" {
		return false
	}
	hash, _ := memory.Metadata["block_hash"].(string)
	file, _ := memory.Metadata["aggregate_file_path"].(string)
	if hash == "" || file == "" {
		return false
	}
	var stored map[string]any
	if json.Unmarshal([]byte(storedMetadata), &stored) != nil {
		return true
	}
	return stored["block_hash"] != hash || stored["aggregate_file_path"] != file
}

func (w *writer) supersedeVanishedHermesBlocks(ctx context.Context, observedFiles []string,
	memories []parsers.Memory, counts *Counts) error {
	current := map[string]map[string]bool{}
	for _, file := range observedFiles {
		if file != "" {
			current[file] = map[string]bool{}
		}
	}
	for _, memory := range memories {
		file, _ := memory.Metadata["aggregate_file_path"].(string)
		hash, _ := memory.Metadata["block_hash"].(string)
		if file == "" || hash == "" {
			continue
		}
		if current[file] == nil {
			current[file] = map[string]bool{}
		}
		current[file][hash] = true
	}
	for file, hashes := range current {
		rows, err := w.tx.QueryContext(ctx, `
			SELECT id, COALESCE(json_extract(metadata, '$.block_hash'), '') FROM memories
			WHERE status = 'active' AND json_extract(metadata, '$.aggregate_file_path') = ?`, file)
		if err != nil {
			return fmt.Errorf("look up vanished Hermes blocks of %s: %w", file, err)
		}
		var vanished []int64
		for rows.Next() {
			var id int64
			var hash string
			if err := rows.Scan(&id, &hash); err != nil {
				rows.Close()
				return fmt.Errorf("read a vanished Hermes block of %s: %w", file, err)
			}
			if !hashes[hash] {
				vanished = append(vanished, id)
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		for _, id := range vanished {
			_, err := w.tx.ExecContext(ctx, `
				UPDATE memories SET status = 'resolved',
				 metadata = json_patch(COALESCE(metadata, '{}'), '{"superseded":true}')
				WHERE id = ?`, id)
			if err != nil {
				return fmt.Errorf("mark vanished Hermes memory %d superseded: %w", id, err)
			}
			counts.MemoriesUpdated++
		}
	}
	return nil
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
	signals, _ := document["source_exchange_signal"].(map[string]any)
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
		if signal, ok := signals[id].(float64); ok {
			stated := int(signal)
			key.Signal = &stated
		}
		if key.Number > 0 {
			assigned[id] = key
		}
	}
	return assigned
}

func historyFallbackNumbers(current row) map[int]bool {
	numbers := map[int]bool{}
	for id, key := range readExchangeMap(current.text("metadata"), "") {
		if strings.HasPrefix(id, "codex-history:") {
			numbers[key.Number] = true
		}
	}
	return numbers
}

// putExchangeMap writes the map back in the shape its own source keeps it in.
//
// Each adapter writes the exchange-map shape it also reads; using one shape for
// both would leave the other adapter unable to find its entries.
func putExchangeMap(metadata map[string]any, scope string, assigned map[string]exchangeKey) {
	if len(assigned) == 0 {
		if scope != "" {
			metadata[scope] = nil
		} else {
			metadata["source_exchange_ids"] = nil
			metadata["source_exchange_fingerprints"] = nil
			metadata["source_exchange_signal"] = nil
		}
		return
	}
	ids := map[string]any{}
	fingerprints := map[string]any{}
	signals := map[string]any{}
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
		if key.Signal != nil {
			signals[id] = *key.Signal
		}
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
	// Only a source that measures per-answer richness carries this key, so a source
	// that measures none keeps the metadata it always had. Every exchange of a
	// source that does measure it is recorded, a measured zero included: leaving
	// that one out is what would make "stated nothing" read as "nobody looked".
	if len(signals) > 0 {
		into["source_exchange_signal"] = signals
	} else {
		into["source_exchange_signal"] = nil
	}
}

// statedMore says whether an incoming reading measurably said more about an
// answer than the reading whose provenance the row carries. Two measurements are
// needed to answer it: one that nobody recorded is not a low one.
func statedMore(incoming, stored *int) bool {
	return incoming != nil && stored != nil && *incoming > *stored
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
