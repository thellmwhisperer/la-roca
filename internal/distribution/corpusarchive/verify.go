package corpusarchive

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
)

type familySpec struct {
	name, destinationTable, ftsTable, divergentSQL string
}

var archiveFamilies = []familySpec{
	{
		name: "sessions", destinationTable: "session_versions",
		ftsTable: "session_versions_fts",
		divergentSQL: `SELECT COUNT(*) FROM (
			SELECT session_id FROM corpus_source_rows
			WHERE source_table = 'sessions'
			GROUP BY session_id
			HAVING COUNT(DISTINCT source_database) > 1
			   AND COUNT(DISTINCT version_digest) > 1)`,
	},
	{
		name: "exchanges", destinationTable: "exchange_versions",
		ftsTable: "exchange_versions_fts",
		divergentSQL: `SELECT COUNT(*) FROM (
			SELECT session_id, exchange_number FROM corpus_source_rows
			WHERE source_table = 'exchanges'
			GROUP BY session_id, exchange_number
			HAVING COUNT(DISTINCT source_database) > 1
			   AND COUNT(DISTINCT version_digest) > 1)`,
	},
	{name: "tool_uses", destinationTable: "tool_use_versions"},
	{
		name: "thinking_blocks", destinationTable: "thinking_block_versions",
		ftsTable: "thinking_block_versions_fts",
	},
	{name: "ingest_file_state", destinationTable: "ingest_file_state_versions"},
}

func rebuildArchiveFTS(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin corpus FTS rebuild: %w", err)
	}
	defer tx.Rollback()
	for _, family := range archiveFamilies {
		if family.ftsTable == "" {
			continue
		}
		statement := fmt.Sprintf("INSERT INTO %s(%s) VALUES ('rebuild')",
			family.ftsTable, family.ftsTable)
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("rebuild %s: %w", family.ftsTable, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit corpus FTS rebuild: %w", err)
	}
	return nil
}

func verifyArchive(ctx context.Context, db *sql.DB) (Report, error) {
	if err := verifyDatabaseIntegrity(ctx, db); err != nil {
		return Report{}, err
	}
	report := Report{Families: make(map[string]FamilyReport)}
	sources, err := verifySourceMemberships(ctx, db)
	if err != nil {
		return Report{}, err
	}
	report.Sources = sources
	for _, family := range archiveFamilies {
		verified, err := verifyFamily(ctx, db, family)
		if err != nil {
			return Report{}, err
		}
		report.Families[family.name] = verified
	}
	if err := verifyDestinationStateWins(ctx, db); err != nil {
		return Report{}, err
	}
	return report, nil
}

func verifyDatabaseIntegrity(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, "PRAGMA integrity_check")
	if err != nil {
		return fmt.Errorf("run corpus integrity_check: %w", err)
	}
	defer rows.Close()
	var failures []string
	for rows.Next() {
		var result string
		if err := rows.Scan(&result); err != nil {
			return err
		}
		if result != "ok" {
			failures = append(failures, result)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(failures) > 0 {
		return fmt.Errorf("corpus integrity_check failed: %s", strings.Join(failures, "; "))
	}
	foreignKeys, err := db.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return fmt.Errorf("run corpus foreign_key_check: %w", err)
	}
	defer foreignKeys.Close()
	if foreignKeys.Next() {
		return fmt.Errorf("corpus foreign_key_check found at least one violation")
	}
	return foreignKeys.Err()
}

