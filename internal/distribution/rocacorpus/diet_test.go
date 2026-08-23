package rocacorpus_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/distribution/rocacorpus"
	"github.com/thellmwhisperer/la-roca/internal/store/exactdedup"
	_ "modernc.org/sqlite"
)

func TestCompactRewritesAFatCorpusWithoutLosingCurrentRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "roca-corpus.db")
	if err := rocacorpus.ApplySchema(path); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	seedFatCorpus(t, db)
	if err := exactdedup.EnsureGuards(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	before := currentCounts(t, db)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	report, err := rocacorpus.Compact(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if report.Sessions != before.sessions || report.Exchanges != before.exchanges ||
		report.ThinkingBlocks != before.thinking || report.ToolUses != before.tools {
		t.Fatalf("current rows drifted: %+v vs %+v", report, before)
	}
	if report.BytesAfter <= 0 || report.BytesAfter > report.BytesBefore {
		t.Fatalf("compact did not shrink the database: before=%d after=%d",
			report.BytesBefore, report.BytesAfter)
	}
	if report.VacuumFreelist != 0 {
		t.Fatalf("compact report vacuum freelist = %d", report.VacuumFreelist)
	}

	db, err = sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	after := currentCounts(t, db)
	if after != before {
		t.Fatalf("current counts after reopen = %+v, want %+v", after, before)
	}
	assertNoTable(t, db, "exchange_versions_fts")
	assertNoTable(t, db, "thinking_block_versions_fts")
	assertNoTable(t, db, "session_versions_fts")
	assertNoColumn(t, db, "exchange_versions", "human_text")
	assertNoColumn(t, db, "exchange_versions", "agent_text")
	assertNoColumn(t, db, "thinking_block_versions", "full_text")
	assertNoColumn(t, db, "session_versions", "title")
	assertNoColumn(t, db, "session_versions", "metadata")
	assertNoColumn(t, db, "tool_use_versions", "tool_params_summary")
	assertNoColumn(t, db, "tool_use_versions", "error_message")
	assertCountQuery(t, db, `SELECT COUNT(*) FROM custody_memberships`, 0)
	assertCountQuery(t, db, `SELECT COUNT(*) FROM corpus_source_rows`, 0)
	assertCountQuery(t, db, `SELECT COUNT(*) FROM exchange_versions`, 0)
	assertCountQuery(t, db, `SELECT COUNT(*) FROM thinking_block_versions`, 0)
	var indexSQL string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type = 'index' AND name = 'idx_exchanges_exact_payload'`).
		Scan(&indexSQL); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.ToLower(indexSQL), "roca_payload_hash(") {
		t.Fatalf("payload index still stores content: %s", indexSQL)
	}
	var freelist int
	if err := db.QueryRow(`PRAGMA freelist_count`).Scan(&freelist); err != nil {
		t.Fatal(err)
	}
	if freelist != 0 {
		t.Fatalf("compact did not VACUUM: freelist_count=%d", freelist)
	}
}

func seedFatCorpus(t *testing.T, db *sql.DB) {
	t.Helper()
	statements := []string{
		`INSERT INTO sessions(session_id, source_agent, title, started_at, metadata)
			VALUES ('sess-1', 'claude', 'diet fixture', '2026-08-01T10:00:00Z', '{}')`,
		`INSERT INTO exchanges(session_id, exchange_number, human_text, agent_text)
			VALUES ('sess-1', 1, 'diet prompt', 'diet answer')`,
		`INSERT INTO thinking_blocks(session_id, exchange_number, position_in_session, word_count, full_text)
			VALUES ('sess-1', 1, 1, 2, 'diet thought')`,
		`INSERT INTO tool_uses(session_id, exchange_number, tool_name)
			VALUES ('sess-1', 1, 'Read')`,
		`DROP VIEW IF EXISTS exchange_version_memberships`,
		`DROP VIEW IF EXISTS thinking_block_version_memberships`,
		`DROP TABLE IF EXISTS exchange_versions_fts`,
		`DROP TABLE IF EXISTS thinking_block_versions_fts`,
		`DROP TABLE IF EXISTS exchange_versions`,
		`DROP TABLE IF EXISTS thinking_block_versions`,
		`CREATE TABLE exchange_versions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			version_digest TEXT NOT NULL UNIQUE,
			session_id TEXT NOT NULL,
			exchange_number INTEGER,
			is_after_compaction INTEGER,
			human_text TEXT,
			agent_text TEXT,
			human_timestamp TEXT,
			agent_timestamp TEXT,
			response_latency_ms INTEGER,
			model TEXT, provider TEXT,
			tokens_in INTEGER, tokens_out INTEGER, tokens_reasoning INTEGER, cost_usd REAL,
			observed_at TEXT)`,
		`CREATE TABLE thinking_block_versions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			version_digest TEXT NOT NULL UNIQUE,
			session_id TEXT NOT NULL,
			exchange_number INTEGER,
			position_in_session REAL,
			depth TEXT,
			caution_ratio REAL,
			word_count INTEGER,
			is_after_compaction INTEGER,
			full_text TEXT,
			observed_at TEXT)`,
		`INSERT INTO session_versions (version_digest, session_id, observed_at)
			VALUES ('` + strings.Repeat("e", 64) + `', 'sess-1', '2001-02-03T04:05:06Z')`,
		`INSERT INTO exchange_versions
			(version_digest, session_id, exchange_number, human_text, agent_text, observed_at)
			VALUES ('` + strings.Repeat("a", 64) + `', 'sess-1', 1, 'diet prompt', 'diet answer',
			        '2001-02-03T04:05:06Z')`,
		`INSERT INTO tool_use_versions
			(version_digest, session_id, exchange_number, tool_name, observed_at)
			VALUES ('` + strings.Repeat("f", 64) + `', 'sess-1', 1, 'Read',
			        '2001-02-03T04:05:06Z')`,
		`INSERT INTO thinking_block_versions
			(version_digest, session_id, exchange_number, word_count, full_text, observed_at)
			VALUES ('` + strings.Repeat("b", 64) + `', 'sess-1', 1, 2, 'diet thought',
			        '2001-02-03T04:05:06Z')`,
		`CREATE VIRTUAL TABLE exchange_versions_fts USING fts5(
			human_text, agent_text, content='exchange_versions', content_rowid='id')`,
		`INSERT INTO exchange_versions_fts(exchange_versions_fts) VALUES ('rebuild')`,
		`INSERT INTO custody_memberships
			(migration, source_database, source_table, source_key, destination_table,
			 destination_key, canonical_digest, batch_id)
			VALUES ('corpus-archive-exchanges', 'core', 'exchanges', '` + strings.Repeat("c", 64) + `',
			        'exchange_versions', '` + strings.Repeat("a", 64) + `',
			        '` + strings.Repeat("d", 64) + `', 'batch-1')`,
		`INSERT INTO corpus_source_rows
			(source_database, source_table, source_key, destination_table, version_digest,
			 source_row_id, session_id, exchange_number, occurrence_ordinal)
			VALUES ('core', 'exchanges', '` + strings.Repeat("c", 64) + `', 'exchange_versions',
			        '` + strings.Repeat("a", 64) + `', 1, 'sess-1', 1, 0)`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
}

func TestApplySchemaPreservesVersionObservedTimes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "roca-corpus.db")
	if err := rocacorpus.ApplySchema(path); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	seedFatCorpus(t, db)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := rocacorpus.ApplySchema(path); err != nil {
		t.Fatal(err)
	}
	db, err = sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, table := range []string{
		"session_versions", "exchange_versions", "tool_use_versions", "thinking_block_versions",
	} {
		var observedAt string
		if err := db.QueryRow("SELECT observed_at FROM " + table).Scan(&observedAt); err != nil {
			t.Fatal(err)
		}
		if observedAt != "2001-02-03T04:05:06Z" {
			t.Fatalf("%s observed_at = %q", table, observedAt)
		}
	}
}

type rowCounts struct {
	sessions, exchanges, thinking, tools int64
}

func currentCounts(t *testing.T, db *sql.DB) rowCounts {
	t.Helper()
	count := func(table string) int64 {
		t.Helper()
		var n int64
		if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	return rowCounts{
		sessions:  count("sessions"),
		exchanges: count("exchanges"),
		thinking:  count("thinking_blocks"),
		tools:     count("tool_uses"),
	}
}

func tableExists(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, name).
		Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n == 1
}

func assertNoTable(t *testing.T, db *sql.DB, name string) {
	t.Helper()
	if tableExists(t, db, name) {
		t.Fatalf("table %s still exists", name)
	}
}

func assertCountQuery(t *testing.T, db *sql.DB, query string, want int) {
	t.Helper()
	var got int
	if err := db.QueryRow(query).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("%s = %d, want %d", query, got, want)
	}
}

func assertNoColumn(t *testing.T, db *sql.DB, table, column string) {
	t.Helper()
	rows, err := db.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		if name == column {
			t.Fatalf("%s.%s still exists", table, column)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestCompactIsIdempotentOnAnAlreadySlimDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "roca-corpus.db")
	if err := rocacorpus.ApplySchema(path); err != nil {
		t.Fatal(err)
	}
	first, err := rocacorpus.Compact(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE compact_freelist_fixture(payload BLOB);
		INSERT INTO compact_freelist_fixture(payload) VALUES (zeroblob(1048576));
		DROP TABLE compact_freelist_fixture`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := rocacorpus.Compact(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if first.Sessions != second.Sessions || first.Exchanges != second.Exchanges {
		t.Fatalf("idempotent compact drifted current rows: %+v vs %+v", first, second)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() <= 0 {
		t.Fatal("compact left an empty database")
	}
	db, err = sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var freelist int
	if err := db.QueryRow(`PRAGMA freelist_count`).Scan(&freelist); err != nil {
		t.Fatal(err)
	}
	if freelist != 0 {
		t.Fatalf("idempotent compact left freelist_count=%d", freelist)
	}
}

func TestCompactRefusesToReportMissingHashGuards(t *testing.T) {
	path := filepath.Join(t.TempDir(), "roca-corpus.db")
	if err := rocacorpus.ApplySchema(path); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	seedFatCorpus(t, db)
	if _, err := db.Exec(`DROP INDEX idx_sessions_exact_payload;
		INSERT INTO sessions(session_id, source_agent, title, metadata)
		VALUES ('duplicate-a', 'claude', 'same', '{}'),
		       ('duplicate-b', 'claude', 'same', '{}')`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := rocacorpus.Compact(context.Background(), path); err == nil {
		t.Fatal("compact reported success without every hash guard")
	}
	db, err = sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	assertCountQuery(t, db, `SELECT COUNT(*) FROM custody_memberships`, 1)
	assertCountQuery(t, db, `SELECT COUNT(*) FROM corpus_source_rows`, 1)
	assertCountQuery(t, db, `SELECT COUNT(*) FROM exchange_versions`, 1)
	if !tableHasColumn(t, db, "exchange_versions", "human_text") {
		t.Fatal("compact refusal rewrote the archive")
	}
}

func tableHasColumn(t *testing.T, db *sql.DB, table, column string) bool {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?`,
		table, column).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count == 1
}
