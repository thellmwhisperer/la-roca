package exactdedup

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/thellmwhisperer/la-roca/internal/store/payloadhash"

	sqlite "modernc.org/sqlite"
)

type TableReport struct {
	Table                   string `json:"table"`
	Before                  int64  `json:"before"`
	ObservedExactGroups     int    `json:"observed_exact_groups"`
	ObservedGroupedRows     int    `json:"observed_grouped_rows"`
	ObservedLosers          int    `json:"observed_losers"`
	ObservedAmbiguousGroups int    `json:"observed_ambiguous_groups"`
	ObservedAmbiguousRows   int    `json:"observed_ambiguous_rows"`
	ExactGroups             int    `json:"apply_exact_groups"`
	GroupedRows             int    `json:"apply_grouped_rows"`
	Losers                  int    `json:"apply_losers"`
	After                   int64  `json:"after"`
	AmbiguousGroups         int    `json:"post_remap_ambiguous_groups"`
	AmbiguousRows           int    `json:"post_remap_ambiguous_rows"`
}

type DatabaseReport struct {
	Path           string        `json:"path"`
	ManifestSHA256 string        `json:"manifest_sha256"`
	Tables         []TableReport `json:"tables"`
	FileSHA256     string        `json:"file_sha256,omitempty"`
	Bytes          int64         `json:"bytes,omitempty"`
	SchemaVersion  int           `json:"schema_version,omitempty"`
}

type mapping struct {
	table, oldID, canonicalID, payload, payloadSHA string
}

type tableSpec struct {
	name, id, remaps, fts string
	payload, identity     []string
}

var baseSpecs = []tableSpec{
	{name: "sessions", id: "session_id", remaps: "session_id_remaps", fts: "sessions_fts",
		payload:  []string{"source_agent", "project", "started_at", "ended_at", "duration_minutes", "title", "metadata", "source_surface"},
		identity: []string{"source_agent", "title", "started_at"}},
	{name: "exchanges", id: "id", remaps: "exchange_id_remaps", fts: "exchanges_fts",
		payload:  []string{"session_id", "exchange_number", "is_after_compaction", "human_text", "agent_text", "human_timestamp", "agent_timestamp", "response_latency_ms", "model", "provider", "tokens_in", "tokens_out", "tokens_reasoning", "cost_usd"},
		identity: []string{"session_id", "exchange_number"}},
	{name: "thinking_blocks", id: "id", remaps: "thinking_block_id_remaps", fts: "thinking_fts",
		payload:  []string{"session_id", "exchange_number", "position_in_session", "depth", "caution_ratio", "word_count", "is_after_compaction", "full_text"},
		identity: []string{"session_id", "exchange_number", "position_in_session"}},
}

func Inspect(ctx context.Context, path string) (DatabaseReport, error) {
	db, err := open(path, true)
	if err != nil {
		return DatabaseReport{}, err
	}
	defer db.Close()
	return inspect(ctx, db, path)
}

func inspect(ctx context.Context, db querier, path string) (DatabaseReport, error) {
	specs, err := specs(ctx, db)
	if err != nil {
		return DatabaseReport{}, err
	}
	sessionMap, err := mappingsFor(ctx, db, findSpec(specs, "sessions"), nil)
	if err != nil {
		return DatabaseReport{}, err
	}
	canonicalSessions := make(map[string]string, len(sessionMap))
	for _, item := range sessionMap {
		canonicalSessions[item.oldID] = item.canonicalID
	}

	var all []mapping
	report := DatabaseReport{Path: path}
	for _, spec := range specs {
		observedMaps, err := mappingsFor(ctx, db, spec, nil)
		if err != nil {
			return DatabaseReport{}, err
		}
		observed, err := summarize(ctx, db, spec, observedMaps, nil)
		if err != nil {
			return DatabaseReport{}, err
		}
		maps, err := mappingsFor(ctx, db, spec, canonicalSessions)
		if err != nil {
			return DatabaseReport{}, err
		}
		all = append(all, maps...)
		row, err := summarize(ctx, db, spec, maps, canonicalSessions)
		if err != nil {
			return DatabaseReport{}, err
		}
		row.ObservedExactGroups = observed.ExactGroups
		row.ObservedGroupedRows = observed.GroupedRows
		row.ObservedLosers = observed.Losers
		row.ObservedAmbiguousGroups = observed.AmbiguousGroups
		row.ObservedAmbiguousRows = observed.AmbiguousRows
		report.Tables = append(report.Tables, row)
	}
	report.ManifestSHA256 = digestMappings(all)
	return report, nil
}

