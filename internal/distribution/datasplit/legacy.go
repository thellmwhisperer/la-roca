// Package datasplit owns shadow-only copies from the retired core database
// into the plugin databases that have ratified custody of each source table.
package datasplit

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/thellmwhisperer/la-roca/internal/distribution/bundledplugin"
	"github.com/thellmwhisperer/la-roca/internal/distribution/migrationledger"
	"github.com/thellmwhisperer/la-roca/internal/distribution/rocacorpus"
	"github.com/thellmwhisperer/la-roca/internal/distribution/rocacron"
	"github.com/thellmwhisperer/la-roca/internal/distribution/rocaops"
)

const (
	defaultBatchSize = 500
	coreSource       = "core"
)

type LegacyOptions struct {
	SourceClone    string
	CronDatabase   string
	OpsDatabase    string
	CorpusDatabase string
}

type LegacyTableResult struct {
	SourceTable      string
	DestinationTable string
	Disposition      string
	SourceRows       int
	PhysicalRows     int
	BatchCount       int
	CanonicalDigest  string
}

type LegacyReport struct {
	Tables           []LegacyTableResult
	Undisposed       []string
	SourceValid      bool
	DestinationValid bool
}

type destination string

const (
	destinationCron   destination = "cron"
	destinationOps    destination = "ops"
	destinationCorpus destination = "corpus"
)

type legacyPlan struct {
	sourceTable      string
	keyColumns       []string
	destination      destination
	destinationTable string
	recordType       string
}

var legacyPlans = []legacyPlan{
	{sourceTable: "runs", keyColumns: []string{"id"}, destination: destinationCron, destinationTable: "legacy_runs"},
	{sourceTable: "run_logs", keyColumns: []string{"id"}, destination: destinationCron, destinationTable: "legacy_run_logs"},
	{sourceTable: "garden_channels", keyColumns: []string{"id"}, destination: destinationOps, destinationTable: "legacy_records", recordType: "garden_channels"},
	{sourceTable: "garden_memberships", keyColumns: []string{"channel_id", "nick"}, destination: destinationOps, destinationTable: "legacy_records", recordType: "garden_memberships"},
	{sourceTable: "garden_messages", keyColumns: []string{"id"}, destination: destinationOps, destinationTable: "legacy_records", recordType: "garden_messages"},
	{sourceTable: "garden_read_cursors", keyColumns: []string{"channel_id", "nick"}, destination: destinationOps, destinationTable: "legacy_records", recordType: "garden_read_cursors"},
	{sourceTable: "garden_voice_leases", keyColumns: []string{"id"}, destination: destinationOps, destinationTable: "legacy_records", recordType: "garden_voice_leases"},
	{sourceTable: "proposals", keyColumns: []string{"id"}, destination: destinationOps, destinationTable: "legacy_records", recordType: "proposals"},
	{sourceTable: "proposal_annotations", keyColumns: []string{"id"}, destination: destinationOps, destinationTable: "legacy_records", recordType: "proposal_annotations"},
	{sourceTable: "queryplan_teach_examples", keyColumns: []string{"id"}, destination: destinationOps, destinationTable: "legacy_records", recordType: "queryplan_teach_examples"},
	{sourceTable: "flow_patterns", keyColumns: []string{"id"}, destination: destinationCorpus, destinationTable: "legacy_flow_patterns"},
}

var sourceTablesOwnedElsewhere = map[string]struct{}{
	"sessions": {}, "memories": {}, "exchanges": {}, "tool_uses": {},
	"thinking_blocks": {}, "ingest_file_state": {}, "layers": {},
	"sessions_fts": {}, "sessions_fts_config": {}, "sessions_fts_data": {},
	"sessions_fts_docsize": {}, "sessions_fts_idx": {},
	"memories_fts": {}, "memories_fts_config": {}, "memories_fts_data": {},
	"memories_fts_docsize": {}, "memories_fts_idx": {},
	"exchanges_fts": {}, "exchanges_fts_config": {}, "exchanges_fts_data": {},
	"exchanges_fts_docsize": {}, "exchanges_fts_idx": {},
	"thinking_fts": {}, "thinking_fts_config": {}, "thinking_fts_data": {},
	"thinking_fts_docsize": {}, "thinking_fts_idx": {},
	"sqlite_sequence": {},
}

