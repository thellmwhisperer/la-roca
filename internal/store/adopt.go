package store

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/thellmwhisperer/la-roca/data"
	"github.com/thellmwhisperer/la-roca/internal/ingestprovenance"
)

// Verdict of the structural comparison between the database that is there and
// the schema this binary carries.
type Verdict string

const (
	// VerdictCurrent: structurally identical. It is adopted untouched.
	VerdictCurrent Verdict = "current"
	// VerdictMigratable: every difference has a safe in-place repair.
	VerdictMigratable Verdict = "migratable"
	// VerdictIncompatible: some difference has no safe repair.
	VerdictIncompatible Verdict = "incompatible"
	// VerdictForeign: the identity tables are missing. It is not a Roca database.
	VerdictForeign Verdict = "foreign"
)

// identityTables are the ones that make a database a Roca one.
var identityTables = []string{"sessions", "memories", "exchanges"}

// Difference is one concrete structural difference, named so that the
// diagnosis says what is wrong and not how many things are wrong.
type Difference struct {
	Kind   string `json:"kind"`
	Table  string `json:"table"`
	Column string `json:"column,omitempty"`
	Index  string `json:"index,omitempty"`
	Detail string `json:"detail"`
	// Repairable says whether this difference fits inside the repair
	// boundary: create what is missing, never delete and never deduplicate.
	Repairable bool `json:"repairable"`
}

// Report is the result of comparing a database with the v1 schema.
type Report struct {
	Verdict            Verdict      `json:"verdict"`
	Reason             string       `json:"reason"`
	RequiredStructures int          `json:"required_structures"`
	Differences        []Difference `json:"differences,omitempty"`
	// Orphans are tables and views the database carries and v1 does not
	// declare. They are reported and do not block: an existing database may carry
	// proposals, runs or leftovers of withdrawn features, and they are still
	// its data.
	Orphans []string `json:"orphans,omitempty"`
	// Fresh tells creating from adopting: a database with no tables at all is
	// new, not foreign, and has nothing of its own a backup should protect.
	Fresh bool `json:"fresh"`
}

// Adoption is what Adopt did with the database.
type Adoption struct {
	Report
	Adopted    bool     `json:"adopted"`
	Repairs    []string `json:"repairs,omitempty"`
	BackupPath string   `json:"backup_path,omitempty"`
}

// Inspect classifies the database by structure, never by the text of its create
// statements: it compares table and view names and, per column, type affinity,
// NOT NULL, default expression and position in the primary key. A database whose
// every REQUIRED column matches is adopted even when its DDL is written another
// way. The rule is "all required columns match" and not "identical column by
// column": `compare` walks the expected columns, so a table carrying an extra
// column of the operator's own is adopted with it rather than refused. An extra
// TABLE is different and is reported as an orphan.
//
// What this comparison deliberately does not cover: CHECKs and trigger bodies
// are invisible to PRAGMA table_info. They are neither compared nor repaired;
// they are imposed on write by the schema the product creates.
func Inspect(ctx context.Context, db *DB) (Report, error) {
	current, err := readStructure(ctx, db.SQL())
	if err != nil {
		return Report{}, fmt.Errorf("read the structure of %q: %w", db.path, err)
	}
	expected, err := referenceStructure(ctx)
	if err != nil {
		return Report{}, err
	}
	return compare(ctx, db, current, expected), nil
}

// Adopt leaves the database ready for this binary to use.
//
// A current database is adopted untouched. A migratable one is repaired inside
// its boundary and always behind a verified backup. An incompatible or foreign
// one is not touched: the diagnosis is returned naming the difference.
func Adopt(ctx context.Context, db *DB, backupDir string) (Adoption, error) {
	report, err := Inspect(ctx, db)
	if err != nil {
		return Adoption{}, err
	}
	adoption := Adoption{Report: report}

	switch report.Verdict {
	case VerdictForeign, VerdictIncompatible:
		return adoption, fmt.Errorf("the database %q is not adopted: %s", db.path, report.Reason)
	case VerdictMigratable:
		if !report.Fresh {
			backupPath, err := Backup(ctx, db, backupDir)
			if err != nil {
				return adoption, fmt.Errorf("backup before the repair: %w", err)
			}
			adoption.BackupPath = backupPath
		}
		repairs, err := repair(ctx, db, report)
		adoption.Repairs = repairs
		if err != nil {
			return adoption, err
		}
		if err := db.Write(ctx, func(tx *sql.Tx) error {
			return ingestprovenance.Backfill(ctx, tx)
		}); err != nil {
			return adoption, fmt.Errorf("backfill ingest provenance: %w", err)
		}
	}

	after, err := Inspect(ctx, db)
	if err != nil {
		return adoption, err
	}
	adoption.Report = after
	if after.Verdict != VerdictCurrent {
		return adoption, fmt.Errorf(
			"the database %q is still not up to date after repairing: %s", db.path, after.Reason)
	}
	adoption.Adopted = true
	return adoption, nil
}