func Apply(ctx context.Context, path, expectedManifest, runID, backupPath string) (DatabaseReport, error) {
	if strings.TrimSpace(expectedManifest) == "" {
		return DatabaseReport{}, fmt.Errorf("apply requires the exact dry-run manifest SHA-256")
	}
	if strings.TrimSpace(backupPath) == "" {
		return DatabaseReport{}, fmt.Errorf("apply requires a verified VACUUM INTO backup")
	}
	targetAbs, _ := filepath.Abs(path)
	backupAbs, _ := filepath.Abs(backupPath)
	if targetAbs == backupAbs {
		return DatabaseReport{}, fmt.Errorf("the backup and apply target are the same database")
	}
	backup, err := Inspect(ctx, backupPath)
	if err != nil {
		return DatabaseReport{}, fmt.Errorf("open the pre-apply backup read-only: %w", err)
	}
	if backup.ManifestSHA256 != expectedManifest {
		return DatabaseReport{}, fmt.Errorf("backup manifest is %s, expected %s", backup.ManifestSHA256, expectedManifest)
	}
	db, err := open(path, false)
	if err != nil {
		return DatabaseReport{}, err
	}
	defer db.Close()

	before, err := inspect(ctx, db, path)
	if err != nil {
		return DatabaseReport{}, err
	}
	if before.ManifestSHA256 != expectedManifest {
		return DatabaseReport{}, fmt.Errorf("dedup drift gate: database manifest is %s, expected %s",
			before.ManifestSHA256, expectedManifest)
	}
	if runID == "" {
		runID = "dedup-" + time.Now().UTC().Format("20060102T150405.000000000Z")
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return DatabaseReport{}, fmt.Errorf("begin exact dedup transaction: %w", err)
	}
	defer tx.Rollback()
	// The first comparison gives a useful refusal before taking the write lock.
	// Repeat it after BEGIN IMMEDIATE so no writer can change the certified set
	// between the drift gate and the deletes.
	locked, err := inspect(ctx, tx, path)
	if err != nil {
		return DatabaseReport{}, err
	}
	if locked.ManifestSHA256 != expectedManifest {
		return DatabaseReport{}, fmt.Errorf("dedup drift gate under write lock: database manifest is %s, expected %s",
			locked.ManifestSHA256, expectedManifest)
	}
	if err := createAuditTables(ctx, tx); err != nil {
		return DatabaseReport{}, err
	}
	// The retired identity-only index cannot coexist with the ratified law:
	// two observations may share a source identity while carrying different
	// payloads, and those rows are evidence to report, never deletion authority.
	if _, err := tx.ExecContext(ctx, `DROP INDEX IF EXISTS idx_exchanges_session_number`); err != nil {
		return DatabaseReport{}, fmt.Errorf("retire the exchange identity-only guard: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO dedup_runs
		(run_id, manifest_sha256, started_at) VALUES (?, ?, datetime('now'))`, runID, expectedManifest); err != nil {
		return DatabaseReport{}, fmt.Errorf("record dedup run: %w", err)
	}

	specs, err := specs(ctx, tx)
	if err != nil {
		return DatabaseReport{}, err
	}
	sessionSpec := findSpec(specs, "sessions")
	sessionMaps, err := mappingsFor(ctx, tx, sessionSpec, nil)
	if err != nil {
		return DatabaseReport{}, err
	}
	canonicalSessions := map[string]string{}
	for _, item := range sessionMaps {
		canonicalSessions[item.oldID] = item.canonicalID
	}
	if err := persistMappings(ctx, tx, sessionSpec, sessionMaps, runID); err != nil {
		return DatabaseReport{}, err
	}
	if err := rewriteSessionReferences(ctx, tx, sessionMaps); err != nil {
		return DatabaseReport{}, err
	}
	if err := deleteMappings(ctx, tx, sessionSpec, sessionMaps); err != nil {
		return DatabaseReport{}, err
	}

	for _, spec := range specs {
		if spec.name == "sessions" {
			continue
		}
		maps, mapErr := mappingsFor(ctx, tx, spec, nil)
		if mapErr != nil {
			return DatabaseReport{}, mapErr
		}
		if spec.name == "memories" {
			if err := rewriteSupersedes(ctx, tx, maps); err != nil {
				return DatabaseReport{}, err
			}
		}
		if err := persistMappings(ctx, tx, spec, maps, runID); err != nil {
			return DatabaseReport{}, err
		}
		if err := deleteMappings(ctx, tx, spec, maps); err != nil {
			return DatabaseReport{}, err
		}
	}
	if err := rebuildFTS(ctx, tx, specs); err != nil {
		return DatabaseReport{}, err
	}
	if err := EnsureGuards(ctx, tx); err != nil {
		return DatabaseReport{}, err
	}
	if err := verify(ctx, tx, specs); err != nil {
		return DatabaseReport{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE dedup_runs SET committed_at = datetime('now') WHERE run_id = ?`, runID); err != nil {
		return DatabaseReport{}, fmt.Errorf("finish dedup run: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return DatabaseReport{}, fmt.Errorf("commit exact dedup transaction: %w", err)
	}
	return Inspect(ctx, path)
}

func Backup(ctx context.Context, source, destination string) (DatabaseReport, error) {
	if strings.TrimSpace(destination) == "" {
		return DatabaseReport{}, fmt.Errorf("a backup destination is required")
	}
	if _, err := os.Stat(destination); err == nil {
		return DatabaseReport{}, fmt.Errorf("backup destination already exists: %s", destination)
	} else if !os.IsNotExist(err) {
		return DatabaseReport{}, err
	}
	sourceAbs, _ := filepath.Abs(source)
	destinationAbs, _ := filepath.Abs(destination)
	if sourceAbs == destinationAbs {
		return DatabaseReport{}, fmt.Errorf("the backup and source are the same database")
	}
	db, err := open(source, true)
	if err != nil {
		return DatabaseReport{}, err
	}
	if _, err := db.ExecContext(ctx, "VACUUM INTO ?", destinationAbs); err != nil {
		db.Close()
		return DatabaseReport{}, fmt.Errorf("VACUUM INTO backup: %w", err)
	}
	if err := db.Close(); err != nil {
		return DatabaseReport{}, err
	}
	report, err := Inspect(ctx, destinationAbs)
	if err != nil {
		return DatabaseReport{}, err
	}
	file, err := os.Open(destinationAbs)
	if err != nil {
		return DatabaseReport{}, err
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		file.Close()
		return DatabaseReport{}, err
	}
	if err := file.Close(); err != nil {
		return DatabaseReport{}, err
	}
	info, err := os.Stat(destinationAbs)
	if err != nil {
		return DatabaseReport{}, err
	}
	db, err = open(destinationAbs, true)
	if err != nil {
		return DatabaseReport{}, err
	}
	defer db.Close()
	if err := db.QueryRowContext(ctx, "PRAGMA schema_version").Scan(&report.SchemaVersion); err != nil {
		return DatabaseReport{}, err
	}
	report.FileSHA256 = hex.EncodeToString(hash.Sum(nil))
	report.Bytes = info.Size()
	return report, nil
}

type querier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type executor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

type queryExecutor interface {
	querier
	executor
}

func specs(ctx context.Context, db querier) ([]tableSpec, error) {
	var available []tableSpec
	for _, spec := range baseSpecs {
		present, err := tableExists(ctx, db, spec.name)
		if err != nil {
			return nil, err
		}
		if present {
			ordered, err := orderedColumns(ctx, db, spec.name)
			if err != nil {
				return nil, err
			}
			spec.payload = payloadColumns(ordered, spec.id)
			available = append(available, spec)
		}
	}
	if present, err := tableExists(ctx, db, "memories"); err != nil {
		return nil, err
	} else if present {
		ordered, err := orderedColumns(ctx, db, "memories")
		if err != nil {
			return nil, err
		}
		payload := payloadColumns(ordered, "id", "created_at")
		available = append(available, tableSpec{name: "memories", id: "id", remaps: "memory_id_remaps",
			fts: "memories_fts", payload: payload, identity: []string{"layer", "project", "status", "content"}})
	}
	return available, nil
}

func payloadColumns(columns []string, excluded ...string) []string {
	skip := map[string]bool{}
	for _, name := range excluded {
		skip[name] = true
	}
	result := make([]string, 0, len(columns))
	for _, name := range columns {
		if !skip[name] {
			result = append(result, name)
		}
	}
	return result
}

func findSpec(specs []tableSpec, name string) tableSpec {
	for _, spec := range specs {
		if spec.name == name {
			return spec
		}
	}
	return tableSpec{}
}

func mappingsFor(ctx context.Context, db querier, spec tableSpec, sessions map[string]string) ([]mapping, error) {
	if spec.name == "" {
		return nil, nil
	}
	if spec.name == "memories" {
		if err := validateMemoryWinners(ctx, db, spec, sessions); err != nil {
			return nil, err
		}
	}
	payload := keyExpression(spec.payload, sessions)
	order := spec.id
	query := fmt.Sprintf(`WITH keyed AS (
		SELECT %s AS record_id, %s AS payload_key FROM %s
	), ranked AS (
		SELECT record_id, payload_key,
		       MIN(record_id) OVER (PARTITION BY payload_key) AS canonical_id,
		       COUNT(*) OVER (PARTITION BY payload_key) AS copies
		FROM keyed
	)
	SELECT CAST(record_id AS TEXT), CAST(canonical_id AS TEXT), payload_key
	FROM ranked WHERE copies > 1 AND record_id <> canonical_id
	ORDER BY canonical_id, record_id`, order, payload, spec.name)
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("map exact %s duplicates: %w", spec.name, err)
	}
	defer rows.Close()
	var result []mapping
	for rows.Next() {
		var item mapping
		item.table = spec.name
		if err := rows.Scan(&item.oldID, &item.canonicalID, &item.payload); err != nil {
			return nil, err
		}
		hash := sha256.Sum256([]byte(item.payload))
		item.payloadSHA = hex.EncodeToString(hash[:])
		result = append(result, item)
	}
	return result, rows.Err()
}

func validateMemoryWinners(ctx context.Context, db querier, spec tableSpec, sessions map[string]string) error {
	var invalid int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM memories WHERE NOT json_valid(metadata)`).Scan(&invalid); err != nil {
		return err
	}
	if invalid != 0 {
		return fmt.Errorf("memory exact dedup refuses %d rows with invalid metadata JSON", invalid)
	}
	payload := keyExpression(spec.payload, sessions)
	query := fmt.Sprintf(`WITH keyed AS (
		SELECT id, created_at, %s AS payload_key FROM memories
	), groups AS (
		SELECT payload_key, MIN(id) AS winner_id, MIN(created_at) AS earliest_created_at
		FROM keyed GROUP BY payload_key HAVING COUNT(*) > 1
	)
	SELECT COUNT(*) FROM groups JOIN keyed ON keyed.id = groups.winner_id
	WHERE keyed.created_at <> groups.earliest_created_at`, payload)
	var lateWinners int
	if err := db.QueryRowContext(ctx, query).Scan(&lateWinners); err != nil {
		return err
	}
	if lateWinners != 0 {
		return fmt.Errorf("memory exact dedup refuses %d groups whose minimum ID is not the earliest row", lateWinners)
	}
	return nil
}

func summarize(ctx context.Context, db querier, spec tableSpec, maps []mapping,
	sessions map[string]string) (TableReport, error) {
	report := TableReport{Table: spec.name, Losers: len(maps)}
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+spec.name).Scan(&report.Before); err != nil {
		return report, err
	}
	winners := map[string]bool{}
	for _, item := range maps {
		winners[item.canonicalID] = true
	}
	report.ExactGroups = len(winners)
	report.GroupedRows = report.ExactGroups + report.Losers
	report.After = report.Before - int64(report.Losers)
	payload, identity := keyExpression(spec.payload, sessions), keyExpression(spec.identity, sessions)
	query := fmt.Sprintf(`SELECT COUNT(*), COALESCE(SUM(copies), 0) FROM (
		SELECT %s identity_key, COUNT(*) copies, COUNT(DISTINCT %s) variants
		FROM %s GROUP BY identity_key HAVING variants > 1)`, identity, payload, spec.name)
	if err := db.QueryRowContext(ctx, query).Scan(&report.AmbiguousGroups, &report.AmbiguousRows); err != nil {
		return report, fmt.Errorf("report ambiguous %s identities: %w", spec.name, err)
	}
	return report, nil
}

func keyExpression(names []string, sessions map[string]string) string {
	parts := make([]string, len(names))
	for i, name := range names {
		parts[i] = name
		if name == "session_id" || name == "source_session" {
			if len(sessions) > 0 {
				cases := make([]string, 0, len(sessions))
				keys := make([]string, 0, len(sessions))
				for old := range sessions {
					keys = append(keys, old)
				}
				sort.Strings(keys)
				for _, old := range keys {
					cases = append(cases, fmt.Sprintf("WHEN %s THEN %s", quote(old), quote(sessions[old])))
				}
				parts[i] = fmt.Sprintf("CASE %s %s ELSE %s END", name, strings.Join(cases, " "), name)
			}
		}
	}
	return "json_array(" + strings.Join(parts, ",") + ")"
}

func digestMappings(items []mapping) string {
	sort.Slice(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if a.table != b.table {
			return a.table < b.table
		}
		if a.canonicalID != b.canonicalID {
			return a.canonicalID < b.canonicalID
		}
		return a.oldID < b.oldID
	})
	hash := sha256.New()
	for _, item := range items {
		fmt.Fprintf(hash, "%s\x00%s\x00%s\x00%s\n", item.table, item.oldID, item.canonicalID, item.payloadSHA)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func createAuditTables(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS dedup_runs (
		 run_id TEXT PRIMARY KEY, manifest_sha256 TEXT NOT NULL, started_at TEXT NOT NULL,
		 committed_at TEXT)`); err != nil {
		return fmt.Errorf("create dedup audit schema: %w", err)
	}
	tables := []struct {
		owner, statement string
	}{
		{"memories", `CREATE TABLE IF NOT EXISTS memory_id_remaps (
		 old_id INTEGER PRIMARY KEY, canonical_id INTEGER NOT NULL REFERENCES memories(id),
		 dedup_run_id TEXT NOT NULL REFERENCES dedup_runs(run_id), payload_sha256 TEXT NOT NULL,
		 old_created_at TEXT NOT NULL, mapped_at TEXT NOT NULL DEFAULT (datetime('now')))`},
		{"sessions", `CREATE TABLE IF NOT EXISTS session_id_remaps (
		 old_id TEXT PRIMARY KEY, canonical_id TEXT NOT NULL REFERENCES sessions(session_id),
		 dedup_run_id TEXT NOT NULL REFERENCES dedup_runs(run_id), payload_sha256 TEXT NOT NULL,
		 mapped_at TEXT NOT NULL DEFAULT (datetime('now')))`},
		{"exchanges", `CREATE TABLE IF NOT EXISTS exchange_id_remaps (
		 old_id INTEGER PRIMARY KEY, canonical_id INTEGER NOT NULL REFERENCES exchanges(id),
		 dedup_run_id TEXT NOT NULL REFERENCES dedup_runs(run_id), payload_sha256 TEXT NOT NULL,
		 mapped_at TEXT NOT NULL DEFAULT (datetime('now')))`},
		{"thinking_blocks", `CREATE TABLE IF NOT EXISTS thinking_block_id_remaps (
		 old_id INTEGER PRIMARY KEY, canonical_id INTEGER NOT NULL REFERENCES thinking_blocks(id),
		 dedup_run_id TEXT NOT NULL REFERENCES dedup_runs(run_id), payload_sha256 TEXT NOT NULL,
		 mapped_at TEXT NOT NULL DEFAULT (datetime('now')))`},
	}
	for _, table := range tables {
		present, err := tableExists(ctx, tx, table.owner)
		if err != nil {
			return err
		}
		if !present {
			continue
		}
		if _, err := tx.ExecContext(ctx, table.statement); err != nil {
			return fmt.Errorf("create dedup audit schema: %w", err)
		}
	}
	return nil
}

