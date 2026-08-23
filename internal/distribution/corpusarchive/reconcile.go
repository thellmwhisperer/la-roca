package corpusarchive

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
)

const (
	ReconciliationGreen = "green"
	ReconciliationRed   = "red"
)

type CustodyReport struct {
	Database             string  `json:"database"`
	SourceTable          string  `json:"source_table"`
	ExpectedCount        int64   `json:"expected_count"`
	ObservedCount        int64   `json:"observed_count"`
	ExpectedHash         string  `json:"expected_hash"`
	ObservedHash         string  `json:"observed_hash"`
	DuplicateMemberships int64   `json:"duplicate_memberships"`
	CoveragePercent      float64 `json:"coverage_percent"`
	Status               string  `json:"status"`
}

type SessionReport struct {
	Database                 string `json:"database"`
	SessionID                string `json:"session_id"`
	ExpectedRecords          int64  `json:"expected_records"`
	ObservedRecords          int64  `json:"observed_records"`
	ExpectedExchanges        int64  `json:"expected_exchanges"`
	ObservedExchanges        int64  `json:"observed_exchanges"`
	ExpectedHash             string `json:"expected_hash"`
	ObservedHash             string `json:"observed_hash"`
	ExpectedProvenanceHash   string `json:"expected_provenance_hash"`
	ObservedProvenanceHash   string `json:"observed_provenance_hash"`
	ExactPayloadAliases      int64  `json:"exact_payload_aliases"`
	DuplicateMemberships     int64  `json:"duplicate_memberships"`
	ModelProvenancePreserved bool   `json:"model_provenance_preserved"`
	Status                   string `json:"status"`
}

type ReconciliationReport struct {
	Status                string          `json:"status"`
	CoveragePercent       float64         `json:"coverage_percent"`
	ExpectedRecords       int64           `json:"expected_records"`
	ObservedRecords       int64           `json:"observed_records"`
	DuplicatePhysicalRows int64           `json:"duplicate_physical_rows"`
	Custody               []CustodyReport `json:"custody"`
	Sessions              []SessionReport `json:"sessions"`
}

type reconciliationRecord struct {
	table, key, digest string
}

type sessionInventory struct {
	records, provenance []reconciliationRecord
	exchanges           int64
	duplicates          int64
}

type sourceInventory struct {
	custody  map[string][]reconciliationRecord
	sessions map[string]*sessionInventory
}

func buildReport(ctx context.Context, db *sql.DB, sources []preparedSource) (Report, error) {
	reconciliation, reconcileErr := reconcileFrozenSources(ctx, db, sources)
	report := Report{Reconciliation: reconciliation}
	if reconcileErr != nil {
		return report, reconcileErr
	}
	verified, err := verifyArchive(ctx, db)
	if err != nil {
		report.Reconciliation.Status = ReconciliationRed
		return report, err
	}
	verified.Reconciliation = reconciliation
	return verified, nil
}