// repair creates what is missing, in the order SQLite accepts it. It never drops
// a data table and never deletes rows to make a constraint fit.
func repair(ctx context.Context, db *DB, report Report) ([]string, error) {
	reference, closeDB, err := referenceDB(ctx)
	if err != nil {
		return nil, err
	}
	defer closeDB()

	var done []string
	for _, kind := range []string{"missing_table", "missing_column", "missing_index"} {
		for _, d := range report.Differences {
			if d.Kind != kind {
				continue
			}
			stmt, err := repairStatement(ctx, reference, d)
			if err != nil {
				return done, err
			}
			err = db.Write(ctx, func(tx *sql.Tx) error {
				_, err := tx.ExecContext(ctx, stmt)
				return err
			})
			if err != nil {
				return done, fmt.Errorf("reparar %s: %w", d.Detail, err)
			}
			done = append(done, d.Detail)
		}
	}
	return done, nil
}

func repairStatement(ctx context.Context, reference *sql.DB, d Difference) (string, error) {
	switch d.Kind {
	case "missing_table":
		return referenceDDL(ctx, reference, d.Table)
	case "missing_index":
		return referenceDDL(ctx, reference, d.Index)
	case "missing_column":
		return d.Detail, nil
	}
	return "", fmt.Errorf("repair not contemplated: %s", d.Kind)
}

func referenceDDL(ctx context.Context, reference *sql.DB, name string) (string, error) {
	var ddl string
	err := reference.QueryRowContext(ctx,
		"SELECT sql FROM sqlite_master WHERE name = ?", name).Scan(&ddl)
	if err != nil {
		return "", fmt.Errorf("the embedded schema does not declare %q: %w", name, err)
	}
	return ddl, nil
}

// --- structural comparison ---

type column struct {
	name         string
	affinity     string
	notNull      bool
	defaultValue string
	pk           int
}

type index struct {
	name    string
	table   string
	unique  bool
	columns []string
}

type structure struct {
	tables  map[string]map[string]column
	views   map[string]bool
	indexes map[string]index
}

func compare(ctx context.Context, db *DB, current, expected structure) Report {
	report := Report{
		Verdict:            VerdictCurrent,
		RequiredStructures: len(expected.tables) + len(expected.views) + len(expected.indexes),
	}

	for name := range current.tables {
		if _, ok := expected.tables[name]; ok {
			continue
		}
		// The search artefacts are created by this very product and rebuild
		// themselves: they are nobody's data that a diagnosis should name, and
		// counting them as orphans would make every init over an already indexed
		// database warn about tables it has just created itself.
		if isDerived(name) {
			continue
		}
		report.Orphans = append(report.Orphans, name)
	}
	for name := range current.views {
		report.Orphans = append(report.Orphans, name)
	}
	slices.Sort(report.Orphans)

	// A database with no tables at all is a new database, not a foreign one.
	if len(current.tables) == 0 && len(current.views) == 0 {
		report.Verdict = VerdictMigratable
		report.Fresh = true
		report.Reason = "new database: it has no tables yet"
		for name := range expected.tables {
			report.Differences = append(report.Differences, Difference{
				Kind: "missing_table", Table: name, Repairable: true,
				Detail: "the table is missing: " + name,
			})
		}
		for name, idx := range expected.indexes {
			report.Differences = append(report.Differences, Difference{
				Kind: "missing_index", Table: idx.table, Index: name, Repairable: true,
				Detail: "the index is missing: " + name,
			})
		}
		sortDifferences(report.Differences)
		return report
	}

	var missingIdentity []string
	for _, name := range identityTables {
		if _, ok := current.tables[name]; !ok {
			missingIdentity = append(missingIdentity, name)
		}
	}
	if len(missingIdentity) > 0 {
		report.Verdict = VerdictForeign
		report.Reason = "not a Roca database: the identity tables are missing: " +
			strings.Join(missingIdentity, ", ")
		return report
	}

	for name, expectedColumns := range expected.tables {
		currentColumns, ok := current.tables[name]
		if !ok {
			report.Differences = append(report.Differences, Difference{
				Kind: "missing_table", Table: name, Repairable: true,
				Detail: "the table is missing: " + name,
			})
			continue
		}
		for colName, expectedCol := range expectedColumns {
			currentCol, ok := currentColumns[colName]
			if !ok {
				report.Differences = append(report.Differences, missingColumnDifference(name, expectedCol))
				continue
			}
			if reason := differ(expectedCol, currentCol); reason != "" {
				report.Differences = append(report.Differences, Difference{
					Kind: "column_mismatch", Table: name, Column: colName,
					Detail: fmt.Sprintf("%s.%s %s", name, colName, reason),
				})
			}
		}
	}

	for name, expectedIdx := range expected.indexes {
		currentIdx, ok := current.indexes[name]
		if !ok {
			report.Differences = append(report.Differences,
				missingIndexDifference(ctx, db, name, expectedIdx))
			continue
		}
		if currentIdx.unique != expectedIdx.unique ||
			!slices.Equal(currentIdx.columns, expectedIdx.columns) {
			report.Differences = append(report.Differences, Difference{
				Kind: "index_mismatch", Table: expectedIdx.table, Index: name,
				Detail: fmt.Sprintf("the index %s covers different columns or loses its uniqueness", name),
			})
		}
	}

	sortDifferences(report.Differences)
	report.Verdict, report.Reason = verdict(report.Differences)
	return report
}

