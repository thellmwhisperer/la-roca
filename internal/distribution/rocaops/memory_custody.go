package rocaops

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/thellmwhisperer/la-roca/internal/distribution/bundledplugin"
	"github.com/thellmwhisperer/la-roca/internal/distribution/migrationledger"
	"github.com/thellmwhisperer/la-roca/internal/securefile"
)

const defaultMemoryBatchSize = 250

const (
	coreMemorySource   = "core"
	corpusMemorySource = "plugin:roca-corpus"
	opsMemorySource    = "plugin:roca-ops"
)

// MemoryCustodyOptions names the three databases DATA-2 freezes and the ops
// database that receives their memory identities. SnapshotDir is intentionally
// explicit: callers keep the verified VACUUM copies beside their other Roca
// backups, never in a process-global temporary directory.
type MemoryCustodyOptions struct {
	CorePath    string
	CorpusPath  string
	OpsPath     string
	SnapshotDir string
	LockPath    string
	BatchSize   int
	AfterBatch  func(MemoryBatch) error
}

type MemoryBatch struct {
	ID             string
	SourceDatabase string
	RowCount       int
	HighWaterMark  string
}

type MemoryCustodyReport struct {
	State              migrationledger.State
	Memberships        int
	PhysicalRecords    int
	FTSRecords         int
	VerificationDigest string
	Snapshots          []string
}

type memorySource struct {
	name, path string
}

type memoryRow struct {
	id             int64
	layer          string
	content        string
	metadata       sql.NullString
	origin         string
	provenance     sql.NullString
	sourceAgent    sql.NullString
	sourceModel    sql.NullString
	sourceSurface  sql.NullString
	sourceSession  sql.NullString
	sourceSequence sql.NullInt64
	project        sql.NullString
	status         sql.NullString
	supersedes     sql.NullInt64
	createdAt      sql.NullString
	expiresAt      sql.NullString
	digest         string
}