func reconcileFrozenSources(ctx context.Context, destination *sql.DB,
	sources []preparedSource,
) (ReconciliationReport, error) {
	report := ReconciliationReport{Status: ReconciliationGreen}
	expected, err := readExpectedInventories(ctx, sources)
	if err != nil {
		report.Status = ReconciliationRed
		return report, err
	}
	var firstMismatch error
	recordMismatch := func(err error) {
		if firstMismatch == nil {
			firstMismatch = err
		}
	}
	if err := compareRecordedSourceLabels(ctx, destination, expected); err != nil {
		recordMismatch(err)
	}
	observed, duplicateMemberships, err := readObservedInventories(ctx, destination, expected)
	if err != nil {
		report.Status = ReconciliationRed
		return report, err
	}
	report.DuplicatePhysicalRows, err = duplicatePhysicalRows(ctx, destination)
	if err != nil {
		report.Status = ReconciliationRed
		return report, err
	}
	if report.DuplicatePhysicalRows != 0 {
		recordMismatch(fmt.Errorf("DATA-3 has %d duplicate physical version rows",
			report.DuplicatePhysicalRows))
	}

	databases := sortedInventoryKeys(expected)
	for _, database := range databases {
		for _, table := range archiveSourceTables {
			want := expected[database].custody[table.sourceTable]
			got := observed[database].custody[table.sourceTable]
			custody := CustodyReport{
				Database: database, SourceTable: table.sourceTable,
				ExpectedCount: int64(len(want)), ObservedCount: int64(len(got)),
				ExpectedHash:         inventoryDigest("custody-table", want),
				ObservedHash:         inventoryDigest("custody-table", got),
				DuplicateMemberships: duplicateMemberships[database+"\x00"+table.sourceTable],
				Status:               ReconciliationGreen,
			}
			custody.CoveragePercent = coveragePercent(custody.ExpectedCount, custody.ObservedCount)
			report.ExpectedRecords += custody.ExpectedCount
			report.ObservedRecords += custody.ObservedCount
			if custody.ExpectedCount != custody.ObservedCount ||
				custody.ExpectedHash != custody.ObservedHash || custody.DuplicateMemberships != 0 {
				custody.Status = ReconciliationRed
				recordMismatch(fmt.Errorf(
					"DATA-3 custody mismatch for %s.%s: count=%d/%d hash=%s/%s duplicates=%d",
					database, table.sourceTable, custody.ObservedCount, custody.ExpectedCount,
					custody.ObservedHash, custody.ExpectedHash, custody.DuplicateMemberships))
			}
			report.Custody = append(report.Custody, custody)
		}
	}

	for _, key := range sortedSessionKeys(expected, observed) {
		want := sessionAt(expected, key.database, key.sessionID)
		got := sessionAt(observed, key.database, key.sessionID)
		session := SessionReport{
			Database: key.database, SessionID: key.sessionID,
			ExpectedRecords: int64(len(want.records)), ObservedRecords: int64(len(got.records)),
			ExpectedExchanges: want.exchanges, ObservedExchanges: got.exchanges,
			ExpectedHash:           inventoryDigest("session-custody", want.records),
			ObservedHash:           inventoryDigest("session-custody", got.records),
			ExpectedProvenanceHash: inventoryDigest("session-provenance", want.provenance),
			ObservedProvenanceHash: inventoryDigest("session-provenance", got.provenance),
			ExactPayloadAliases:    exactAliases(want.records),
			DuplicateMemberships:   got.duplicates,
			Status:                 ReconciliationGreen,
		}
		session.ModelProvenancePreserved =
			session.ExpectedProvenanceHash == session.ObservedProvenanceHash
		if session.ExpectedRecords != session.ObservedRecords ||
			session.ExpectedExchanges != session.ObservedExchanges ||
			session.ExpectedHash != session.ObservedHash || session.DuplicateMemberships != 0 ||
			!session.ModelProvenancePreserved {
			session.Status = ReconciliationRed
			recordMismatch(fmt.Errorf(
				"DATA-3 session mismatch for %s/%q: records=%d/%d exchanges=%d/%d hash=%s/%s duplicates=%d provenance=%t",
				key.database, key.sessionID, session.ObservedRecords, session.ExpectedRecords,
				session.ObservedExchanges, session.ExpectedExchanges, session.ObservedHash,
				session.ExpectedHash, session.DuplicateMemberships,
				session.ModelProvenancePreserved))
		}
		report.Sessions = append(report.Sessions, session)
	}

	report.CoveragePercent = coveragePercent(report.ExpectedRecords, report.ObservedRecords)
	if firstMismatch != nil || report.CoveragePercent != 100 {
		report.Status = ReconciliationRed
		if firstMismatch == nil {
			firstMismatch = fmt.Errorf("DATA-3 global coverage is %.6f%%, want exactly 100%%",
				report.CoveragePercent)
		}
		return report, firstMismatch
	}
	return report, nil
}