func verdict(differences []Difference) (Verdict, string) {
	if len(differences) == 0 {
		return VerdictCurrent, "the database is structurally identical to the v1 schema"
	}
	for _, d := range differences {
		if !d.Repairable {
			return VerdictIncompatible, "no safe in-place repair: " + d.Detail
		}
	}
	return VerdictMigratable, fmt.Sprintf(
		"%d differences, all of them with a safe in-place repair", len(differences))
}

// missingColumnDifference decides whether a missing column can be added. SQLite
// accepts neither ADD COLUMN of a primary key column, nor NOT NULL without a
// default value, nor a default value that is not constant.
func missingColumnDifference(table string, col column) Difference {
	d := Difference{
		Kind: "missing_column", Table: table, Column: col.name,
		Detail: fmt.Sprintf("the column %s.%s is missing", table, col.name),
	}
	switch {
	case col.pk > 0:
		d.Detail += ": it is a primary key and cannot be added in place"
	case col.defaultValue == "" && col.notNull:
		d.Detail += ": it is NOT NULL without a default value"
	case strings.Contains(col.defaultValue, "("):
		d.Detail += ": its default value is not constant"
	default:
		d.Repairable = true
		d.Detail = addColumnStatement(table, col)
	}
	return d
}

func addColumnStatement(table string, col column) string {
	stmt := fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, col.name, col.affinity)
	if col.notNull {
		stmt += " NOT NULL"
	}
	if col.defaultValue != "" {
		stmt += " DEFAULT " + col.defaultValue
	}
	return stmt
}

// missingIndexDifference checks, for a missing unique index, that creating it
// does not require deleting rows. Duplicate keys are blocking: they are never
// deduplicated silently.
func missingIndexDifference(ctx context.Context, db *DB, name string, idx index) Difference {
	d := Difference{
		Kind: "missing_index", Table: idx.table, Index: name, Repairable: true,
		Detail: "the index is missing: " + name,
	}
	if !idx.unique {
		return d
	}
	duplicates, err := countDuplicates(ctx, db, idx)
	if err != nil {
		d.Repairable = false
		d.Detail = fmt.Sprintf("cannot check whether %s admits the unique index %s: %v",
			idx.table, name, err)
		return d
	}
	if duplicates > 0 {
		d.Repairable = false
		d.Detail = fmt.Sprintf(
			"the unique index %s is missing and %s has %d duplicate keys: they are fixed by hand, never by deleting rows",
			name, idx.table, duplicates)
	}
	return d
}

