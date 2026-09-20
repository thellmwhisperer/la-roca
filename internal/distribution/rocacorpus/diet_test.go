package rocacorpus_test

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/distribution/bundledplugin"
	"github.com/thellmwhisperer/la-roca/internal/distribution/rocacorpus"
	"github.com/thellmwhisperer/la-roca/internal/store/exactdedup"
	_ "modernc.org/sqlite"
)

func TestCompactRefusesSchemaAdvanceWithoutMutatingCorpus(t *testing.T) {
	t.Setenv(bundledplugin.EnvAllowHomeMigrate, "")
	db, path := openCorpusDB(t)
	seedFatCorpus(t, db)
	if _, err := db.Exec(`UPDATE plugin_schema SET schema_version = ?`, rocacorpus.SchemaVersion-1); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	_, err = rocacorpus.Compact(context.Background(), path)
	want := fmt.Sprintf("refusing to migrate roca-corpus from schema %d to %d", rocacorpus.SchemaVersion-1, rocacorpus.SchemaVersion)
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("compact error = %v, want %q", err, want)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("compact mutated the persisted corpus database before refusing migration")
	}
}

func TestCompactRewritesAFatCorpusWithoutLosingCurrentRows(t *testing.T) {
	db, path := openCorpusDB(t)
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
	assertCurrentRows(t, report, before.sessions, before.exchanges, before.thinking, before.tools)
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
	db, path := openCorpusDB(t)
	seedFatCorpus(t, db)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db = reapplySchemaAndReopen(t, path)
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

func TestApplySchemaDropsVersionFTSBeforeAddingObservedTime(t *testing.T) {
	db, path := openCorpusDB(t)
	statements := []string{
		`DROP VIEW IF EXISTS exchange_version_memberships`,
		`DROP TABLE IF EXISTS exchange_versions_fts`,
		`DROP TABLE exchange_versions`,
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
			model TEXT,
			provider TEXT,
			tokens_in INTEGER,
			tokens_out INTEGER,
			tokens_reasoning INTEGER,
			cost_usd REAL)`,
		`INSERT INTO exchange_versions
			(version_digest, session_id, exchange_number, human_text, agent_text)
		 VALUES ('` + strings.Repeat("a", 64) + `', 'sess-1', 1, 'prompt', 'answer')`,
		`CREATE VIRTUAL TABLE exchange_versions_fts USING fts5(
			human_text, agent_text, content='exchange_versions', content_rowid='id')`,
		`INSERT INTO exchange_versions_fts(exchange_versions_fts) VALUES ('rebuild')`,
	}
	execStatements(t, db, statements)

	db = reapplySchemaAndReopen(t, path)
	defer db.Close()
	assertNoTable(t, db, "exchange_versions_fts")
	assertNoColumn(t, db, "exchange_versions", "human_text")
	var observedAt string
	if err := db.QueryRow(`SELECT observed_at FROM exchange_versions`).Scan(&observedAt); err != nil {
		t.Fatal(err)
	}
	if observedAt == "" {
		t.Fatal("version observed time was not backfilled")
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

func indexExists(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?`, name).
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
		t.Fatalf("%s: %v", query, err)
	}
	if got != want {
		t.Fatalf("%s = %d, want %d", query, got, want)
	}
}

func assertNoColumn(t *testing.T, db *sql.DB, table, column string) {
	t.Helper()
	if tableHasColumn(t, db, table, column) {
		t.Fatalf("%s.%s still exists", table, column)
	}
}

func TestCompactRefusesANonCorpusDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "core.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	statements := []string{
		`CREATE TABLE sessions (
  session_id    TEXT PRIMARY KEY,
  source_agent  TEXT DEFAULT 'claude-code',
  project       TEXT,
  started_at    TEXT,
  ended_at      TEXT,
  duration_minutes INTEGER,
  title         TEXT,
  metadata      TEXT DEFAULT '{}',
  source_surface TEXT
)`,
		`CREATE TABLE exchanges (
  id                    INTEGER PRIMARY KEY AUTOINCREMENT,
  session_id            TEXT,
  exchange_number       INTEGER,
  is_after_compaction   INTEGER DEFAULT 0,
  human_text            TEXT,
  agent_text            TEXT,
  human_timestamp       TEXT,
  agent_timestamp       TEXT,
  response_latency_ms   INTEGER,
  model                 TEXT,
  provider              TEXT,
  tokens_in             INTEGER,
  tokens_out            INTEGER,
  tokens_reasoning      INTEGER,
  cost_usd              REAL
)`,
		`CREATE TABLE tool_uses (
  id                    INTEGER PRIMARY KEY AUTOINCREMENT,
  session_id            TEXT,
  exchange_number       INTEGER,
  tool_name             TEXT,
  tool_params_summary   TEXT,
  had_error             INTEGER DEFAULT 0,
  error_message         TEXT,
  initiative_type       TEXT
)`,
		`CREATE TABLE thinking_blocks (
  id                    INTEGER PRIMARY KEY AUTOINCREMENT,
  session_id            TEXT,
  exchange_number       INTEGER,
  position_in_session   REAL,
  depth                 TEXT,
  caution_ratio         REAL,
  word_count            INTEGER,
  is_after_compaction   INTEGER DEFAULT 0,
  full_text             TEXT
)`,
	}
	execStatements(t, db, statements)

	if _, err := rocacorpus.Compact(context.Background(), path); err == nil {
		t.Fatal("compact accepted a database with no corpus identity")
	}

	db, err = sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var owned int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'plugin_schema'`).
		Scan(&owned); err != nil {
		t.Fatal(err)
	}
	if owned != 0 {
		t.Fatal("compact mutated a non-corpus database")
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
	db, path := openCorpusDB(t)
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
	db, err := sql.Open("sqlite", path)
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

func TestApplySchemaBackfillsMachineOnUpgrade(t *testing.T) {
	var beforeFTS string
	path := prepareCorpusUpgradeBeforeBump(t, func(db *sql.DB) {
		installHistoricalHarvestTriggers(t, db)
		beforeFTS = dumpHarvestFTSIndex(t, db)
	},
		`INSERT INTO sessions(session_id, source_agent, title, project) VALUES ('historical', 'claude', 'fixture title', 'demo')`,
		`INSERT INTO exchanges(session_id, exchange_number, human_text, agent_text) VALUES ('historical', 1, 'question', 'answer')`,
		`INSERT INTO thinking_blocks(session_id, exchange_number, position_in_session, full_text) VALUES ('historical', 1, 1, 'thought')`,
		`INSERT INTO tool_uses(session_id, exchange_number, tool_name) VALUES ('historical', 1, 'Read')`,
	)

	db := reapplySchemaAndReopen(t, path)
	defer db.Close()
	machine, err := os.Hostname()
	if err != nil || strings.TrimSpace(machine) == "" {
		machine = "local"
	} else {
		machine = strings.TrimSpace(machine)
	}
	for _, table := range []string{"sessions", "exchanges", "thinking_blocks", "tool_uses"} {
		var got string
		if err := db.QueryRow(`SELECT machine FROM ` + table + ` WHERE session_id = 'historical'`).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != machine {
			t.Fatalf("%s.machine = %q, want %q", table, got, machine)
		}
	}
	if afterFTS := dumpHarvestFTSIndex(t, db); beforeFTS != afterFTS {
		t.Fatalf("FTS index bytes changed during machine backfill\nbefore:\n%s\nafter:\n%s", beforeFTS, afterFTS)
	}
	for _, table := range []string{"sessions", "exchanges", "thinking_blocks"} {
		if _, err := db.Exec("UPDATE " + table + " SET machine = 'remote'"); err != nil {
			t.Fatal(err)
		}
	}
	if afterFTS := dumpHarvestFTSIndex(t, db); beforeFTS != afterFTS {
		t.Fatal("machine-only updates changed the FTS index bytes after upgrade")
	}
	for _, test := range []struct{ table, column, index, old string }{
		{"sessions", "title", "sessions_fts", "fixture"},
		{"exchanges", "human_text", "exchanges_fts", "question"},
		{"thinking_blocks", "full_text", "thinking_fts", "thought"},
	} {
		if _, err := db.Exec("UPDATE " + test.table + " SET " + test.column + " = 'replacement'"); err != nil {
			t.Fatal(err)
		}
		assertCountQuery(t, db, "SELECT COUNT(*) FROM "+test.index+" WHERE "+test.index+" MATCH 'replacement'", 1)
		assertCountQuery(t, db, "SELECT COUNT(*) FROM "+test.index+" WHERE "+test.index+" MATCH '"+test.old+"'", 0)
	}
}

func TestApplySchemaProvenanceFailureRollsBackMachineAndTriggers(t *testing.T) {
	t.Setenv(bundledplugin.EnvAllowHomeMigrate, "1")
	db, path := openCorpusDB(t)
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO sessions(session_id, source_agent, title)
		VALUES ('historical', 'claude', 'original');
		CREATE TRIGGER reject_provenance BEFORE UPDATE OF source_surface ON sessions
		BEGIN SELECT RAISE(ABORT, 'fixture provenance failure'); END;`); err != nil {
		t.Fatal(err)
	}
	installHistoricalHarvestTriggers(t, db)
	if _, err := db.Exec(`UPDATE plugin_schema SET schema_version = ?`, rocacorpus.SchemaVersion-1); err != nil {
		t.Fatal(err)
	}
	beforeFTS := dumpHarvestFTSIndex(t, db)
	if err := rocacorpus.ApplySchema(path); err == nil || !strings.Contains(err.Error(), "fixture provenance failure") {
		t.Fatalf("migration error = %v, want fixture provenance failure", err)
	}
	assertCountQuery(t, db, "SELECT COUNT(*) FROM sessions WHERE machine IS NULL", 1)
	if beforeFTS != dumpHarvestFTSIndex(t, db) {
		t.Fatal("failed migration changed the FTS index bytes")
	}
	if _, err := db.Exec(`UPDATE sessions SET title = 'replacement'`); err != nil {
		t.Fatal(err)
	}
	assertCountQuery(t, db, "SELECT COUNT(*) FROM sessions_fts WHERE sessions_fts MATCH 'replacement'", 1)
	assertCountQuery(t, db, "SELECT COUNT(*) FROM sessions_fts WHERE sessions_fts MATCH 'original'", 0)
}