func readExpectedInventories(ctx context.Context,
	sources []preparedSource,
) (map[string]*sourceInventory, error) {
	inventories := make(map[string]*sourceInventory, len(sources))
	for _, source := range sources {
		inventory := newSourceInventory()
		inventories[source.Database] = inventory
		for _, table := range archiveSourceTables {
			rows, err := source.db.QueryContext(ctx, table.query)
			if err != nil {
				return nil, fmt.Errorf("read frozen %s.%s for reconciliation: %w",
					source.Database, table.sourceTable, err)
			}
			tracker := &occurrenceTracker{}
			for rows.Next() {
				record, scanErr := table.scan(rows, tracker)
				if scanErr != nil {
					rows.Close()
					return nil, scanErr
				}
				item := reconciliationRecord{table: table.sourceTable,
					key: record.sourceKey, digest: record.digest}
				inventory.custody[table.sourceTable] = append(inventory.custody[table.sourceTable], item)
				if record.sessionID.Valid {
					session := ensureSession(inventory, record.sessionID.String)
					session.records = append(session.records, item)
					if table.sourceTable == "exchanges" {
						session.exchanges++
						session.provenance = append(session.provenance,
							expectedProvenance(record))
					}
				}
			}
			if err := rows.Err(); err != nil {
				rows.Close()
				return nil, err
			}
			if err := rows.Close(); err != nil {
				return nil, err
			}
		}
	}
	return inventories, nil
}

func readObservedInventories(ctx context.Context, destination *sql.DB,
	expected map[string]*sourceInventory,
) (map[string]*sourceInventory, map[string]int64, error) {
	observed := make(map[string]*sourceInventory, len(expected))
	duplicates := make(map[string]int64)
	physicalByTable := make(map[string]map[string]string, len(archiveSourceTables))
	for _, table := range archiveSourceTables {
		physical, err := readPhysicalDigests(ctx, destination, table)
		if err != nil {
			return nil, nil, err
		}
		physicalByTable[table.sourceTable] = physical
	}
	for _, database := range sortedInventoryKeys(expected) {
		inventory := newSourceInventory()
		observed[database] = inventory
		for _, table := range archiveSourceTables {
			physical := physicalByTable[table.sourceTable]
			rows, err := destination.QueryContext(ctx, `SELECT m.source_key, m.destination_key,
				m.canonical_digest, r.version_digest, r.session_id
				FROM custody_memberships AS m
				LEFT JOIN corpus_source_rows AS r
				  USING (source_database, source_table, source_key)
				WHERE m.migration = ? AND m.source_database = ? AND m.source_table = ?
				ORDER BY m.source_key`, table.migration, database, table.sourceTable)
			if err != nil {
				return nil, nil, err
			}
			seen := make(map[string]bool)
			for rows.Next() {
				var key, destinationKey, digest string
				var evidenceDigest, sessionID sql.NullString
				if err := rows.Scan(&key, &destinationKey, &digest, &evidenceDigest, &sessionID); err != nil {
					rows.Close()
					return nil, nil, err
				}
				duplicate := seen[key]
				if duplicate {
					duplicates[database+"\x00"+table.sourceTable]++
				}
				seen[key] = true
				observedDigest, physicalPresent := physical[destinationKey]
				if !physicalPresent || destinationKey != digest || !evidenceDigest.Valid ||
					evidenceDigest.String != digest {
					observedDigest = canonicalDigest("broken-membership", destinationKey,
						digest, evidenceDigest, observedDigest)
				}
				item := reconciliationRecord{table: table.sourceTable, key: key, digest: observedDigest}
				inventory.custody[table.sourceTable] = append(inventory.custody[table.sourceTable], item)
				if sessionID.Valid {
					session := ensureSession(inventory, sessionID.String)
					session.records = append(session.records, item)
					if duplicate {
						session.duplicates++
					}
					if table.sourceTable == "exchanges" {
						session.exchanges++
					}
				}
			}
			if err := rows.Err(); err != nil {
				rows.Close()
				return nil, nil, err
			}
			if err := rows.Close(); err != nil {
				return nil, nil, err
			}
		}
		if err := readObservedProvenance(ctx, destination, database, inventory); err != nil {
			return nil, nil, err
		}
	}
	return observed, duplicates, nil
}