type legacyRow struct {
	sourceKey string
	payload   string
	digest    string
}

type legacyImporter struct {
	options    LegacyOptions
	batchSize  int
	source     *sql.DB
	dest       map[destination]*sql.DB
	afterBatch func(string, int) error
}

// ImportLegacyOrphans copies only ratified DATA-4 tables from an immutable
// source snapshot. It never changes the serving route or the source database.
func ImportLegacyOrphans(ctx context.Context, options LegacyOptions) (LegacyReport, error) {
	return importLegacyOrphans(ctx, options, defaultBatchSize, nil)
}

func importLegacyOrphans(ctx context.Context, options LegacyOptions, batchSize int, afterBatch func(string, int) error) (LegacyReport, error) {
	if batchSize < 1 {
		return LegacyReport{}, fmt.Errorf("DATA-4 batch size must be positive")
	}
	if err := options.valid(); err != nil {
		return LegacyReport{}, err
	}
	source, err := bundledplugin.OpenDatabase(options.SourceClone, true)
	if err != nil {
		return LegacyReport{}, fmt.Errorf("open the DATA SPLIT source clone: %w", err)
	}
	defer source.Close()
	source.SetMaxOpenConns(1)

	valid, err := validateSource(ctx, source)
	if err != nil {
		return LegacyReport{}, err
	}
	report := LegacyReport{SourceValid: valid}
	undisposed, present, err := inspectSourceInventory(ctx, source)
	if err != nil {
		return report, err
	}
	report.Undisposed = undisposed
	if len(undisposed) != 0 {
		return report, fmt.Errorf("the source clone has undisposed tables: %s", strings.Join(undisposed, ", "))
	}

	for _, disposition := range []struct {
		table, action string
	}{
		{table: "messages", action: "create-nothing"},
		{table: "search_state", action: "rebuild-derived-state"},
	} {
		if !present[disposition.table] {
			continue
		}
		count, err := tableCount(ctx, source, disposition.table)
		if err != nil {
			return report, err
		}
		if disposition.table == "messages" && count != 0 {
			return report, fmt.Errorf("messages has %d source rows but DATA-4 ratified no destination", count)
		}
		report.Tables = append(report.Tables, LegacyTableResult{
			SourceTable: disposition.table, Disposition: disposition.action, SourceRows: count,
		})
	}

	if err := prepareDestinations(options); err != nil {
		return report, err
	}
	destinations, err := openDestinations(options)
	if err != nil {
		return report, err
	}
	defer closeDatabases(destinations)
	importer := legacyImporter{options: options, batchSize: batchSize,
		source: source, dest: destinations, afterBatch: afterBatch}
	for _, plan := range legacyPlans {
		if !present[plan.sourceTable] {
			continue
		}
		result, err := importer.importTable(ctx, plan)
		if err != nil {
			return report, err
		}
		report.Tables = append(report.Tables, result)
	}
	destinationValid := true
	for owner, db := range destinations {
		valid, err := validateDatabase(ctx, db, "DATA-4 "+string(owner)+" destination")
		if err != nil {
			return report, err
		}
		destinationValid = destinationValid && valid
	}
	report.DestinationValid = destinationValid
	sort.Slice(report.Tables, func(i, j int) bool {
		return report.Tables[i].SourceTable < report.Tables[j].SourceTable
	})
	return report, nil
}

func (options LegacyOptions) valid() error {
	if strings.TrimSpace(options.SourceClone) == "" || strings.TrimSpace(options.CronDatabase) == "" ||
		strings.TrimSpace(options.OpsDatabase) == "" || strings.TrimSpace(options.CorpusDatabase) == "" {
		return fmt.Errorf("DATA-4 needs a source clone and cron, ops, and corpus destination databases")
	}
	paths := []string{options.SourceClone, options.CronDatabase, options.OpsDatabase, options.CorpusDatabase}
	seen := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		absolute, err := filepath.Abs(path)
		if err != nil {
			return fmt.Errorf("resolve DATA-4 database path: %w", err)
		}
		if _, duplicate := seen[absolute]; duplicate {
			return fmt.Errorf("DATA-4 source and destination database paths must be distinct")
		}
		seen[absolute] = struct{}{}
	}
	info, err := os.Stat(options.SourceClone)
	if err != nil {
		return fmt.Errorf("inspect the DATA SPLIT source clone: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("the DATA SPLIT source clone is not a regular file")
	}
	return nil
}

