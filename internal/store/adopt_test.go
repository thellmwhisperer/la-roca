package store_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/store"
)

func TestADatabaseCreatedByRocaIsAdoptedUntouched(t *testing.T) {
	db := openFresh(t)
	ctx := context.Background()
	if err := store.ApplySchema(ctx, db); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}

	report, err := store.Inspect(ctx, db)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if report.Verdict != store.VerdictCurrent {
		t.Fatalf("verdict = %q (%s), want current", report.Verdict, report.Reason)
	}
	if len(report.Differences) != 0 {
		t.Errorf("differences = %v, want none", report.Differences)
	}
}

// Adoption cannot depend on the text of the DDL. Whitespace, comments, column
// order and constraint order are formatting noise.
func TestDDLFormattingNoiseNeverBlocks(t *testing.T) {
	db := openFresh(t)
	ctx := context.Background()
	if err := store.ApplySchema(ctx, db); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}
	// The same table, written by another hand: columns in another order, a
	// comment inside, different spacing and the DEFAULT before the NOT NULL.
	recreate(t, db, "ingest_file_state", `
		CREATE TABLE ingest_file_state (
		  -- estado incremental, reescrito a mano
		  metadata       TEXT     DEFAULT '{}',
		  path           TEXT     NOT NULL PRIMARY KEY,
		  last_error     TEXT,
		  fingerprint    TEXT,
		  source_kind    TEXT NOT NULL,
		  last_synced_at TEXT DEFAULT (datetime('now')),
		  project        TEXT,
		  source_agent   TEXT
		)`)
	execute(t, db, `CREATE INDEX idx_ingest_state_project ON ingest_file_state (project)`)
	execute(t, db, `CREATE
	                 INDEX idx_ingest_state_source_agent ON ingest_file_state (source_agent)`)

	report, err := store.Inspect(ctx, db)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if report.Verdict != store.VerdictCurrent {
		t.Fatalf("verdict = %q (%s), want current: %v",
			report.Verdict, report.Reason, report.Differences)
	}
}

func TestOrphanTablesAreReportedAndDoNotBlock(t *testing.T) {
	db := openFresh(t)
	ctx := context.Background()
	if err := store.ApplySchema(ctx, db); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}
	execute(t, db, `CREATE TABLE garden_notes (id INTEGER PRIMARY KEY, note TEXT)`)
	execute(t, db, `INSERT INTO garden_notes (note) VALUES ('from a removed feature')`)
	execute(t, db, `CREATE TABLE messages (id INTEGER PRIMARY KEY)`)

	report, err := store.Inspect(ctx, db)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if report.Verdict != store.VerdictCurrent {
		t.Fatalf("verdict = %q, want current: orphans do not block", report.Verdict)
	}
	if !contains(report.Orphans, "garden_notes") || !contains(report.Orphans, "messages") {
		t.Errorf("orphans = %v, want garden_notes and messages", report.Orphans)
	}

	adoption, err := store.Adopt(ctx, db, t.TempDir())
	if err != nil {
		t.Fatalf("Adopt: %v", err)
	}
	if !adoption.Adopted {
		t.Error("Adopted = false, want true")
	}
	// The repair boundary never drops a data table.
	var score string
	if err := db.SQL().QueryRow("SELECT note FROM garden_notes").Scan(&score); err != nil {
		t.Fatalf("the orphan disappeared after adopting: %v", err)
	}
}

func TestAMissingTableIsMigratableAndRepairedAfterBackup(t *testing.T) {
	db := openFresh(t)
	ctx := context.Background()
	if err := store.ApplySchema(ctx, db); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}
	seedIdentity(t, db)
	execute(t, db, `DROP INDEX idx_memories_layer`)

	report, err := store.Inspect(ctx, db)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if report.Verdict != store.VerdictMigratable {
		t.Fatalf("verdict = %q (%s), want migratable", report.Verdict, report.Reason)
	}

	backups := t.TempDir()
	adoption, err := store.Adopt(ctx, db, backups)
	if err != nil {
		t.Fatalf("Adopt: %v", err)
	}
	if adoption.BackupPath == "" {
		t.Fatal("there is no backup: every repair goes behind a verified backup")
	}
	if _, err := os.Stat(adoption.BackupPath); err != nil {
		t.Fatalf("the backup does not exist: %v", err)
	}
	if len(adoption.Repairs) == 0 {
		t.Error("Repairs empty, want the itemized list of what was repaired")
	}

	after, err := store.Inspect(ctx, db)
	if err != nil {
		t.Fatalf("Inspect tras reparar: %v", err)
	}
	if after.Verdict != store.VerdictCurrent {
		t.Errorf("after repairing the verdict is %q, want current: %v",
			after.Verdict, after.Differences)
	}
}