func installHistoricalHarvestTriggers(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`
		DROP TRIGGER sessions_au;
		CREATE TRIGGER sessions_au AFTER UPDATE ON sessions BEGIN
		  INSERT INTO sessions_fts(sessions_fts, rowid, title, project) VALUES ('delete', old.rowid, old.title, old.project);
		  INSERT INTO sessions_fts(rowid, title, project) VALUES (new.rowid, new.title, new.project);
		END;
		DROP TRIGGER exchanges_au;
		CREATE TRIGGER exchanges_au AFTER UPDATE ON exchanges BEGIN
		  INSERT INTO exchanges_fts(exchanges_fts, rowid, human_text, agent_text) VALUES ('delete', old.id, old.human_text, old.agent_text);
		  INSERT INTO exchanges_fts(rowid, human_text, agent_text) VALUES (new.id, new.human_text, new.agent_text);
		END;
		DROP TRIGGER thinking_au;
		CREATE TRIGGER thinking_au AFTER UPDATE ON thinking_blocks BEGIN
		  INSERT INTO thinking_fts(thinking_fts, rowid, full_text) VALUES ('delete', old.id, old.full_text);
		  INSERT INTO thinking_fts(rowid, full_text) VALUES (new.id, new.full_text);
		END;`); err != nil {
		t.Fatal(err)
	}
}

func dumpHarvestFTSIndex(t *testing.T, db *sql.DB) string {
	t.Helper()
	var dump strings.Builder
	for _, index := range []string{"sessions_fts", "exchanges_fts", "thinking_fts"} {
		for _, shadow := range []struct{ suffix, columns, order string }{
			{"data", "quote(id) || ':' || quote(block)", "id"},
			{"idx", "quote(segid) || ':' || quote(term) || ':' || quote(pgno)", "segid, term"},
			{"docsize", "quote(id) || ':' || quote(sz)", "id"},
			{"config", "quote(k) || ':' || quote(v)", "k"},
		} {
			table := index + "_" + shadow.suffix
			rows, err := db.Query("SELECT " + shadow.columns + " FROM " + table + " ORDER BY " + shadow.order)
			if err != nil {
				t.Fatal(err)
			}
			for rows.Next() {
				var row string
				if err := rows.Scan(&row); err != nil {
					rows.Close()
					t.Fatal(err)
				}
				fmt.Fprintf(&dump, "%s:%s\n", table, row)
			}
			if err := rows.Err(); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			rows.Close()
		}
	}
	return dump.String()
}