func validateSource(ctx context.Context, source *sql.DB) (bool, error) {
	return validateDatabase(ctx, source, "DATA SPLIT source")
}

func validateDatabase(ctx context.Context, database *sql.DB, label string) (bool, error) {
	var integrity string
	if err := database.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&integrity); err != nil {
		return false, fmt.Errorf("check %s integrity: %w", label, err)
	}
	if integrity != "ok" {
		return false, fmt.Errorf("%s integrity is %q", label, integrity)
	}
	var foreignKeyFailures int
	if err := database.QueryRowContext(ctx, "SELECT COUNT(*) FROM pragma_foreign_key_check").Scan(&foreignKeyFailures); err != nil {
		return false, fmt.Errorf("check %s foreign keys: %w", label, err)
	}
	if foreignKeyFailures != 0 {
		return false, fmt.Errorf("%s has %d foreign key failures", label, foreignKeyFailures)
	}
	return true, nil
}

func inspectSourceInventory(ctx context.Context, source *sql.DB) ([]string, map[string]bool, error) {
	handled := map[string]struct{}{"messages": {}, "search_state": {}}
	for _, plan := range legacyPlans {
		handled[plan.sourceTable] = struct{}{}
	}
	rows, err := source.QueryContext(ctx, "SELECT name FROM sqlite_schema WHERE type = 'table' ORDER BY name")
	if err != nil {
		return nil, nil, fmt.Errorf("inventory DATA SPLIT source tables: %w", err)
	}
	defer rows.Close()
	present := make(map[string]bool)
	var undisposed []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, nil, fmt.Errorf("read DATA SPLIT source inventory: %w", err)
		}
		present[name] = true
		if _, owned := sourceTablesOwnedElsewhere[name]; owned {
			continue
		}
		if _, owned := handled[name]; !owned {
			undisposed = append(undisposed, name)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("read DATA SPLIT source inventory: %w", err)
	}
	return undisposed, present, nil
}

func prepareDestinations(options LegacyOptions) error {
	for _, path := range []string{options.CronDatabase, options.OpsDatabase, options.CorpusDatabase} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return fmt.Errorf("create DATA-4 destination directory: %w", err)
		}
	}
	for _, apply := range []struct {
		name string
		path string
		fn   func(string) error
	}{
		{name: "cron", path: options.CronDatabase, fn: rocacron.ApplySchema},
		{name: "ops", path: options.OpsDatabase, fn: rocaops.ApplySchema},
		{name: "corpus", path: options.CorpusDatabase, fn: rocacorpus.ApplySchema},
	} {
		if err := apply.fn(apply.path); err != nil {
			return fmt.Errorf("prepare the DATA-4 %s destination: %w", apply.name, err)
		}
	}
	return nil
}

func openDestinations(options LegacyOptions) (map[destination]*sql.DB, error) {
	paths := map[destination]string{
		destinationCron: options.CronDatabase, destinationOps: options.OpsDatabase,
		destinationCorpus: options.CorpusDatabase,
	}
	databases := make(map[destination]*sql.DB, len(paths))
	for owner, path := range paths {
		db, err := bundledplugin.OpenDatabase(path, false)
		if err != nil {
			closeDatabases(databases)
			return nil, fmt.Errorf("open the DATA-4 %s destination: %w", owner, err)
		}
		databases[owner] = db
	}
	return databases, nil
}

func closeDatabases(databases map[destination]*sql.DB) {
	for _, db := range databases {
		_ = db.Close()
	}
}