// MigrateMemoryCustody copies memory identities from verified snapshots into
// ops' hidden DATA-2 tables. The currently served memories and FTS tables are
// never selected as a destination, so a completed shadow migration cannot
// change an answer before cutover.
func MigrateMemoryCustody(ctx context.Context, options MemoryCustodyOptions) (MemoryCustodyReport, error) {
	if err := options.valid(); err != nil {
		return MemoryCustodyReport{}, err
	}
	batchSize := options.BatchSize
	if batchSize == 0 {
		batchSize = defaultMemoryBatchSize
	}

	release, err := custodyLock(options.LockPath)
	if err != nil {
		return MemoryCustodyReport{}, err
	}
	defer release()

	ops, err := bundledplugin.OpenDatabase(options.OpsPath, false)
	if err != nil {
		return MemoryCustodyReport{}, fmt.Errorf("open ops memory custody: %w", err)
	}
	defer ops.Close()
	state, err := migrationledger.Inspect(ctx, ops)
	if err != nil {
		return MemoryCustodyReport{}, err
	}
	if state.State == migrationledger.StateVerified {
		return inspectMemoryCustody(ctx, ops, state)
	}
	if state.State != migrationledger.StatePrepared && state.State != migrationledger.StateBatchInProgress {
		return MemoryCustodyReport{}, fmt.Errorf("ops memory custody is %q, want prepared or batch-in-progress", state.State)
	}

	sources := []memorySource{
		{name: opsMemorySource, path: options.OpsPath},
		{name: coreMemorySource, path: options.CorePath},
		{name: corpusMemorySource, path: options.CorpusPath},
	}
	snapshots := make([]string, 0, len(sources))
	rowsBySource := make(map[string][]memoryRow, len(sources))
	for _, source := range sources {
		snapshot, snapshotErr := snapshotMemories(ctx, source, options.SnapshotDir)
		if snapshotErr != nil {
			return MemoryCustodyReport{Snapshots: snapshots}, snapshotErr
		}
		snapshots = append(snapshots, snapshot)
		rows, readErr := readMemoryRows(ctx, source.name, snapshot)
		if readErr != nil {
			return MemoryCustodyReport{Snapshots: snapshots}, readErr
		}
		rowsBySource[source.name] = rows
	}

	for _, source := range sources {
		rows := rowsBySource[source.name]
		imported, importedErr := importedMemoryDigests(ctx, ops, source.name)
		if importedErr != nil {
			return MemoryCustodyReport{Snapshots: snapshots}, importedErr
		}
		pending := make([]memoryRow, 0, len(rows))
		for _, row := range rows {
			storedDigest, found := imported[row.id]
			if !found {
				pending = append(pending, row)
				continue
			}
			if storedDigest != row.digest {
				return MemoryCustodyReport{Snapshots: snapshots}, fmt.Errorf(
					"%s memory %d changed after its custody batch committed", source.name, row.id)
			}
		}
		rows = pending
		for first := 0; first < len(rows); first += batchSize {
			last := min(first+batchSize, len(rows))
			group := rows[first:last]
			batchID := memoryBatchID(source.name, group[0].id, group[len(group)-1].id)
			batch, beginErr := migrationledger.BeginBatch(ctx, ops, migrationledger.BatchSpec{
				ID: batchID, SourceDatabase: source.name, SourceTable: "memories",
			})
			if beginErr != nil {
				if errors.Is(beginErr, migrationledger.ErrBatchCommitted) {
					continue
				}
				return MemoryCustodyReport{Snapshots: snapshots}, beginErr
			}
			commit, importErr := importMemoryBatch(ctx, ops, batch, source.name, group)
			if importErr != nil {
				_ = batch.Rollback()
				return MemoryCustodyReport{Snapshots: snapshots}, importErr
			}
			if commitErr := batch.Commit(ctx, commit); commitErr != nil {
				return MemoryCustodyReport{Snapshots: snapshots}, commitErr
			}
			if options.AfterBatch != nil {
				progress := MemoryBatch{ID: batchID, SourceDatabase: source.name,
					RowCount: len(group), HighWaterMark: commit.HighWaterMark}
				if callbackErr := options.AfterBatch(progress); callbackErr != nil {
					return MemoryCustodyReport{State: migrationledger.StateBatchInProgress,
						Snapshots: snapshots}, callbackErr
				}
			}
		}
	}

	if _, err := ops.ExecContext(ctx,
		"INSERT INTO memory_records_fts(memory_records_fts) VALUES ('rebuild')"); err != nil {
		return MemoryCustodyReport{Snapshots: snapshots}, fmt.Errorf("rebuild ops memory FTS: %w", err)
	}
	report, digest, err := verifyMemoryCustody(ctx, ops, rowsBySource)
	if err != nil {
		return MemoryCustodyReport{Snapshots: snapshots}, err
	}
	if err := migrationledger.Verify(ctx, ops, digest); err != nil {
		return MemoryCustodyReport{Snapshots: snapshots}, err
	}
	report.State = migrationledger.StateVerified
	report.VerificationDigest = digest
	report.Snapshots = snapshots
	return report, nil
}

func (options MemoryCustodyOptions) valid() error {
	if strings.TrimSpace(options.CorePath) == "" || strings.TrimSpace(options.CorpusPath) == "" ||
		strings.TrimSpace(options.OpsPath) == "" || strings.TrimSpace(options.SnapshotDir) == "" {
		return fmt.Errorf("memory custody needs core, corpus, ops, and snapshot paths")
	}
	if options.BatchSize < 0 {
		return fmt.Errorf("memory custody batch size cannot be negative")
	}
	return nil
}

func custodyLock(path string) (func() error, error) {
	if path == "" {
		return func() error { return nil }, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create memory custody lock directory: %w", err)
	}
	release, err := securefile.Lock(path)
	if err != nil {
		return nil, fmt.Errorf("lock memory custody sources: %w", err)
	}
	return release, nil
}