func TestApplySchemaCollapsesThinkingCopiesThatOnlyDifferByPosition(t *testing.T) {
	path := prepareCorpusUpgrade(t,
		`INSERT INTO sessions(session_id, source_agent) VALUES ('open-session', 'claude')`,
		`INSERT INTO exchanges(session_id, exchange_number, human_text, agent_text)
		   VALUES ('open-session', 1, 'first', 'answer')`,
		`DROP INDEX IF EXISTS idx_thinking_blocks_identity`,
		`INSERT INTO thinking_blocks(session_id, exchange_number, position_in_session, word_count, full_text)
		   VALUES ('open-session', 1, 1.0, 3, 'keep this thought')`,
		`INSERT INTO thinking_blocks(session_id, exchange_number, position_in_session, word_count, full_text)
		   VALUES ('open-session', 1, 0.5, 3, 'keep this thought')`,
		`INSERT INTO thinking_blocks(session_id, exchange_number, position_in_session, word_count, full_text)
		   VALUES ('open-session', 1, 0.5, 2, 'a different thought')`,
	)

	db := reapplySchemaAndReopen(t, path)
	defer db.Close()
	var copies int
	if err := db.QueryRow(`SELECT COUNT(*) FROM thinking_blocks
		WHERE session_id = 'open-session' AND exchange_number = 1 AND full_text = 'keep this thought'`).
		Scan(&copies); err != nil {
		t.Fatal(err)
	}
	if copies != 1 {
		t.Fatalf("thinking copies = %d, want 1 after identity collapse", copies)
	}
	var position float64
	if err := db.QueryRow(`SELECT position_in_session FROM thinking_blocks
		WHERE session_id = 'open-session' AND exchange_number = 1 AND full_text = 'keep this thought'`).Scan(&position); err != nil {
		t.Fatal(err)
	}
	if position != 0.5 {
		t.Fatalf("thinking position = %v, want newest copy at 0.5", position)
	}
	var distinct int
	if err := db.QueryRow(`SELECT COUNT(*) FROM thinking_blocks
		WHERE session_id = 'open-session' AND exchange_number = 1`).Scan(&distinct); err != nil {
		t.Fatal(err)
	}
	if distinct != 2 {
		t.Fatalf("thinking rows = %d, want both distinct texts kept", distinct)
	}
	var groups int
	if err := db.QueryRow(`SELECT COUNT(*) FROM (
		SELECT session_id, exchange_number,
			roca_payload_hash(
				typeof(full_text),
				CASE WHEN typeof(full_text) = 'text' THEN CAST(full_text AS BLOB) ELSE full_text END
			) AS text_hash,
			COUNT(*) AS n
		FROM thinking_blocks GROUP BY 1, 2, 3 HAVING n > 1)`).Scan(&groups); err != nil {
		t.Fatal(err)
	}
	if groups != 0 {
		t.Fatalf("duplicate thinking groups = %d, want 0", groups)
	}
	if !indexExists(t, db, "idx_thinking_blocks_identity") {
		t.Fatal("thinking identity index missing after collapse")
	}
}