func (importer legacyImporter) importTable(ctx context.Context, plan legacyPlan) (LegacyTableResult, error) {
	columns, err := tableColumns(ctx, importer.source, plan.sourceTable)
	if err != nil {
		return LegacyTableResult{}, err
	}
	if err := requireColumns(columns, plan); err != nil {
		return LegacyTableResult{}, err
	}
	query := fmt.Sprintf("SELECT %s FROM %s ORDER BY %s",
		quotedColumns(columns), quoteIdentifier(plan.sourceTable), quotedColumns(plan.keyColumns))
	rows, err := importer.source.QueryContext(ctx, query)
	if err != nil {
		return LegacyTableResult{}, fmt.Errorf("read legacy %s rows: %w", plan.sourceTable, err)
	}
	defer rows.Close()

	result := LegacyTableResult{SourceTable: plan.sourceTable,
		DestinationTable: plan.destinationTable, Disposition: string(plan.destination)}
	var digestRecords []legacyRow
	batchNumber := 0
	chunk := make([]legacyRow, 0, importer.batchSize)
	flush := func() error {
		batchNumber++
		if err := importer.applyBatch(ctx, plan, batchNumber, chunk); err != nil {
			return err
		}
		result.BatchCount++
		if importer.afterBatch != nil {
			if err := importer.afterBatch(plan.sourceTable, batchNumber); err != nil {
				return err
			}
		}
		chunk = chunk[:0]
		return nil
	}
	for rows.Next() {
		record, err := scanLegacyRow(rows, columns, plan.keyColumns)
		if err != nil {
			return LegacyTableResult{}, fmt.Errorf("read legacy %s row: %w", plan.sourceTable, err)
		}
		if plan.recordType != "" {
			record.digest = digestLegacyRecord(plan.recordType, record.payload)
		}
		digestRecords = append(digestRecords, legacyRow{sourceKey: record.sourceKey, digest: record.digest})
		result.SourceRows++
		chunk = append(chunk, record)
		if len(chunk) == importer.batchSize {
			if err := flush(); err != nil {
				return LegacyTableResult{}, err
			}
		}
	}
	if err := rows.Err(); err != nil {
		return LegacyTableResult{}, fmt.Errorf("read legacy %s rows: %w", plan.sourceTable, err)
	}
	if len(chunk) != 0 || result.SourceRows == 0 {
		if err := flush(); err != nil {
			return LegacyTableResult{}, err
		}
	}
	result.CanonicalDigest = digestRowsSorted(digestRecords)
	physicalRows, err := importer.verifyTable(ctx, plan, result)
	if err != nil {
		return LegacyTableResult{}, err
	}
	result.PhysicalRows = physicalRows
	return result, nil
}

func (importer legacyImporter) applyBatch(ctx context.Context, plan legacyPlan, number int, records []legacyRow) error {
	db := importer.dest[plan.destination]
	id := fmt.Sprintf("data4-core-%s-%06d", strings.ReplaceAll(plan.sourceTable, "_", "-"), number)
	commit := migrationledger.BatchCommit{RowCount: len(records), CanonicalDigest: digestRows(records)}
	if len(records) == 0 {
		commit.HighWaterMark = "empty"
	} else {
		commit.HighWaterMark = records[len(records)-1].sourceKey
	}
	existing, found, err := migrationledger.LookupBatch(ctx, db, id)
	if err != nil {
		return err
	}
	if found {
		if existing.SourceDatabase != coreSource || existing.SourceTable != plan.sourceTable ||
			existing.BatchCommit != commit {
			return fmt.Errorf("committed batch %q does not match the frozen %s source", id, plan.sourceTable)
		}
		return nil
	}
	batch, err := migrationledger.BeginBatch(ctx, db, migrationledger.BatchSpec{
		ID: id, SourceDatabase: coreSource, SourceTable: plan.sourceTable,
	})
	if err != nil {
		return fmt.Errorf("begin legacy %s batch %d: %w", plan.sourceTable, number, err)
	}
	defer batch.Rollback()
	for _, record := range records {
		if err := insertLegacyRecord(ctx, batch, plan, record); err != nil {
			return fmt.Errorf("copy legacy %s row %s: %w", plan.sourceTable, record.sourceKey, err)
		}
		if err := batch.AddMembership(ctx, migrationledger.Membership{
			SourceKey: record.sourceKey, DestinationTable: plan.destinationTable,
			DestinationKey: record.digest, CanonicalDigest: record.digest,
		}); err != nil {
			return fmt.Errorf("record legacy %s custody for identity (%s) source key %s: %w",
				plan.sourceTable, strings.Join(plan.keyColumns, ", "), record.sourceKey, err)
		}
	}
	if err := batch.Commit(ctx, commit); err != nil {
		return fmt.Errorf("commit legacy %s batch %d: %w", plan.sourceTable, number, err)
	}
	return nil
}