func snapshotMemories(ctx context.Context, source memorySource, directory string) (string, error) {
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", fmt.Errorf("create memory snapshot directory: %w", err)
	}
	prefix := "." + strings.NewReplacer(":", "-", "/", "-").Replace(source.name) + "-"
	placeholder, err := os.CreateTemp(directory, prefix+"*.snapshot.db")
	if err != nil {
		return "", fmt.Errorf("reserve memory snapshot path: %w", err)
	}
	path := placeholder.Name()
	if closeErr := placeholder.Close(); closeErr != nil {
		return "", closeErr
	}
	if removeErr := os.Remove(path); removeErr != nil {
		return "", removeErr
	}

	sourceDB, err := bundledplugin.OpenDatabase(source.path, true)
	if err != nil {
		return "", fmt.Errorf("open %s memory source: %w", source.name, err)
	}
	defer sourceDB.Close()
	if _, err := sourceDB.ExecContext(ctx, "VACUUM INTO ?", path); err != nil {
		return "", fmt.Errorf("freeze %s memories: %w", source.name, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return "", fmt.Errorf("restrict %s memory snapshot: %w", source.name, err)
	}

	copyDB, err := bundledplugin.OpenDatabase(path, true)
	if err != nil {
		return "", fmt.Errorf("open %s memory snapshot: %w", source.name, err)
	}
	defer copyDB.Close()
	var sourceCount, copyCount int
	var integrity string
	if err := sourceDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM memories").Scan(&sourceCount); err != nil {
		return "", fmt.Errorf("count %s source memories: %w", source.name, err)
	}
	if err := copyDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM memories").Scan(&copyCount); err != nil {
		return "", fmt.Errorf("count %s snapshot memories: %w", source.name, err)
	}
	if err := copyDB.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&integrity); err != nil {
		return "", fmt.Errorf("check %s memory snapshot: %w", source.name, err)
	}
	if sourceCount != copyCount || integrity != "ok" {
		return "", fmt.Errorf("%s memory snapshot verification failed: source=%d copy=%d integrity=%s",
			source.name, sourceCount, copyCount, integrity)
	}
	return path, nil
}

func readMemoryRows(ctx context.Context, source, path string) ([]memoryRow, error) {
	db, err := bundledplugin.OpenDatabase(path, true)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	columns, err := memoryColumns(ctx, db)
	if err != nil {
		return nil, err
	}
	provenance := "NULL"
	if columns["provenance"] {
		provenance = "provenance"
	}
	expiresAt := "NULL"
	if columns["expires_at"] {
		expiresAt = "expires_at"
	}
	query := `SELECT id, layer, content, metadata, origin, ` + provenance + `,
		source_agent, source_model, source_surface, source_session, source_sequence,
		project, status, supersedes, created_at, ` + expiresAt + ` FROM memories ORDER BY id`
	result, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("read %s memory snapshot: %w", source, err)
	}
	defer result.Close()
	var rows []memoryRow
	for result.Next() {
		var row memoryRow
		if err := result.Scan(&row.id, &row.layer, &row.content, &row.metadata, &row.origin,
			&row.provenance, &row.sourceAgent, &row.sourceModel, &row.sourceSurface,
			&row.sourceSession, &row.sourceSequence, &row.project, &row.status,
			&row.supersedes, &row.createdAt, &row.expiresAt); err != nil {
			return nil, fmt.Errorf("scan %s memory: %w", source, err)
		}
		row.digest = row.canonicalDigest()
		rows = append(rows, row)
	}
	if err := result.Err(); err != nil {
		return nil, fmt.Errorf("read %s memories: %w", source, err)
	}
	return rows, nil
}

func memoryColumns(ctx context.Context, db *sql.DB) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, "PRAGMA table_info('memories')")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns := make(map[string]bool)
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, kind string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &kind, &notNull, &defaultValue, &primaryKey); err != nil {
			return nil, err
		}
		columns[name] = true
	}
	return columns, rows.Err()
}