func TestExactDedupReconcilesThinkingIdentityBeforeSessionRemap(t *testing.T) {
	for _, exchange := range []string{"1", "NULL"} {
		t.Run(exchange, func(t *testing.T) {
			path := prepareCorpusUpgrade(t,
				`DROP INDEX idx_sessions_exact_payload`,
				`CREATE INDEX idx_sessions_exact_payload ON sessions(source_agent, title)`,
				`DROP INDEX idx_thinking_blocks_identity`,
				`INSERT INTO sessions(session_id, source_agent, title) VALUES
				 ('a', 'claude', 'same'), ('b', 'claude', 'same'), ('c', 'claude', 'same')`,
				fmt.Sprintf(`INSERT INTO thinking_blocks(id, session_id, exchange_number, position_in_session, full_text) VALUES
				 (1, 'a', %[1]s, 1.0, 'shared thought'),
				 (2, 'b', %[1]s, 0.5, 'shared thought'),
				 (3, 'c', %[1]s, 0.25, 'shared thought'),
				 (4, 'b', %[1]s, 0.75, 'distinct thought')`, exchange),
			)
			db := reapplySchemaAndReopen(t, path)
			defer db.Close()
			assertCountQuery(t, db, `SELECT COUNT(*) FROM thinking_blocks`, 4)

			ctx := context.Background()
			before, err := exactdedup.Inspect(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			for _, table := range before.Tables {
				if table.Table == "thinking_blocks" && (table.Losers != 2 || table.After != 2) {
					t.Fatalf("thinking remap preview = %+v", table)
				}
			}
			backup := filepath.Join(t.TempDir(), "before.db")
			if _, err := exactdedup.Backup(ctx, path, backup); err != nil {
				t.Fatal(err)
			}
			if _, err := exactdedup.Apply(ctx, path, before.ManifestSHA256, "thinking-remap", backup); err != nil {
				t.Fatal(err)
			}
			assertCountQuery(t, db, `SELECT COUNT(*) FROM sessions`, 1)
			assertCountQuery(t, db, `SELECT COUNT(*) FROM thinking_blocks`, 2)
			assertCountQuery(t, db, `SELECT COUNT(*) FROM thinking_blocks
				WHERE id = 3 AND session_id = 'a' AND position_in_session = 0.25 AND full_text = 'shared thought'`, 1)
			assertCountQuery(t, db, `SELECT COUNT(*) FROM thinking_blocks
				WHERE id = 4 AND session_id = 'a' AND full_text = 'distinct thought'`, 1)
			assertCountQuery(t, db, `SELECT COUNT(*) FROM thinking_block_id_remaps
				WHERE old_id IN (1, 2) AND canonical_id = 3`, 2)
			assertCountQuery(t, db, `SELECT COUNT(*) FROM thinking_fts WHERE thinking_fts MATCH 'thought'`, 2)
			if _, err := db.Exec(fmt.Sprintf(`INSERT INTO thinking_blocks
				(session_id, exchange_number, position_in_session, full_text)
				VALUES ('a', %s, 0.125, 'shared thought')`, exchange)); err == nil {
				t.Fatal("thinking identity guard accepted a duplicate after session remapping")
			}
			if _, err := exactdedup.Apply(ctx, path, before.ManifestSHA256, "stale-remap", backup); err == nil {
				t.Fatal("apply accepted a stale manifest after session remapping")
			}
			if _, err := db.Exec(fmt.Sprintf(`DROP INDEX idx_sessions_exact_payload;
				INSERT INTO sessions(session_id, source_agent, project, started_at, ended_at,
				 duration_minutes, title, metadata, source_surface, machine)
				SELECT 'd', source_agent, project, started_at, ended_at,
				 duration_minutes, title, metadata, source_surface, machine FROM sessions WHERE session_id = 'a';
				INSERT INTO thinking_blocks(id, session_id, exchange_number, position_in_session, full_text)
				VALUES (5, 'd', %s, 0.125, 'shared thought')`, exchange)); err != nil {
				t.Fatal(err)
			}
			nextBackup := filepath.Join(t.TempDir(), "next.db")
			next, err := exactdedup.Backup(ctx, path, nextBackup)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := exactdedup.Apply(ctx, path, next.ManifestSHA256, "next-remap", nextBackup); err != nil {
				t.Fatal(err)
			}
			assertCountQuery(t, db, `SELECT COUNT(*) FROM thinking_block_id_remaps
				WHERE old_id IN (1, 2, 3) AND canonical_id = 5`, 3)
			assertCountQuery(t, db, `SELECT COUNT(*) FROM thinking_blocks WHERE id = 5 AND session_id = 'a'`, 1)
		})
	}
}

func prepareCorpusUpgrade(t *testing.T, statements ...string) string {
	return prepareCorpusUpgradeBeforeBump(t, nil, statements...)
}

func prepareCorpusUpgradeBeforeBump(t *testing.T, beforeBump func(*sql.DB), statements ...string) string {
	t.Helper()
	t.Setenv(bundledplugin.EnvAllowHomeMigrate, "1")
	db, path := openCorpusDB(t)
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			db.Close()
			t.Fatal(err)
		}
	}
	if beforeBump != nil {
		beforeBump(db)
	}
	if _, err := db.Exec(`UPDATE plugin_schema SET schema_version = ?`, rocacorpus.SchemaVersion-1); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

// openCorpusDB applies the corpus schema to a fresh database and opens it,
// returning the handle and the file path for tests that reopen or compact it.
func openCorpusDB(t *testing.T) (*sql.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "roca-corpus.db")
	if err := rocacorpus.ApplySchema(path); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	return db, path
}

// reapplySchemaAndReopen re-runs the schema over an already-created corpus and
// opens the result, the shared tail of the observed-time upgrade tests.
func reapplySchemaAndReopen(t *testing.T, path string) *sql.DB {
	t.Helper()
	if err := rocacorpus.ApplySchema(path); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

// execStatements runs every fixture statement and closes the database.
func execStatements(t *testing.T, db *sql.DB, statements []string) {
	t.Helper()
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			db.Close()
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

// assertCurrentRows checks a compact report carried every current row through.
func assertCurrentRows(t *testing.T, report rocacorpus.CompactReport, sessions, exchanges, thinking, tools int64) {
	t.Helper()
	if report.Sessions != sessions || report.Exchanges != exchanges ||
		report.ThinkingBlocks != thinking || report.ToolUses != tools {
		t.Fatalf("current rows drifted: %+v", report)
	}
}