func readPhysicalDigests(ctx context.Context, destination *sql.DB,
	table archiveTable,
) (map[string]string, error) {
	query, err := physicalDigestQuery(table.destinationTable)
	if err != nil {
		return nil, err
	}
	rows, err := destination.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	digests := make(map[string]string)
	for rows.Next() {
		stored, actual, scanErr := scanPhysicalDigest(rows, table.destinationTable)
		if scanErr != nil {
			return nil, scanErr
		}
		if stored != actual {
			actual = canonicalDigest("broken-physical-digest", stored, actual)
		}
		digests[stored] = actual
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return digests, nil
}

func physicalDigestQuery(destinationTable string) (string, error) {
	switch destinationTable {
	case "session_versions":
		return `SELECT version_digest FROM session_versions`, nil
	case "exchange_versions":
		return `SELECT version_digest FROM exchange_versions`, nil
	case "tool_use_versions":
		return `SELECT version_digest FROM tool_use_versions`, nil
	case "thinking_block_versions":
		return `SELECT version_digest FROM thinking_block_versions`, nil
	case "ingest_file_state_versions":
		return `SELECT version_digest, path, source_kind, source_agent, project, fingerprint,
			last_synced_at, last_error, metadata FROM ingest_file_state_versions`, nil
	default:
		return "", fmt.Errorf("unknown corpus archive destination %q", destinationTable)
	}
}

func scanPhysicalDigest(rows *sql.Rows, destinationTable string) (string, string, error) {
	var stored string
	switch destinationTable {
	case "session_versions":
		if err := rows.Scan(&stored); err != nil {
			return "", "", err
		}
		return stored, stored, nil
	case "exchange_versions":
		if err := rows.Scan(&stored); err != nil {
			return "", "", err
		}
		return stored, stored, nil
	case "tool_use_versions":
		if err := rows.Scan(&stored); err != nil {
			return "", "", err
		}
		return stored, stored, nil
	case "thinking_block_versions":
		if err := rows.Scan(&stored); err != nil {
			return "", "", err
		}
		return stored, stored, nil
	case "ingest_file_state_versions":
		var path string
		var kind, agent, project, fingerprint, syncedAt, lastError, metadata sql.NullString
		if err := rows.Scan(&stored, &path, &kind, &agent, &project, &fingerprint,
			&syncedAt, &lastError, &metadata); err != nil {
			return "", "", err
		}
		return stored, canonicalDigest("ingest-file-state", path, kind, agent, project,
			fingerprint, syncedAt, lastError, metadata), nil
	default:
		return "", "", fmt.Errorf("unknown corpus archive destination %q", destinationTable)
	}
}

func readObservedProvenance(ctx context.Context, destination *sql.DB, database string,
	inventory *sourceInventory,
) error {
	rows, err := destination.QueryContext(ctx, `SELECT m.source_key, r.session_id,
		v.model, v.provider, v.tokens_in, v.tokens_out, v.tokens_reasoning, v.cost_usd
		FROM custody_memberships AS m
		JOIN corpus_source_rows AS r USING (source_database, source_table, source_key)
		JOIN exchange_versions AS v ON v.version_digest = m.destination_key
		WHERE m.migration = 'corpus-archive-exchanges'
		  AND m.source_database = ? AND m.source_table = 'exchanges'
		ORDER BY m.source_key`, database)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var key, sessionID string
		var model, provider sql.NullString
		var tokensIn, tokensOut, tokensReasoning sql.NullInt64
		var cost sql.NullFloat64
		if err := rows.Scan(&key, &sessionID, &model, &provider, &tokensIn,
			&tokensOut, &tokensReasoning, &cost); err != nil {
			return err
		}
		ensureSession(inventory, sessionID).provenance = append(
			ensureSession(inventory, sessionID).provenance,
			reconciliationRecord{table: "exchanges", key: key,
				digest: provenanceDigest(model, provider, tokensIn, tokensOut, tokensReasoning, cost)})
	}
	return rows.Err()
}

func expectedProvenance(record archiveRecord) reconciliationRecord {
	return reconciliationRecord{table: "exchanges", key: record.sourceKey,
		digest: provenanceDigest(record.provenance...)}
}