func insertLegacyRecord(ctx context.Context, batch *migrationledger.Batch, plan legacyPlan, record legacyRow) error {
	if plan.destination == destinationOps {
		_, err := batch.ExecContext(ctx, `INSERT INTO legacy_records
			(canonical_digest, record_type, payload) VALUES (?, ?, ?)
			ON CONFLICT(canonical_digest) DO NOTHING`, record.digest, plan.recordType, record.payload)
		return err
	}
	query := fmt.Sprintf(`INSERT INTO %s (canonical_digest, payload) VALUES (?, ?)
		ON CONFLICT(canonical_digest) DO NOTHING`, quoteIdentifier(plan.destinationTable))
	_, err := batch.ExecContext(ctx, query, record.digest, record.payload)
	return err
}

func (importer legacyImporter) verifyTable(ctx context.Context, plan legacyPlan, result LegacyTableResult) (int, error) {
	db := importer.dest[plan.destination]
	var memberships, missing, physical int
	args := []any{coreSource, plan.sourceTable}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM custody_memberships
		WHERE source_database = ? AND source_table = ?`, args...).Scan(&memberships); err != nil {
		return 0, fmt.Errorf("count legacy %s memberships: %w", plan.sourceTable, err)
	}
	missingQuery := fmt.Sprintf(`SELECT COUNT(*) FROM custody_memberships AS membership
		LEFT JOIN %s AS destination ON destination.canonical_digest = membership.destination_key
		WHERE membership.source_database = ? AND membership.source_table = ?
		AND (destination.canonical_digest IS NULL OR membership.destination_key <> membership.canonical_digest)`,
		quoteIdentifier(plan.destinationTable))
	if err := db.QueryRowContext(ctx, missingQuery, args...).Scan(&missing); err != nil {
		return 0, fmt.Errorf("verify legacy %s destination rows: %w", plan.sourceTable, err)
	}
	physicalQuery := fmt.Sprintf(`SELECT COUNT(*) FROM %s AS destination
		WHERE EXISTS (SELECT 1 FROM custody_memberships AS membership
			WHERE membership.destination_key = destination.canonical_digest
			AND membership.source_database = ? AND membership.source_table = ?)`,
		quoteIdentifier(plan.destinationTable))
	if err := db.QueryRowContext(ctx, physicalQuery, args...).Scan(&physical); err != nil {
		return 0, fmt.Errorf("count physical legacy %s rows: %w", plan.sourceTable, err)
	}
	rows, err := db.QueryContext(ctx, `SELECT source_key, canonical_digest FROM custody_memberships
		WHERE source_database = ? AND source_table = ? ORDER BY source_key`, args...)
	if err != nil {
		return 0, fmt.Errorf("verify legacy %s digests: %w", plan.sourceTable, err)
	}
	defer rows.Close()
	digest := sha256.New()
	for rows.Next() {
		var record legacyRow
		if err := rows.Scan(&record.sourceKey, &record.digest); err != nil {
			return 0, fmt.Errorf("verify legacy %s digest row: %w", plan.sourceTable, err)
		}
		writeDigestRecord(digest, record)
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("verify legacy %s digests: %w", plan.sourceTable, err)
	}
	gotDigest := hex.EncodeToString(digest.Sum(nil))
	if memberships != result.SourceRows || missing != 0 || gotDigest != result.CanonicalDigest {
		return 0, fmt.Errorf("legacy %s verification failed: source=%d memberships=%d missing=%d digest=%s want=%s",
			plan.sourceTable, result.SourceRows, memberships, missing, gotDigest, result.CanonicalDigest)
	}
	return physical, nil
}

func tableColumns(ctx context.Context, db *sql.DB, table string) ([]string, error) {
	rows, err := db.QueryContext(ctx, fmt.Sprintf("PRAGMA table_info(%s)", quoteIdentifier(table)))
	if err != nil {
		return nil, fmt.Errorf("inspect legacy %s columns: %w", table, err)
	}
	defer rows.Close()
	var columns []string
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, declaredType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &declaredType, &notNull, &defaultValue, &primaryKey); err != nil {
			return nil, fmt.Errorf("inspect legacy %s columns: %w", table, err)
		}
		columns = append(columns, name)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("inspect legacy %s columns: %w", table, err)
	}
	if len(columns) == 0 {
		return nil, fmt.Errorf("legacy source table %s has no columns", table)
	}
	return columns, nil
}

func requireColumns(columns []string, plan legacyPlan) error {
	available := make(map[string]struct{}, len(columns))
	for _, column := range columns {
		available[column] = struct{}{}
	}
	for _, column := range plan.keyColumns {
		if _, ok := available[column]; !ok {
			return fmt.Errorf("legacy source table %s lacks identity column %s", plan.sourceTable, column)
		}
	}
	return nil
}

func scanLegacyRow(rows *sql.Rows, columns, keyColumns []string) (legacyRow, error) {
	values := make([]any, len(columns))
	destinations := make([]any, len(columns))
	for index := range values {
		destinations[index] = &values[index]
	}
	if err := rows.Scan(destinations...); err != nil {
		return legacyRow{}, err
	}
	payload := make(map[string]any, len(columns))
	for index, column := range columns {
		payload[column] = encodeSourceValue(values[index])
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return legacyRow{}, fmt.Errorf("encode the source payload: %w", err)
	}
	keyValues := make([]any, len(keyColumns))
	for index, column := range keyColumns {
		value, ok := payload[column]
		if !ok || value == nil {
			return legacyRow{}, fmt.Errorf("source identity column %s is NULL in identity (%s)",
				column, strings.Join(keyColumns, ", "))
		}
		keyValues[index] = value
	}
	key, err := encodeSourceKey(keyValues)
	if err != nil {
		return legacyRow{}, err
	}
	digest := sha256.Sum256(encoded)
	return legacyRow{sourceKey: key, payload: string(encoded), digest: hex.EncodeToString(digest[:])}, nil
}

func encodeSourceValue(value any) any {
	switch typed := value.(type) {
	case []byte:
		return map[string]string{"$blob": hex.EncodeToString(typed)}
	case string:
		if !utf8.ValidString(typed) {
			return map[string]string{"$text_blob": hex.EncodeToString([]byte(typed))}
		}
	}
	return value
}

func encodeSourceKey(values []any) (string, error) {
	if len(values) == 1 {
		switch value := values[0].(type) {
		case string:
			return value, nil
		case int64:
			return fmt.Sprintf("%d", value), nil
		case float64:
			return fmt.Sprintf("%v", value), nil
		}
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		return "", fmt.Errorf("encode the source identity: %w", err)
	}
	return string(encoded), nil
}

func digestRows(records []legacyRow) string {
	digest := sha256.New()
	for _, record := range records {
		writeDigestRecord(digest, record)
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func digestRowsSorted(records []legacyRow) string {
	sorted := append([]legacyRow(nil), records...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].sourceKey < sorted[j].sourceKey })
	return digestRows(sorted)
}

func digestLegacyRecord(recordType, payload string) string {
	digest := sha256.New()
	writeDigestField(digest, recordType)
	writeDigestField(digest, payload)
	return hex.EncodeToString(digest.Sum(nil))
}

func writeDigestRecord(digest hash.Hash, record legacyRow) {
	writeDigestField(digest, record.sourceKey)
	writeDigestField(digest, record.digest)
}

func writeDigestField(digest hash.Hash, value string) {
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(value)))
	_, _ = digest.Write(size[:])
	_, _ = digest.Write([]byte(value))
}

func tableCount(ctx context.Context, db *sql.DB, table string) (int, error) {
	var count int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+quoteIdentifier(table)).Scan(&count); err != nil {
		return 0, fmt.Errorf("count DATA-4 source table %s: %w", table, err)
	}
	return count, nil
}

func quotedColumns(columns []string) string {
	quoted := make([]string, len(columns))
	for index, column := range columns {
		quoted[index] = quoteIdentifier(column)
	}
	return strings.Join(quoted, ", ")
}

func quoteIdentifier(identifier string) string {
	return `"` + strings.ReplaceAll(identifier, `"`, `""`) + `"`
}