func TestAuthorshipColumnsAdoptWithoutGuessingHistoricalRows(t *testing.T) {
	db, ctx := schemaReadyDatabase(t)
	execute(t, db, "ALTER TABLE memories DROP COLUMN source_model")
	execute(t, db, "ALTER TABLE memories DROP COLUMN source_surface")
	seedIdentity(t, db)

	if _, err := store.Adopt(ctx, db, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	var agent, model, surface sql.NullString
	if err := db.SQL().QueryRow(
		"SELECT source_agent, source_model, source_surface FROM memories WHERE content = 'anchor of adoption'",
	).Scan(&agent, &model, &surface); err != nil {
		t.Fatal(err)
	}
	if agent.Valid || model.Valid || surface.Valid {
		t.Errorf("historical authorship was guessed: agent=%v model=%v surface=%v", agent, model, surface)
	}
}

func TestIngestSurfaceAdoptsAndSanitizesOnlyDerivableHistoricalRows(t *testing.T) {
	db, ctx := schemaReadyDatabase(t)
	execute(t, db, "ALTER TABLE sessions DROP COLUMN source_surface")
	execute(t, db, `INSERT INTO sessions (session_id, source_agent) VALUES
		('grok-session', 'grok'), ('unknown-session', 'synthetic-unknown')`)
	execute(t, db, `INSERT INTO exchanges (session_id, exchange_number, model) VALUES
		('grok-session', 1, 'grok-4.6-build'),
		('grok-session', 2, 'grok-4.5-build'),
		('unknown-session', 1, 'grok-4.6-build')`)

	if _, err := store.Adopt(ctx, db, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	rows, err := db.SQL().Query(`
		SELECT s.session_id, COALESCE(s.source_surface, ''), e.model, COALESCE(e.provider, '')
		FROM sessions s JOIN exchanges e USING (session_id)
		ORDER BY s.session_id, e.exchange_number`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	want := [][4]string{
		{"grok-session", "Grok Build", "grok-4.6", ""},
		{"grok-session", "Grok Build", "grok-4.5", ""},
		{"unknown-session", "", "grok-4.6-build", ""},
	}
	var got [][4]string
	for rows.Next() {
		var row [4]string
		if err := rows.Scan(&row[0], &row[1], &row[2], &row[3]); err != nil {
			t.Fatal(err)
		}
		got = append(got, row)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sanitized provenance = %#v, want %#v", got, want)
	}
}

func schemaReadyDatabase(t *testing.T) (*store.DB, context.Context) {
	t.Helper()
	db := openFresh(t)
	ctx := context.Background()
	if err := store.ApplySchema(ctx, db); err != nil {
		t.Fatal(err)
	}
	return db, ctx
}

func TestAColumnWithAnotherShapeIsIncompatibleAndUntouched(t *testing.T) {
	db := openFresh(t)
	ctx := context.Background()
	if err := store.ApplySchema(ctx, db); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}
	// content goes from TEXT NOT NULL to optional INTEGER: there is no safe repair.
	recreate(t, db, "memories", `
		CREATE TABLE memories (
		  id              INTEGER PRIMARY KEY AUTOINCREMENT,
		  layer           TEXT NOT NULL,
		  content         INTEGER,
		  metadata        TEXT DEFAULT '{}',
		  origin          TEXT NOT NULL,
		  source_agent    TEXT,
		  source_session  TEXT,
		  source_sequence INTEGER,
		  project         TEXT,
		  status          TEXT DEFAULT 'active',
		  supersedes      INTEGER,
		  created_at      TEXT DEFAULT (datetime('now'))
		)`)

	report, err := store.Inspect(ctx, db)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if report.Verdict != store.VerdictIncompatible {
		t.Fatalf("verdict = %q, want incompatible", report.Verdict)
	}
	if !namesTheDifference(report, "memories", "content") {
		t.Errorf("the diagnosis does not name memories.content: %v", report.Differences)
	}

	if _, err := store.Adopt(ctx, db, t.TempDir()); err == nil {
		t.Fatal("Adopt accepted an incompatible database")
	}
	var kind string
	err = db.SQL().QueryRow(
		`SELECT type FROM pragma_table_info('memories') WHERE name = 'content'`).Scan(&kind)
	if err != nil {
		t.Fatalf("table_info: %v", err)
	}
	if kind != "INTEGER" {
		t.Errorf("Adopt touched an incompatible database: content is %q", kind)
	}
}

func TestADatabaseWithoutIdentityTablesIsForeign(t *testing.T) {
	db := openFresh(t)
	ctx := context.Background()
	execute(t, db, `CREATE TABLE cosas (id INTEGER PRIMARY KEY)`)

	report, err := store.Inspect(ctx, db)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if report.Verdict != store.VerdictForeign {
		t.Fatalf("verdict = %q, want foreign", report.Verdict)
	}
	if !strings.Contains(report.Reason, "sessions") &&
		!strings.Contains(report.Reason, "memories") &&
		!strings.Contains(report.Reason, "exchanges") {
		t.Errorf("the reason does not name the identity tables: %q", report.Reason)
	}
}

// An empty database is not foreign: it is a new database, and creating its
// schema is init's normal path.
func TestAnEmptyDatabaseIsCreatedInsteadOfRejected(t *testing.T) {
	db := openFresh(t)
	ctx := context.Background()

	report, err := store.Inspect(ctx, db)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if report.Verdict != store.VerdictMigratable {
		t.Fatalf("verdict = %q, want migratable for an empty database", report.Verdict)
	}
	if _, err := store.Adopt(ctx, db, t.TempDir()); err != nil {
		t.Fatalf("Adopt: %v", err)
	}
	if got := len(tableNames(t, db.SQL())); got != 7 {
		t.Errorf("tables = %d after adopting an empty database, want 7", got)
	}
}

// Duplicate keys under a missing unique index are blocking: they are never
// deduplicated silently to make the constraint fit.
func TestAMissingUniqueIndexWithDuplicatesBlocks(t *testing.T) {
	db := openFresh(t)
	ctx := context.Background()
	if err := store.ApplySchema(ctx, db); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}
	execute(t, db, `DROP INDEX idx_exchanges_session_number`)
	execute(t, db, `INSERT INTO sessions (session_id) VALUES ('s1')`)
	execute(t, db, `INSERT INTO exchanges (session_id, exchange_number) VALUES ('s1', 1)`)
	execute(t, db, `INSERT INTO exchanges (session_id, exchange_number) VALUES ('s1', 1)`)

	report, err := store.Inspect(ctx, db)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if report.Verdict != store.VerdictIncompatible {
		t.Fatalf("verdict = %q, want incompatible", report.Verdict)
	}
	if _, err := store.Adopt(ctx, db, t.TempDir()); err == nil {
		t.Fatal("Adopt accepted a database with duplicates under a missing unique index")
	}
	var n int
	if err := db.SQL().QueryRow("SELECT COUNT(*) FROM exchanges").Scan(&n); err != nil {
		t.Fatalf("COUNT exchanges: %v", err)
	}
	if n != 2 {
		t.Errorf("exchanges rows = %d, want 2: nothing is deduplicated silently", n)
	}
}

func TestBackupCreatesAVerifiedCopy(t *testing.T) {
	db := openFresh(t)
	ctx := context.Background()
	if err := store.ApplySchema(ctx, db); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}
	seedIdentity(t, db)

	dest := t.TempDir()
	path, err := store.Backup(ctx, db, dest)
	if err != nil {
		t.Fatalf("Backup: %v", err)
	}
	if filepath.Dir(path) != dest {
		t.Errorf("the backup is at %q, want it at %q", filepath.Dir(path), dest)
	}

	copia, err := store.Open(path)
	if err != nil {
		t.Fatalf("open the copy: %v", err)
	}
	defer copia.Close()
	if got := countMemories(t, copia.SQL()); got != 1 {
		t.Errorf("memories in the copy = %d, want 1", got)
	}
}

