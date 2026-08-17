package corpusarchive

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"

	"github.com/thellmwhisperer/la-roca/internal/distribution/migrationledger"
)

type archiveTable struct {
	sourceTable      string
	migration        string
	destinationTable string
	query            string
	scan             func(*sql.Rows, *occurrenceTracker) (archiveRecord, error)
}

type archiveRecord struct {
	sourceKey        string
	digest           string
	destinationTable string
	values           []any
	sourceRowID      sql.NullInt64
	sessionID        sql.NullString
	exchangeNumber   sql.NullInt64
	ordinal          sql.NullInt64
	statePath        sql.NullString
}

type occurrenceTracker struct {
	parent string
	counts map[string]int64
}

type exchangePayload struct {
	sessionID                            sql.NullString
	number, compacted, latency           sql.NullInt64
	human, agent, humanAt, agentAt       sql.NullString
	model, provider                      sql.NullString
	tokensIn, tokensOut, tokensReasoning sql.NullInt64
	cost                                 sql.NullFloat64
}

func scanExchangePayload(rows *sql.Rows, identity any) (exchangePayload, error) {
	var payload exchangePayload
	err := rows.Scan(identity, &payload.sessionID, &payload.number, &payload.compacted,
		&payload.human, &payload.agent, &payload.humanAt, &payload.agentAt,
		&payload.latency, &payload.model, &payload.provider, &payload.tokensIn,
		&payload.tokensOut, &payload.tokensReasoning, &payload.cost)
	return payload, err
}

func (payload exchangePayload) values() []any {
	return []any{payload.sessionID.String, payload.number, payload.compacted,
		payload.human, payload.agent, payload.humanAt, payload.agentAt, payload.latency,
		payload.model, payload.provider, payload.tokensIn, payload.tokensOut,
		payload.tokensReasoning, payload.cost}
}

// Each family is its own named custody migration. The ledger keys a migration
// to the single destination it owns, and the archive fills five of them, so one
// name per family is what lets them commit, resume and verify side by side in
// the corpus database.
var archiveSourceTables = []archiveTable{
	{
		sourceTable: "sessions", migration: "corpus-archive-sessions",
		destinationTable: "session_versions",
		query: `SELECT rowid, session_id, source_agent, project, started_at, ended_at,
			duration_minutes, title, metadata FROM sessions ORDER BY session_id`,
		scan: scanSession,
	},
	{
		sourceTable: "exchanges", migration: "corpus-archive-exchanges",
		destinationTable: "exchange_versions",
		query: `SELECT id, session_id, exchange_number, is_after_compaction,
			human_text, agent_text, human_timestamp, agent_timestamp,
			response_latency_ms, model, provider, tokens_in, tokens_out,
			tokens_reasoning, cost_usd FROM exchanges
			ORDER BY session_id, exchange_number, id`,
		scan: scanExchange,
	},
	{
		sourceTable: "tool_uses", migration: "corpus-archive-tool-uses",
		destinationTable: "tool_use_versions",
		query: `SELECT id, session_id, exchange_number, tool_name,
			tool_params_summary, had_error, error_message, initiative_type
			FROM tool_uses ORDER BY session_id, exchange_number, id`,
		scan: scanToolUse,
	},
	{
		sourceTable: "thinking_blocks", migration: "corpus-archive-thinking-blocks",
		destinationTable: "thinking_block_versions",
		query: `SELECT id, session_id, exchange_number, position_in_session, depth,
			caution_ratio, word_count, is_after_compaction, full_text
			FROM thinking_blocks ORDER BY session_id, exchange_number, id`,
		scan: scanThinkingBlock,
	},
	{
		sourceTable: "ingest_file_state", migration: "corpus-archive-ingest-file-state",
		destinationTable: "ingest_file_state_versions",
		query: `SELECT path, source_kind, source_agent, project, fingerprint,
			last_synced_at, last_error, metadata FROM ingest_file_state ORDER BY path`,
		scan: scanIngestState,
	},
}

func importSource(ctx context.Context, destination *sql.DB, source preparedSource, batchSize int) error {
	for _, table := range archiveSourceTables {
		if err := importTable(ctx, destination, source, table, batchSize); err != nil {
			return fmt.Errorf("merge %s.%s into corpus shadow: %w",
				source.Database, table.sourceTable, err)
		}
	}
	return nil
}