func persistMappings(ctx context.Context, tx *sql.Tx, spec tableSpec, maps []mapping, runID string) error {
	for _, item := range maps {
		var statement string
		var args []any
		if spec.name == "memories" {
			statement = `INSERT INTO memory_id_remaps
				(old_id, canonical_id, dedup_run_id, payload_sha256, old_created_at)
				SELECT ?, ?, ?, ?, created_at FROM memories WHERE id = ?`
			args = []any{item.oldID, item.canonicalID, runID, item.payloadSHA, item.oldID}
		} else {
			statement = fmt.Sprintf(`INSERT INTO %s
				(old_id, canonical_id, dedup_run_id, payload_sha256) VALUES (?, ?, ?, ?)`, spec.remaps)
			args = []any{item.oldID, item.canonicalID, runID, item.payloadSHA}
		}
		if _, err := tx.ExecContext(ctx, statement, args...); err != nil {
			return fmt.Errorf("record %s remap %s -> %s: %w", spec.name, item.oldID, item.canonicalID, err)
		}
	}
	return nil
}

func rewriteSessionReferences(ctx context.Context, tx *sql.Tx, maps []mapping) error {
	for _, item := range maps {
		for _, target := range []struct{ table, column string }{
			{"exchanges", "session_id"}, {"thinking_blocks", "session_id"},
			{"tool_uses", "session_id"}, {"memories", "source_session"},
		} {
			present, err := tableExists(ctx, tx, target.table)
			if err != nil {
				return err
			}
			if !present {
				continue
			}
			cols, err := columns(ctx, tx, target.table)
			if err != nil {
				return err
			}
			if !cols[target.column] {
				continue
			}
			statement := fmt.Sprintf("UPDATE %s SET %s = ? WHERE %s = ?", target.table, target.column, target.column)
			if _, err := tx.ExecContext(ctx, statement, item.canonicalID, item.oldID); err != nil {
				return fmt.Errorf("remap %s.%s %s: %w", target.table, target.column, item.oldID, err)
			}
		}
	}
	return nil
}