func verifySourceMemberships(ctx context.Context, db *sql.DB) ([]SourceReport, error) {
	rows, err := db.QueryContext(ctx, `SELECT source_database, snapshot_digest
		FROM corpus_source_snapshots ORDER BY source_database`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var reports []SourceReport
	for rows.Next() {
		var source SourceReport
		if err := rows.Scan(&source.Database, &source.SnapshotDigest); err != nil {
			return nil, err
		}
		source.ExpectedRows = make(map[string]int64)
		reports = append(reports, source)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for index := range reports {
		for _, table := range archiveSourceTables {
			var expected, memberships, sourceRows int64
			if err := db.QueryRowContext(ctx, `SELECT expected_rows FROM corpus_source_tables
				WHERE source_database = ? AND source_table = ?`, reports[index].Database,
				table.sourceTable).Scan(&expected); err != nil {
				return nil, fmt.Errorf("read expected count for %s.%s: %w",
					reports[index].Database, table.sourceTable, err)
			}
			if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM custody_memberships
				WHERE source_database = ? AND source_table = ?`, reports[index].Database,
				table.sourceTable).Scan(&memberships); err != nil {
				return nil, err
			}
			if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM corpus_source_rows
				WHERE source_database = ? AND source_table = ?`, reports[index].Database,
				table.sourceTable).Scan(&sourceRows); err != nil {
				return nil, err
			}
			if expected != memberships || expected != sourceRows {
				return nil, fmt.Errorf("source membership coverage for %s.%s is %d/%d, want %d",
					reports[index].Database, table.sourceTable, memberships, sourceRows, expected)
			}
			reports[index].ExpectedRows[table.sourceTable] = expected
		}
	}
	return reports, nil
}

func verifyFamily(ctx context.Context, db *sql.DB, family familySpec) (FamilyReport, error) {
	var report FamilyReport
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM custody_memberships
		WHERE destination_table = ?`, family.destinationTable).Scan(&report.Identities); err != nil {
		return report, err
	}
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+family.destinationTable).
		Scan(&report.PhysicalRows); err != nil {
		return report, err
	}
	var distinctDigests, missingRows, missingEvidence int64
	if err := db.QueryRowContext(ctx, `SELECT COUNT(DISTINCT canonical_digest)
		FROM custody_memberships WHERE destination_table = ?`, family.destinationTable).
		Scan(&distinctDigests); err != nil {
		return report, err
	}
	missingQuery := fmt.Sprintf(`SELECT COUNT(*) FROM custody_memberships AS m
		LEFT JOIN %s AS v ON v.version_digest = m.destination_key
		WHERE m.destination_table = ? AND v.id IS NULL`, family.destinationTable)
	if err := db.QueryRowContext(ctx, missingQuery, family.destinationTable).Scan(&missingRows); err != nil {
		return report, err
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM custody_memberships AS m
		LEFT JOIN corpus_source_rows AS r
		  USING (source_database, source_table, source_key)
		WHERE m.destination_table = ? AND r.source_key IS NULL`, family.destinationTable).
		Scan(&missingEvidence); err != nil {
		return report, err
	}
	if distinctDigests != report.PhysicalRows || missingRows != 0 || missingEvidence != 0 {
		return report, fmt.Errorf("%s physical/membership verification failed: physical=%d unique=%d missing=%d evidence=%d",
			family.name, report.PhysicalRows, distinctDigests, missingRows, missingEvidence)
	}
	report.ExactAliases = report.Identities - report.PhysicalRows
	if report.ExactAliases < 0 {
		return report, fmt.Errorf("%s has fewer identities than physical rows", family.name)
	}
	if family.divergentSQL != "" {
		if err := db.QueryRowContext(ctx, family.divergentSQL).Scan(&report.DivergentKeys); err != nil {
			return report, err
		}
	}
	if family.ftsTable != "" {
		if err := verifyFTS(ctx, db, family, &report); err != nil {
			return report, err
		}
	}
	return report, nil
}

func verifyFTS(ctx context.Context, db *sql.DB, family familySpec, report *FamilyReport) error {
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+family.ftsTable).
		Scan(&report.FTSRows); err != nil {
		return err
	}
	if report.FTSRows != report.PhysicalRows {
		return fmt.Errorf("%s content count is %d, FTS count is %d",
			family.name, report.PhysicalRows, report.FTSRows)
	}
	statement := fmt.Sprintf("INSERT INTO %s(%s, rank) VALUES ('integrity-check', 1)",
		family.ftsTable, family.ftsTable)
	if _, err := db.ExecContext(ctx, statement); err != nil {
		return fmt.Errorf("%s external-content integrity-check failed: %w", family.ftsTable, err)
	}
	return nil
}

func verifyDestinationStateWins(ctx context.Context, db *sql.DB) error {
	var wrong int64
	err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM ingest_file_state_heads AS h
		WHERE h.destination_priority <> 1
		  AND EXISTS (
		    SELECT 1 FROM corpus_source_snapshots AS s
		    JOIN custody_memberships AS m ON m.source_database = s.source_database
		    JOIN ingest_file_state_versions AS v ON v.version_digest = m.destination_key
		    WHERE s.destination_source = 1
		      AND m.destination_table = 'ingest_file_state_versions'
		      AND v.path = h.path
		  )`).Scan(&wrong)
	if err != nil {
		return err
	}
	if wrong != 0 {
		return fmt.Errorf("%d ingest paths did not select the existing corpus state", wrong)
	}
	return nil
}

func reportDigest(report Report) (string, error) {
	report.VerificationDigest = ""
	encoded, err := json.Marshal(report)
	if err != nil {
		return "", fmt.Errorf("serialize corpus archive verification: %w", err)
	}
	return canonicalDigest("corpus-archive-verification", string(encoded)), nil
}