func importTable(ctx context.Context, destination *sql.DB, source preparedSource,
	table archiveTable, batchSize int,
) error {
	var expected int64
	if err := source.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table.sourceTable).Scan(&expected); err != nil {
		return err
	}
	if err := validateRecordedTable(ctx, destination, source.Database, table.sourceTable, expected); err != nil {
		return err
	}
	rows, err := source.db.QueryContext(ctx, table.query)
	if err != nil {
		return err
	}
	defer rows.Close()
	tracker := &occurrenceTracker{}
	batch := make([]archiveRecord, 0, batchSize)
	batchIndex := 0
	var scanned int64
	flush := func() error {
		err := commitBatch(ctx, destination, source, table, expected, batchSize, batchIndex, batch)
		batchIndex++
		batch = batch[:0]
		return err
	}
	for rows.Next() {
		record, err := table.scan(rows, tracker)
		if err != nil {
			return err
		}
		batch = append(batch, record)
		scanned++
		if len(batch) == batchSize {
			if err := flush(); err != nil {
				return err
			}
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(batch) > 0 || scanned == 0 {
		if err := flush(); err != nil {
			return err
		}
	}
	if scanned != expected {
		return fmt.Errorf("source changed while reading: counted %d rows and scanned %d", expected, scanned)
	}
	return nil
}

func commitBatch(ctx context.Context, destination *sql.DB, source preparedSource,
	table archiveTable, expected int64, batchSize, index int, records []archiveRecord,
) error {
	token := canonicalDigest("corpus-source", source.Database, source.SnapshotDigest)[:16]
	id := fmt.Sprintf("corpus-archive-v2-%s-%s-%06d", token, table.sourceTable, index)
	batchValues := make([]any, 0, len(records)*2+3)
	batchValues = append(batchValues, source.Database, table.sourceTable, int64(index))
	for _, record := range records {
		batchValues = append(batchValues, record.sourceKey, record.digest)
	}
	highWaterMark := "empty"
	if len(records) > 0 {
		highWaterMark = records[len(records)-1].sourceKey
	}
	commit := migrationledger.BatchCommit{
		RowCount: len(records), CanonicalDigest: canonicalDigest("corpus-batch", batchValues...),
		HighWaterMark: highWaterMark,
	}
	batch, err := migrationledger.BeginBatch(ctx, destination, migrationledger.BatchSpec{
		Migration: table.migration, ID: id,
		SourceDatabase: source.Database, SourceTable: table.sourceTable,
	})
	if errors.Is(err, migrationledger.ErrBatchCommitted) {
		return verifyCommittedBatch(ctx, destination, table, id, source.Database, commit)
	}
	if err != nil {
		return err
	}
	defer batch.Rollback()
	destinationSource := 0
	if source.ExistingCorpus {
		destinationSource = 1
	}
	if _, err := batch.ExecContext(ctx, `INSERT OR IGNORE INTO corpus_source_snapshots
		(source_database, snapshot_digest, destination_source, batch_size) VALUES (?, ?, ?, ?)`,
		source.Database, source.SnapshotDigest, destinationSource, batchSize); err != nil {
		return err
	}
	if _, err := batch.ExecContext(ctx, `INSERT OR IGNORE INTO corpus_source_tables
		(source_database, source_table, expected_rows) VALUES (?, ?, ?)`,
		source.Database, table.sourceTable, expected); err != nil {
		return err
	}
	for _, record := range records {
		if err := insertRecord(ctx, batch, source, record); err != nil {
			return err
		}
		if err := batch.AddMembership(ctx, migrationledger.Membership{
			SourceKey: record.sourceKey, DestinationTable: record.destinationTable,
			DestinationKey: record.digest, CanonicalDigest: record.digest,
		}); err != nil {
			return err
		}
	}
	return batch.Commit(ctx, commit)
}

func verifyCommittedBatch(ctx context.Context, destination *sql.DB, table archiveTable,
	id, database string, want migrationledger.BatchCommit,
) error {
	var gotDatabase, gotTable, gotDigest, gotHighWater string
	var gotRows int
	err := destination.QueryRowContext(ctx, `SELECT source_database, source_table, row_count,
		canonical_digest, high_water_mark FROM migration_batches
		WHERE migration = ? AND batch_id = ?`, table.migration, id).
		Scan(&gotDatabase, &gotTable, &gotRows, &gotDigest, &gotHighWater)
	if err != nil {
		return fmt.Errorf("verify committed corpus batch %q: %w", id, err)
	}
	if gotDatabase != database || gotTable != table.sourceTable || gotRows != want.RowCount ||
		gotDigest != want.CanonicalDigest || gotHighWater != want.HighWaterMark {
		return fmt.Errorf("committed corpus batch %q does not match its frozen source", id)
	}
	return nil
}

func insertRecord(ctx context.Context, batch *migrationledger.Batch,
	source preparedSource, record archiveRecord,
) error {
	query, err := insertStatement(record.destinationTable)
	if err != nil {
		return err
	}
	args := append([]any{record.digest}, record.values...)
	if _, err := batch.ExecContext(ctx, query, args...); err != nil {
		return fmt.Errorf("store %s version %s: %w", record.destinationTable, record.digest, err)
	}
	if _, err := batch.ExecContext(ctx, `INSERT OR IGNORE INTO corpus_source_rows
		(source_database, source_table, source_key, destination_table, version_digest,
		 source_row_id, session_id, exchange_number, occurrence_ordinal)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, source.Database,
		sourceTableFor(record.destinationTable), record.sourceKey, record.destinationTable,
		record.digest, record.sourceRowID, record.sessionID, record.exchangeNumber, record.ordinal); err != nil {
		return fmt.Errorf("record corpus source row: %w", err)
	}
	if record.statePath.Valid {
		priority := 0
		if source.ExistingCorpus {
			priority = 1
		}
		_, err := batch.ExecContext(ctx, `INSERT INTO ingest_file_state_heads
			(path, version_digest, source_database, destination_priority) VALUES (?, ?, ?, ?)
			ON CONFLICT(path) DO UPDATE SET
			  version_digest = excluded.version_digest,
			  source_database = excluded.source_database,
			  destination_priority = excluded.destination_priority
			WHERE excluded.destination_priority > ingest_file_state_heads.destination_priority
			   OR (excluded.destination_priority = ingest_file_state_heads.destination_priority
			       AND excluded.source_database < ingest_file_state_heads.source_database)`,
			record.statePath.String, record.digest, source.Database, priority)
		if err != nil {
			return fmt.Errorf("select corpus ingest state head: %w", err)
		}
	}
	return nil
}

func insertStatement(destinationTable string) (string, error) {
	switch destinationTable {
	case "session_versions":
		return `INSERT OR IGNORE INTO session_versions
			(version_digest, session_id, source_agent, project, started_at, ended_at,
			 duration_minutes, title, metadata) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, nil
	case "exchange_versions":
		return `INSERT OR IGNORE INTO exchange_versions
			(version_digest, session_id, exchange_number, is_after_compaction,
			 human_text, agent_text, human_timestamp, agent_timestamp, response_latency_ms,
			 model, provider, tokens_in, tokens_out, tokens_reasoning, cost_usd)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, nil
	case "tool_use_versions":
		return `INSERT OR IGNORE INTO tool_use_versions
			(version_digest, session_id, exchange_number, tool_name, tool_params_summary,
			 had_error, error_message, initiative_type) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, nil
	case "thinking_block_versions":
		return `INSERT OR IGNORE INTO thinking_block_versions
			(version_digest, session_id, exchange_number, position_in_session, depth,
			 caution_ratio, word_count, is_after_compaction, full_text)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, nil
	case "ingest_file_state_versions":
		return `INSERT OR IGNORE INTO ingest_file_state_versions
			(version_digest, path, source_kind, source_agent, project, fingerprint,
			 last_synced_at, last_error, metadata) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, nil
	default:
		return "", fmt.Errorf("unknown corpus archive destination %q", destinationTable)
	}
}

func sourceTableFor(destinationTable string) string {
	for _, table := range archiveSourceTables {
		if table.destinationTable == destinationTable {
			return table.sourceTable
		}
	}
	panic("unknown corpus archive destination " + destinationTable)
}

func validateRecordedTable(ctx context.Context, destination *sql.DB,
	database, table string, expected int64,
) error {
	var recorded int64
	err := destination.QueryRowContext(ctx, `SELECT expected_rows FROM corpus_source_tables
		WHERE source_database = ? AND source_table = ?`, database, table).Scan(&recorded)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if recorded != expected {
		return fmt.Errorf("recorded source count for %s.%s is %d, frozen source has %d",
			database, table, recorded, expected)
	}
	return nil
}

func scanSession(rows *sql.Rows, _ *occurrenceTracker) (archiveRecord, error) {
	var rowID int64
	var sessionID string
	var sourceAgent, project, startedAt, endedAt, title, metadata sql.NullString
	var duration sql.NullInt64
	if err := rows.Scan(&rowID, &sessionID, &sourceAgent, &project, &startedAt, &endedAt,
		&duration, &title, &metadata); err != nil {
		return archiveRecord{}, err
	}
	values := []any{sessionID, sourceAgent, project, startedAt, endedAt, duration, title, metadata}
	return archiveRecord{
		sourceKey:        canonicalDigest("session-key", sessionID),
		digest:           canonicalDigest("session", values...),
		destinationTable: "session_versions", values: values,
		sourceRowID: sql.NullInt64{Int64: rowID, Valid: true},
		sessionID:   sql.NullString{String: sessionID, Valid: true},
	}, nil
}

func scanExchange(rows *sql.Rows, tracker *occurrenceTracker) (archiveRecord, error) {
	var id int64
	payload, err := scanExchangePayload(rows, &id)
	if err != nil {
		return archiveRecord{}, err
	}
	if !payload.sessionID.Valid {
		return archiveRecord{}, fmt.Errorf("exchange %d has no deterministic session/exchange key", id)
	}
	values := payload.values()
	digest := canonicalDigest("exchange", values...)
	ordinal := sql.NullInt64{Int64: tracker.next(payload.sessionID, payload.number, digest), Valid: true}
	return archiveRecord{
		sourceKey: occurrenceKey("exchange", payload.sessionID, payload.number, digest, ordinal.Int64),
		digest:    digest, destinationTable: "exchange_versions",
		values: values, sourceRowID: sql.NullInt64{Int64: id, Valid: true},
		sessionID: payload.sessionID, exchangeNumber: payload.number, ordinal: ordinal,
	}, nil
}

func scanToolUse(rows *sql.Rows, tracker *occurrenceTracker) (archiveRecord, error) {
	var id int64
	var sessionID, name, params, errorMessage, initiative sql.NullString
	var number, hadError sql.NullInt64
	if err := rows.Scan(&id, &sessionID, &number, &name, &params, &hadError,
		&errorMessage, &initiative); err != nil {
		return archiveRecord{}, err
	}
	if !sessionID.Valid {
		return archiveRecord{}, fmt.Errorf("tool use %d has no deterministic parent turn", id)
	}
	values := []any{sessionID.String, number, name, params, hadError, errorMessage, initiative}
	digest := canonicalDigest("tool-use", values...)
	ordinal := tracker.next(sessionID, number, digest)
	return childRecord("tool_use_versions", id, sessionID, number, ordinal, digest, values), nil
}

func scanThinkingBlock(rows *sql.Rows, tracker *occurrenceTracker) (archiveRecord, error) {
	var id int64
	var sessionID, depth, fullText sql.NullString
	var number, wordCount, compacted sql.NullInt64
	var position, caution sql.NullFloat64
	if err := rows.Scan(&id, &sessionID, &number, &position, &depth, &caution,
		&wordCount, &compacted, &fullText); err != nil {
		return archiveRecord{}, err
	}
	if !sessionID.Valid {
		return archiveRecord{}, fmt.Errorf("thinking block %d has no deterministic parent turn", id)
	}
	values := []any{sessionID.String, number, position, depth, caution,
		wordCount, compacted, fullText}
	digest := canonicalDigest("thinking-block", values...)
	ordinal := tracker.next(sessionID, number, digest)
	return childRecord("thinking_block_versions", id, sessionID, number, ordinal, digest, values), nil
}

func childRecord(destination string, id int64, sessionID sql.NullString,
	number sql.NullInt64, ordinal int64, digest string, values []any,
) archiveRecord {
	return archiveRecord{
		sourceKey: occurrenceKey("child", sessionID, number, digest, ordinal),
		digest:    digest, destinationTable: destination, values: values,
		sourceRowID: sql.NullInt64{Int64: id, Valid: true}, sessionID: sessionID,
		exchangeNumber: number, ordinal: sql.NullInt64{Int64: ordinal, Valid: true},
	}
}

func scanIngestState(rows *sql.Rows, _ *occurrenceTracker) (archiveRecord, error) {
	var path string
	var kind, agent, project, fingerprint, syncedAt, lastError, metadata sql.NullString
	if err := rows.Scan(&path, &kind, &agent, &project, &fingerprint,
		&syncedAt, &lastError, &metadata); err != nil {
		return archiveRecord{}, err
	}
	values := []any{path, kind, agent, project, fingerprint, syncedAt, lastError, metadata}
	return archiveRecord{
		sourceKey:        canonicalDigest("ingest-file-state-key", path),
		digest:           canonicalDigest("ingest-file-state", values...),
		destinationTable: "ingest_file_state_versions", values: values,
		statePath: sql.NullString{String: path, Valid: true},
	}, nil
}

func (tracker *occurrenceTracker) next(sessionID sql.NullString, number sql.NullInt64, digest string) int64 {
	parent := canonicalDigest("occurrence-parent", sessionID, number)
	if tracker.parent != parent {
		tracker.parent = parent
		tracker.counts = make(map[string]int64)
	}
	ordinal := tracker.counts[digest]
	tracker.counts[digest] = ordinal + 1
	return ordinal
}

func occurrenceKey(family string, sessionID sql.NullString, number sql.NullInt64,
	digest string, ordinal int64,
) string {
	parent := canonicalDigest(family+"-parent", sessionID, number)
	return parent + ":" + digest + ":" + strconv.FormatInt(ordinal, 10)
}