func countDuplicates(ctx context.Context, db *DB, idx index) (int, error) {
	columns := strings.Join(idx.columns, ", ")
	notNull := make([]string, 0, len(idx.columns))
	for _, c := range idx.columns {
		notNull = append(notNull, c+" IS NOT NULL")
	}
	stmt := fmt.Sprintf(
		`SELECT COUNT(*) FROM (SELECT %s FROM %s WHERE %s GROUP BY %s HAVING COUNT(*) > 1)`,
		columns, idx.table, strings.Join(notNull, " AND "), columns)
	var n int
	if err := db.SQL().QueryRowContext(ctx, stmt).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

func differ(expected, current column) string {
	switch {
	case expected.affinity != current.affinity:
		return fmt.Sprintf("has affinity %s and the v1 schema declares it %s",
			current.affinity, expected.affinity)
	case expected.notNull != current.notNull:
		return fmt.Sprintf("has NOT NULL=%t and the v1 schema declares it %t",
			current.notNull, expected.notNull)
	case expected.defaultValue != current.defaultValue:
		return fmt.Sprintf("has default %q and the v1 schema declares %q",
			current.defaultValue, expected.defaultValue)
	case expected.pk != current.pk:
		return fmt.Sprintf("sits at position %d of the primary key and the v1 schema puts it at %d",
			current.pk, expected.pk)
	}
	return ""
}

func sortDifferences(differences []Difference) {
	slices.SortFunc(differences, func(a, b Difference) int {
		return strings.Compare(a.Detail, b.Detail)
	})
}

// --- reading the structure ---

// referenceStructure is the embedded schema's, read from the same engine that is
// going to run it: the reference is not a text, it is a database.
func referenceStructure(ctx context.Context) (structure, error) {
	reference, closeDB, err := referenceDB(ctx)
	if err != nil {
		return structure{}, err
	}
	defer closeDB()
	return readStructure(ctx, reference)
}

func referenceDB(ctx context.Context) (*sql.DB, func(), error) {
	handle, err := sql.Open("sqlite", "file::memory:")
	if err != nil {
		return nil, nil, fmt.Errorf("open the reference database: %w", err)
	}
	if _, err := handle.ExecContext(ctx, data.Schema); err != nil {
		handle.Close()
		return nil, nil, fmt.Errorf("apply the embedded schema to the reference: %w", err)
	}
	return handle, func() { handle.Close() }, nil
}

func readStructure(ctx context.Context, db *sql.DB) (structure, error) {
	e := structure{
		tables:  map[string]map[string]column{},
		views:   map[string]bool{},
		indexes: map[string]index{},
	}

	rows, err := db.QueryContext(ctx,
		`SELECT name, type FROM sqlite_master
		  WHERE type IN ('table', 'view') AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		return e, err
	}
	var tableNames []string
	for rows.Next() {
		var name, kind string
		if err := rows.Scan(&name, &kind); err != nil {
			rows.Close()
			return e, err
		}
		if kind == "view" {
			e.views[name] = true
			continue
		}
		tableNames = append(tableNames, name)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return e, err
	}

	for _, table := range tableNames {
		columns, err := readColumns(ctx, db, table)
		if err != nil {
			return e, err
		}
		e.tables[table] = columns
		indexes, err := readIndexes(ctx, db, table)
		if err != nil {
			return e, err
		}
		for name, idx := range indexes {
			e.indexes[name] = idx
		}
	}
	return e, nil
}

func readColumns(ctx context.Context, db *sql.DB, table string) (map[string]column, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT name, type, "notnull", ifnull(dflt_value, ''), pk FROM pragma_table_info(?)`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	columns := map[string]column{}
	for rows.Next() {
		var c column
		var kind string
		var notNull int
		if err := rows.Scan(&c.name, &kind, &notNull, &c.defaultValue, &c.pk); err != nil {
			return nil, err
		}
		c.affinity = affinity(kind)
		c.notNull = notNull != 0
		c.defaultValue = normalizeDefault(c.defaultValue)
		columns[c.name] = c
	}
	return columns, rows.Err()
}

func readIndexes(ctx context.Context, db *sql.DB, table string) (map[string]index, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT name, "unique", origin FROM pragma_index_list(?)`, table)
	if err != nil {
		return nil, err
	}
	type header struct {
		name   string
		unique bool
	}
	var headers []header
	for rows.Next() {
		var name, origin string
		var unique int
		if err := rows.Scan(&name, &unique, &origin); err != nil {
			rows.Close()
			return nil, err
		}
		// Only the indexes with a name of their own: the ones SQLite creates for
		// a primary key or a UNIQUE constraint already travel in table_info.
		if origin != "c" {
			continue
		}
		headers = append(headers, header{name: name, unique: unique != 0})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	indexes := map[string]index{}
	for _, c := range headers {
		columns, err := readIndexColumns(ctx, db, c.name)
		if err != nil {
			return nil, err
		}
		indexes[c.name] = index{name: c.name, table: table, unique: c.unique, columns: columns}
	}
	return indexes, nil
}

func readIndexColumns(ctx context.Context, db *sql.DB, name string) ([]string, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT ifnull(name, '') FROM pragma_index_xinfo(?) WHERE key = 1 ORDER BY seqno`, name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var columns []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		columns = append(columns, c)
	}
	return columns, rows.Err()
}

var whitespace = regexp.MustCompile(`\s+`)

func normalizeDefault(value string) string {
	return whitespace.ReplaceAllString(strings.TrimSpace(value), " ")
}

// affinity applies SQLite's affinity determination rules, which are what really
// governs how a column is stored. Declared type spelling is not structural
// identity.
func affinity(declaredType string) string {
	t := strings.ToUpper(declaredType)
	switch {
	case strings.Contains(t, "INT"):
		return "INTEGER"
	case strings.Contains(t, "CHAR"), strings.Contains(t, "CLOB"), strings.Contains(t, "TEXT"):
		return "TEXT"
	case t == "", strings.Contains(t, "BLOB"):
		return "BLOB"
	case strings.Contains(t, "REAL"), strings.Contains(t, "FLOA"), strings.Contains(t, "DOUB"):
		return "REAL"
	default:
		return "NUMERIC"
	}
}