func rewriteSupersedes(ctx context.Context, tx *sql.Tx, maps []mapping) error {
	for _, item := range maps {
		if _, err := tx.ExecContext(ctx, `UPDATE memories SET supersedes = ? WHERE supersedes = ?`,
			item.canonicalID, item.oldID); err != nil {
			return fmt.Errorf("rewrite memory supersedes %s: %w", item.oldID, err)
		}
	}
	return nil
}

func deleteMappings(ctx context.Context, tx *sql.Tx, spec tableSpec, maps []mapping) error {
	for _, item := range maps {
		result, err := tx.ExecContext(ctx, fmt.Sprintf("DELETE FROM %s WHERE %s = ?", spec.name, spec.id), item.oldID)
		if err != nil {
			return fmt.Errorf("delete exact %s duplicate %s: %w", spec.name, item.oldID, err)
		}
		if affected, _ := result.RowsAffected(); affected != 1 {
			return fmt.Errorf("delete exact %s duplicate %s affected %d rows", spec.name, item.oldID, affected)
		}
	}
	return nil
}

func rebuildFTS(ctx context.Context, tx *sql.Tx, specs []tableSpec) error {
	for _, spec := range specs {
		present, err := tableExists(ctx, tx, spec.fts)
		if err != nil {
			return err
		}
		if !present {
			continue
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("INSERT INTO %s(%s) VALUES ('rebuild')", spec.fts, spec.fts)); err != nil {
			return fmt.Errorf("rebuild %s: %w", spec.fts, err)
		}
	}
	return nil
}