// CopyDatabase creates a verified copy of the source database at the
// destination. It uses VACUUM INTO (the SQLite online backup approach) so a live
// WAL database copies consistently without risking an inconsistent copy that cp
// would produce.
func TestCopyDatabaseCreatesAVerifiedCopyAndLeavesTheSourceIntact(t *testing.T) {
	t.Run("verified copy with source intact", func(t *testing.T) {
		db := openFresh(t)
		ctx := context.Background()
		if err := store.ApplySchema(ctx, db); err != nil {
			t.Fatalf("ApplySchema: %v", err)
		}
		seedIdentity(t, db)
		if err := os.Chmod(db.Path(), 0o640); err != nil {
			t.Fatal(err)
		}

		dest := filepath.Join(t.TempDir(), "roca.db")
		if err := store.CopyDatabase(ctx, db.Path(), dest); err != nil {
			t.Fatalf("CopyDatabase: %v", err)
		}

		copyDB, err := store.Open(dest)
		if err != nil {
			t.Fatalf("open the copy: %v", err)
		}
		defer copyDB.Close()
		if got := countMemories(t, copyDB.SQL()); got != 1 {
			t.Errorf("memories in the copy = %d, want 1", got)
		}
		if got := len(tableNames(t, copyDB.SQL())); got != 7 {
			t.Errorf("tables in the copy = %d, want 7", got)
		}
		if got := countMemories(t, db.SQL()); got != 1 {
			t.Errorf("memories in the source after Copy = %d, want 1", got)
		}
		if info, err := os.Stat(db.Path()); err != nil {
			t.Errorf("stat source after copy: %v", err)
		} else if info.Mode().Perm() != 0o640 {
			t.Errorf("source permissions changed to %v", info.Mode().Perm())
		}
	})

	t.Run("refuses to overwrite an existing file", func(t *testing.T) {
		db := openFresh(t)
		ctx := context.Background()
		if err := store.ApplySchema(ctx, db); err != nil {
			t.Fatalf("ApplySchema: %v", err)
		}

		dest := filepath.Join(t.TempDir(), "roca.db")
		if err := os.WriteFile(dest, []byte("SQLite format 3\x00"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := store.CopyDatabase(ctx, db.Path(), dest); err == nil {
			t.Fatal("CopyDatabase overwrote an existing file")
		}
	})

	t.Run("escapes URI delimiters in the source path", func(t *testing.T) {
		source := filepath.Join(t.TempDir(), "source?#%.db")
		db, err := store.Open(source)
		if err != nil {
			t.Fatalf("open source: %v", err)
		}
		defer db.Close()
		ctx := context.Background()
		if err := store.ApplySchema(ctx, db); err != nil {
			t.Fatalf("ApplySchema: %v", err)
		}
		seedIdentity(t, db)
		dest := filepath.Join(t.TempDir(), "copy.db")
		if err := store.CopyDatabase(ctx, source, dest); err != nil {
			t.Fatalf("CopyDatabase: %v", err)
		}
		copyDB, err := store.Open(dest)
		if err != nil {
			t.Fatalf("open copy: %v", err)
		}
		defer copyDB.Close()
		if got := countMemories(t, copyDB.SQL()); got != 1 {
			t.Errorf("memories in the copy = %d, want 1", got)
		}
	})
}

func TestCopyDatabaseRebuildsSessionFTSAfterRowidRenumbering(t *testing.T) {
	db := openFresh(t)
	ctx := context.Background()
	if err := store.ApplySchema(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureSearchSchema(ctx, db); err != nil {
		t.Fatal(err)
	}
	execute(t, db, `INSERT INTO sessions (session_id, title) VALUES
		('s1', 'first'), ('s2', 'discarded'), ('s3', 'target session')`)
	execute(t, db, `DELETE FROM sessions WHERE session_id = 's2'`)
	dest := filepath.Join(t.TempDir(), "copy.db")
	if err := store.CopyDatabase(ctx, db.Path(), dest); err != nil {
		t.Fatal(err)
	}
	copyDB, err := store.Open(dest)
	if err != nil {
		t.Fatal(err)
	}
	defer copyDB.Close()
	var sessionID string
	err = copyDB.SQL().QueryRow(`SELECT s.session_id FROM sessions_fts
		JOIN sessions AS s ON s.rowid = sessions_fts.rowid
		WHERE sessions_fts MATCH 'target'`).Scan(&sessionID)
	if err != nil || sessionID != "s3" {
		t.Fatalf("session search mapped to %q: %v", sessionID, err)
	}
}

// --- helpers ---

func execute(t *testing.T, db *store.DB, stmt string) {
	t.Helper()
	if err := db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(stmt)
		return err
	}); err != nil {
		t.Fatalf("ejecutar %q: %v", stmt, err)
	}
}

func recreate(t *testing.T, db *store.DB, table, ddl string) {
	t.Helper()
	execute(t, db, "DROP TABLE "+table)
	execute(t, db, ddl)
}

func seedIdentity(t *testing.T, db *store.DB) {
	t.Helper()
	execute(t, db, `INSERT INTO memories (layer, content, origin)
	                 VALUES ('project', 'anchor of adoption', 'agent')`)
}

func contains(list []string, value string) bool {
	for _, v := range list {
		if v == value {
			return true
		}
	}
	return false
}

func namesTheDifference(report store.Report, table, column string) bool {
	for _, d := range report.Differences {
		if d.Table == table && d.Column == column {
			return true
		}
	}
	return false
}