func importMemoryBatch(ctx context.Context, ops *sql.DB, batch *migrationledger.Batch,
	source string, rows []memoryRow) (migrationledger.BatchCommit, error) {
	digest := sha256.New()
	reserved := make(map[int64]struct{})
	for _, row := range rows {
		destination, found, err := aliasDestination(ctx, ops, source, row.digest, reserved)
		if err != nil {
			return migrationledger.BatchCommit{}, err
		}
		if !found {
			result, insertErr := batch.ExecContext(ctx, `INSERT INTO memory_records
				(canonical_digest, provenance, layer, content, metadata, origin, source_agent,
				 source_model, source_surface, source_session, source_sequence, project, status,
				 supersedes, created_at, expires_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, row.digest, source,
				row.layer, row.content, nullableString(row.metadata), row.origin,
				nullableString(row.sourceAgent), nullableString(row.sourceModel),
				nullableString(row.sourceSurface), nullableString(row.sourceSession),
				nullableInt(row.sourceSequence), nullableString(row.project), nullableString(row.status),
				nullableInt(row.supersedes), nullableString(row.createdAt), nullableString(row.expiresAt))
			if insertErr != nil {
				return migrationledger.BatchCommit{}, fmt.Errorf("store %s memory %d in ops custody: %w",
					source, row.id, insertErr)
			}
			destination, err = result.LastInsertId()
			if err != nil {
				return migrationledger.BatchCommit{}, fmt.Errorf("read ops memory destination: %w", err)
			}
		}
		reserved[destination] = struct{}{}
		sourceKey := strconv.FormatInt(row.id, 10)
		if _, err := batch.ExecContext(ctx, `INSERT INTO memory_provenance
			(source_database, source_key, provenance) VALUES (?, ?, ?)`, source, sourceKey,
			nullableString(row.provenance)); err != nil {
			return migrationledger.BatchCommit{}, fmt.Errorf("store %s memory provenance: %w", source, err)
		}
		if err := batch.AddMembership(ctx, migrationledger.Membership{
			SourceKey: sourceKey, DestinationTable: "memory_records",
			DestinationKey: strconv.FormatInt(destination, 10), CanonicalDigest: row.digest,
		}); err != nil {
			return migrationledger.BatchCommit{}, err
		}
		writeField(digest, source)
		writeField(digest, sourceKey)
		writeField(digest, strconv.FormatInt(destination, 10))
		writeField(digest, row.digest)
	}
	return migrationledger.BatchCommit{
		RowCount: len(rows), CanonicalDigest: fmt.Sprintf("%x", digest.Sum(nil)),
		HighWaterMark: strconv.FormatInt(rows[len(rows)-1].id, 10),
	}, nil
}

// aliasDestination performs a multiset union, not ordinary deduplication. One
// physical record can represent at most one identity from each source. A second
// byte-equal core row therefore remains physical even when the first one aliases
// a corpus row, preserving the historical duplicate population.
func aliasDestination(ctx context.Context, ops *sql.DB, source, digest string,
	reserved map[int64]struct{}) (int64, bool, error) {
	rows, err := ops.QueryContext(ctx, `SELECT records.id
		FROM memory_records AS records
		JOIN custody_memberships AS represented
		  ON represented.destination_table = 'memory_records'
		 AND CAST(represented.destination_key AS INTEGER) = records.id
		WHERE records.canonical_digest = ?
		  AND represented.source_database <> ?
		  AND NOT EXISTS (
		    SELECT 1 FROM custody_memberships AS same_source
		    WHERE same_source.destination_table = 'memory_records'
		      AND CAST(same_source.destination_key AS INTEGER) = records.id
		      AND same_source.source_database = ?)
		ORDER BY records.id`, digest, source, source)
	if err != nil {
		return 0, false, fmt.Errorf("look for an exact cross-source memory: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var destination int64
		if err := rows.Scan(&destination); err != nil {
			return 0, false, fmt.Errorf("read an exact cross-source memory: %w", err)
		}
		if _, used := reserved[destination]; !used {
			return destination, true, nil
		}
	}
	if err := rows.Err(); err != nil {
		return 0, false, fmt.Errorf("look for an exact cross-source memory: %w", err)
	}
	return 0, false, nil
}

func memoryBatchID(source string, first, last int64) string {
	name := strings.NewReplacer(":", "-", "/", "-").Replace(source)
	return fmt.Sprintf("data2-%s-memories-%020d-%020d", name, first, last)
}

func importedMemoryDigests(ctx context.Context, db *sql.DB, source string) (map[int64]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT source_key, canonical_digest
		FROM custody_memberships WHERE source_database = ? AND source_table = 'memories'`, source)
	if err != nil {
		return nil, fmt.Errorf("read imported %s memory identities: %w", source, err)
	}
	defer rows.Close()
	imported := make(map[int64]string)
	for rows.Next() {
		var sourceKey, digest string
		if err := rows.Scan(&sourceKey, &digest); err != nil {
			return nil, fmt.Errorf("read imported %s memory identity: %w", source, err)
		}
		id, err := strconv.ParseInt(sourceKey, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("read imported %s memory key %q: %w", source, sourceKey, err)
		}
		imported[id] = digest
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read imported %s memory identities: %w", source, err)
	}
	return imported, nil
}

func verifyMemoryCustody(ctx context.Context, ops *sql.DB,
	rowsBySource map[string][]memoryRow) (MemoryCustodyReport, string, error) {
	var integrity string
	if err := ops.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&integrity); err != nil || integrity != "ok" {
		return MemoryCustodyReport{}, "", fmt.Errorf("ops memory integrity check = %q: %w", integrity, err)
	}
	var foreignKeys, memberships, physical, expectedPhysical, fts, orphans, missingFTS int
	checks := []struct {
		query string
		into  *int
	}{
		{"SELECT COUNT(*) FROM pragma_foreign_key_check", &foreignKeys},
		{"SELECT COUNT(*) FROM custody_memberships WHERE destination_table = 'memory_records'", &memberships},
		{"SELECT COUNT(*) FROM memory_records", &physical},
		{`SELECT COALESCE(SUM(maximum), 0) FROM (
			SELECT canonical_digest, MAX(source_count) AS maximum FROM (
				SELECT canonical_digest, source_database, COUNT(*) AS source_count
				FROM custody_memberships WHERE destination_table = 'memory_records'
				GROUP BY canonical_digest, source_database)
			GROUP BY canonical_digest)`, &expectedPhysical},
		{"SELECT COUNT(*) FROM memory_records_fts", &fts},
		{`SELECT COUNT(*) FROM memory_records AS records
			WHERE NOT EXISTS (SELECT 1 FROM custody_memberships AS memberships
				WHERE memberships.destination_table = 'memory_records'
				  AND CAST(memberships.destination_key AS INTEGER) = records.id)`, &orphans},
		{`SELECT COUNT(*) FROM memory_records AS records
			LEFT JOIN memory_records_fts AS indexed ON indexed.rowid = records.id
			WHERE indexed.rowid IS NULL`, &missingFTS},
	}
	for _, check := range checks {
		if err := ops.QueryRowContext(ctx, check.query).Scan(check.into); err != nil {
			return MemoryCustodyReport{}, "", fmt.Errorf("verify ops memory custody: %w", err)
		}
	}
	expectedMemberships := 0
	for source, rows := range rowsBySource {
		expectedMemberships += len(rows)
		var got int
		if err := ops.QueryRowContext(ctx, `SELECT COUNT(*) FROM custody_memberships
			WHERE destination_table = 'memory_records' AND source_database = ?`, source).Scan(&got); err != nil {
			return MemoryCustodyReport{}, "", err
		}
		if got != len(rows) {
			return MemoryCustodyReport{}, "", fmt.Errorf("%s memory memberships = %d, want %d", source, got, len(rows))
		}
	}
	if foreignKeys != 0 || memberships != expectedMemberships || physical != expectedPhysical ||
		fts != physical || orphans != 0 || missingFTS != 0 {
		return MemoryCustodyReport{}, "", fmt.Errorf(
			"ops memory verification failed: foreign_keys=%d memberships=%d/%d physical=%d/%d fts=%d orphans=%d missing_fts=%d",
			foreignKeys, memberships, expectedMemberships, physical, expectedPhysical, fts, orphans, missingFTS)
	}
	if _, err := ops.ExecContext(ctx,
		"INSERT INTO memory_records_fts(memory_records_fts, rank) VALUES ('integrity-check', 1)"); err != nil {
		return MemoryCustodyReport{}, "", fmt.Errorf("verify ops memory FTS content: %w", err)
	}

	digest, err := memoryVerificationDigest(ctx, ops, memberships, physical, fts)
	if err != nil {
		return MemoryCustodyReport{}, "", err
	}
	return MemoryCustodyReport{Memberships: memberships, PhysicalRecords: physical, FTSRecords: fts}, digest, nil
}

func inspectMemoryCustody(ctx context.Context, ops *sql.DB,
	state migrationledger.Snapshot) (MemoryCustodyReport, error) {
	report := MemoryCustodyReport{State: state.State, VerificationDigest: state.VerificationDigest}
	queries := []struct {
		statement string
		target    *int
	}{
		{"SELECT COUNT(*) FROM custody_memberships WHERE destination_table = 'memory_records'", &report.Memberships},
		{"SELECT COUNT(*) FROM memory_records", &report.PhysicalRecords},
		{"SELECT COUNT(*) FROM memory_records_fts", &report.FTSRecords},
	}
	for _, query := range queries {
		if err := ops.QueryRowContext(ctx, query.statement).Scan(query.target); err != nil {
			return MemoryCustodyReport{}, err
		}
	}
	return report, nil
}

func memoryVerificationDigest(ctx context.Context, ops *sql.DB,
	memberships, physical, fts int) (string, error) {
	digest := sha256.New()
	for _, value := range []int{memberships, physical, fts} {
		writeField(digest, strconv.Itoa(value))
	}
	rows, err := ops.QueryContext(ctx, `SELECT batch_id, source_database, source_table,
		row_count, canonical_digest, high_water_mark FROM migration_batches ORDER BY batch_id`)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	for rows.Next() {
		var id, source, table, canonical, highWater string
		var count int
		if err := rows.Scan(&id, &source, &table, &count, &canonical, &highWater); err != nil {
			return "", err
		}
		for _, field := range []string{id, source, table, strconv.Itoa(count), canonical, highWater} {
			writeField(digest, field)
		}
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", digest.Sum(nil)), nil
}

func (row memoryRow) canonicalDigest() string {
	digest := sha256.New()
	writeField(digest, row.layer)
	writeField(digest, row.content)
	writeNullString(digest, row.metadata)
	writeField(digest, row.origin)
	writeNullString(digest, row.sourceAgent)
	writeNullString(digest, row.sourceModel)
	writeNullString(digest, row.sourceSurface)
	writeNullString(digest, row.sourceSession)
	writeNullInt(digest, row.sourceSequence)
	writeNullString(digest, row.project)
	writeNullString(digest, row.status)
	writeNullInt(digest, row.supersedes)
	writeNullString(digest, row.createdAt)
	writeNullString(digest, row.expiresAt)
	return fmt.Sprintf("%x", digest.Sum(nil))
}

type fieldWriter interface {
	Write([]byte) (int, error)
}

func writeField(writer fieldWriter, value string) {
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(value)))
	_, _ = writer.Write(size[:])
	_, _ = writer.Write([]byte(value))
}

func writeNullString(writer fieldWriter, value sql.NullString) {
	if !value.Valid {
		writeField(writer, "\x00")
		return
	}
	writeField(writer, "\x01"+value.String)
}

func writeNullInt(writer fieldWriter, value sql.NullInt64) {
	if !value.Valid {
		writeField(writer, "\x00")
		return
	}
	writeField(writer, "\x01"+strconv.FormatInt(value.Int64, 10))
}

func nullableString(value sql.NullString) any {
	if !value.Valid {
		return nil
	}
	return value.String
}

func nullableInt(value sql.NullInt64) any {
	if !value.Valid {
		return nil
	}
	return value.Int64
}