func EnsureGuards(ctx context.Context, db queryExecutor) error {
	return EnsureTableGuards(ctx, db)
}

func GuardsInstalled(ctx context.Context, db interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}) (bool, error) {
	specs, err := specs(ctx, db)
	if err != nil {
		return false, err
	}
	for _, spec := range specs {
		name := "idx_" + spec.name + "_exact_payload"
		want := fmt.Sprintf("CREATE UNIQUE INDEX %s ON %s(%s)",
			name, spec.name, guardKeyExpression(spec.payload))
		var installed sql.NullString
		err := db.QueryRowContext(ctx,
			`SELECT sql FROM sqlite_master WHERE type = 'index' AND name = ?`, name).Scan(&installed)
		if err == sql.ErrNoRows {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if !installed.Valid || normalizeDDL(installed.String) != normalizeDDL(want) {
			return false, nil
		}
	}
	return true, nil
}

func EnsureTableGuards(ctx context.Context, db queryExecutor, only ...string) error {
	specs, err := specs(ctx, db)
	if err != nil {
		return err
	}
	wanted := map[string]bool{}
	for _, name := range only {
		wanted[name] = true
	}
	for _, spec := range specs {
		if len(wanted) > 0 && !wanted[spec.name] {
			continue
		}
		name := "idx_" + spec.name + "_exact_payload"
		statement := fmt.Sprintf("CREATE UNIQUE INDEX %s ON %s(%s)",
			name, spec.name, guardKeyExpression(spec.payload))
		var installed sql.NullString
		err := db.QueryRowContext(ctx,
			`SELECT sql FROM sqlite_master WHERE type = 'index' AND name = ?`, name).Scan(&installed)
		if err != nil && err != sql.ErrNoRows {
			return err
		}
		if err == nil && normalizeDDL(installed.String) == normalizeDDL(statement) {
			continue
		}
		if err == nil {
			if _, err := db.ExecContext(ctx, "DROP INDEX "+name); err != nil {
				return fmt.Errorf("refresh exact-payload guard %s: %w", name, err)
			}
		}
		if _, err := db.ExecContext(ctx, statement); err != nil {
			var sqliteErr *sqlite.Error
			if errors.As(err, &sqliteErr) && sqliteErr.Code() == 2067 {
				return fmt.Errorf("create exact-payload guard %s: exact duplicates remain; run the exact dedup dry-run and apply first: %w", name, err)
			}
			return fmt.Errorf("create exact-payload guard %s: %w", name, err)
		}
	}
	return nil
}

func guardKeyExpression(names []string) string {
	parts := make([]string, 0, len(names)*2)
	for _, name := range names {
		parts = append(parts, "typeof("+name+")",
			"CASE WHEN typeof("+name+") = 'text' THEN CAST("+name+" AS BLOB) ELSE "+name+" END")
	}
	return payloadhash.SQLFunc + "(" + strings.Join(parts, ",") + ")"
}

func normalizeDDL(statement string) string {
	return strings.ToLower(strings.Join(strings.Fields(statement), " "))
}

func verify(ctx context.Context, db queryExecutor, specs []tableSpec) error {
	for _, spec := range specs {
		query := fmt.Sprintf(`SELECT COUNT(*) FROM (SELECT %s FROM %s GROUP BY %s HAVING COUNT(*) > 1)`,
			keyExpression(spec.payload, nil), spec.name, keyExpression(spec.payload, nil))
		var duplicates int
		if err := db.QueryRowContext(ctx, query).Scan(&duplicates); err != nil {
			return err
		}
		if duplicates != 0 {
			return fmt.Errorf("acceptance: %s still has %d exact duplicate groups", spec.name, duplicates)
		}
		if spec.fts != "" {
			present, err := tableExists(ctx, db, spec.fts)
			if err != nil {
				return err
			}
			if present {
				var tableRows, ftsRows int64
				if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+spec.name).Scan(&tableRows); err != nil {
					return err
				}
				if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+spec.fts).Scan(&ftsRows); err != nil {
					return err
				}
				if tableRows != ftsRows {
					return fmt.Errorf("acceptance: %s/FTS parity is %d/%d", spec.name, tableRows, ftsRows)
				}
			}
		}
		if spec.remaps != "" {
			var chained int
			query := fmt.Sprintf(`SELECT COUNT(*) FROM %s current
				JOIN %s next ON CAST(next.old_id AS TEXT) = CAST(current.canonical_id AS TEXT)`,
				spec.remaps, spec.remaps)
			if err := db.QueryRowContext(ctx, query).Scan(&chained); err != nil {
				return err
			}
			if chained != 0 {
				return fmt.Errorf("acceptance: %s contains %d chained remaps", spec.remaps, chained)
			}
		}
	}
	if memories := findSpec(specs, "memories"); memories.name != "" {
		var orphaned int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM memories m
			WHERE m.supersedes IS NOT NULL
			AND NOT EXISTS (SELECT 1 FROM memories parent WHERE parent.id = m.supersedes)`).Scan(&orphaned); err != nil {
			return err
		}
		if orphaned != 0 {
			return fmt.Errorf("acceptance: memories has %d orphaned supersedes references", orphaned)
		}
	}
	foreignRows, err := db.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return fmt.Errorf("acceptance: foreign_key_check: %w", err)
	}
	if foreignRows.Next() {
		foreignRows.Close()
		return fmt.Errorf("acceptance: foreign_key_check reported a violation")
	}
	if err := foreignRows.Close(); err != nil {
		return err
	}
	integrityRows, err := db.QueryContext(ctx, "PRAGMA integrity_check")
	if err != nil {
		return fmt.Errorf("acceptance: integrity_check: %w", err)
	}
	defer integrityRows.Close()
	var messages []string
	for integrityRows.Next() {
		var message string
		if err := integrityRows.Scan(&message); err != nil {
			return err
		}
		messages = append(messages, message)
	}
	if err := integrityRows.Err(); err != nil {
		return err
	}
	if len(messages) != 1 || messages[0] != "ok" {
		return fmt.Errorf("acceptance: integrity_check = %q", messages)
	}
	return nil
}

func tableExists(ctx context.Context, db querier, name string) (bool, error) {
	var count int
	err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type IN ('table','view') AND name = ?`, name).Scan(&count)
	return count == 1, err
}

func columns(ctx context.Context, db querier, table string) (map[string]bool, error) {
	ordered, err := orderedColumns(ctx, db, table)
	if err != nil {
		return nil, err
	}
	result := map[string]bool{}
	for _, name := range ordered {
		result[name] = true
	}
	return result, nil
}

func orderedColumns(ctx context.Context, db querier, table string) ([]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT name FROM pragma_table_info(?) ORDER BY cid`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		result = append(result, name)
	}
	return result, rows.Err()
}

func open(path string, readOnly bool) (*sql.DB, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	values := url.Values{"_pragma": {"busy_timeout(15000)", "foreign_keys(ON)"}}
	if readOnly {
		values.Set("mode", "ro")
	} else {
		values.Set("_txlock", "immediate")
	}
	dsn := url.URL{Scheme: "file", Path: filepath.ToSlash(abs), RawQuery: values.Encode()}
	db, err := sql.Open("sqlite", dsn.String())
	if err != nil {
		return nil, err
	}
	if err := db.PingContext(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func quote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}