func provenanceDigest(values ...any) string {
	return canonicalDigest("exchange-provenance", values...)
}

func compareRecordedSourceLabels(ctx context.Context, destination *sql.DB,
	expected map[string]*sourceInventory,
) error {
	rows, err := destination.QueryContext(ctx, `SELECT source_database
		FROM corpus_source_snapshots ORDER BY source_database`)
	if err != nil {
		return err
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var database string
		if err := rows.Scan(&database); err != nil {
			return err
		}
		got = append(got, database)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	want := sortedInventoryKeys(expected)
	if fmt.Sprint(got) != fmt.Sprint(want) {
		return fmt.Errorf("DATA-3 source set is %v, frozen verifier supplied %v", got, want)
	}
	return nil
}

func duplicatePhysicalRows(ctx context.Context, destination *sql.DB) (int64, error) {
	var total int64
	for _, table := range archiveSourceTables {
		query, err := physicalDigestQuery(table.destinationTable)
		if err != nil {
			return 0, err
		}
		rows, err := destination.QueryContext(ctx, query)
		if err != nil {
			return 0, err
		}
		counts := make(map[string]int64)
		for rows.Next() {
			_, actual, scanErr := scanPhysicalDigest(rows, table.destinationTable)
			if scanErr != nil {
				rows.Close()
				return 0, scanErr
			}
			counts[actual]++
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return 0, err
		}
		if err := rows.Close(); err != nil {
			return 0, err
		}
		for _, count := range counts {
			if count > 1 {
				total += count - 1
			}
		}
	}
	return total, nil
}

func newSourceInventory() *sourceInventory {
	return &sourceInventory{custody: make(map[string][]reconciliationRecord),
		sessions: make(map[string]*sessionInventory)}
}

func ensureSession(inventory *sourceInventory, sessionID string) *sessionInventory {
	if inventory.sessions[sessionID] == nil {
		inventory.sessions[sessionID] = &sessionInventory{}
	}
	return inventory.sessions[sessionID]
}

func sessionAt(inventories map[string]*sourceInventory, database, sessionID string) *sessionInventory {
	if inventory := inventories[database]; inventory != nil {
		if session := inventory.sessions[sessionID]; session != nil {
			return session
		}
	}
	return &sessionInventory{}
}

func sortedInventoryKeys(inventories map[string]*sourceInventory) []string {
	keys := make([]string, 0, len(inventories))
	for key := range inventories {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

type sessionKey struct{ database, sessionID string }

func sortedSessionKeys(left, right map[string]*sourceInventory) []sessionKey {
	seen := make(map[sessionKey]bool)
	for _, inventories := range []map[string]*sourceInventory{left, right} {
		for database, inventory := range inventories {
			for sessionID := range inventory.sessions {
				seen[sessionKey{database: database, sessionID: sessionID}] = true
			}
		}
	}
	keys := make([]sessionKey, 0, len(seen))
	for key := range seen {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].database == keys[j].database {
			return keys[i].sessionID < keys[j].sessionID
		}
		return keys[i].database < keys[j].database
	})
	return keys
}

func inventoryDigest(label string, records []reconciliationRecord) string {
	ordered := append([]reconciliationRecord(nil), records...)
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].table != ordered[j].table {
			return ordered[i].table < ordered[j].table
		}
		if ordered[i].key != ordered[j].key {
			return ordered[i].key < ordered[j].key
		}
		return ordered[i].digest < ordered[j].digest
	})
	values := make([]any, 0, 1+len(ordered)*3)
	values = append(values, int64(len(ordered)))
	for _, record := range ordered {
		values = append(values, record.table, record.key, record.digest)
	}
	return canonicalDigest(label, values...)
}

func exactAliases(records []reconciliationRecord) int64 {
	seen := make(map[string]bool, len(records))
	for _, record := range records {
		seen[record.table+"\x00"+record.digest] = true
	}
	return int64(len(records) - len(seen))
}

func coveragePercent(expected, observed int64) float64 {
	if expected == 0 {
		if observed == 0 {
			return 100
		}
		return 0
	}
	return 100 * float64(observed) / float64(expected)
}
