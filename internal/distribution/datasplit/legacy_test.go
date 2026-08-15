package datasplit

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/distribution/migrationledger"
	"github.com/thellmwhisperer/la-roca/internal/distribution/rocacron"
	_ "modernc.org/sqlite"
)

func TestLegacyOrphansResumeIntoExplicitQuarantineWithoutChangingJourneys(t *testing.T) {
	options := legacyFixture(t)
	if err := rocacron.ApplySchema(options.CronDatabase); err != nil {
		t.Fatal(err)
	}
	cron := openDatabase(t, options.CronDatabase)
	if _, err := cron.Exec(`INSERT INTO journeys
		(train, ride, plugin, started_at, ended_at, duration_ms, exit_code, error, gate_status)
		VALUES ('nightly', 'current-fixture', 'synthetic', 'start', 'end', 7, 0, '', 'ready')`); err != nil {
		t.Fatal(err)
	}
	if err := cron.Close(); err != nil {
		t.Fatal(err)
	}

	interrupted := errors.New("synthetic interruption between committed batches")
	_, err := importLegacyOrphans(context.Background(), options, func(table string, batch int) error {
		if table == "runs" && batch == 1 {
			return interrupted
		}
		return nil
	})
	if !errors.Is(err, interrupted) {
		t.Fatalf("interrupted import = %v", err)
	}
	cron = openDatabase(t, options.CronDatabase)
	assertCount(t, cron, "migration_batches", 1)
	assertCount(t, cron, "legacy_runs", 2)
	if err := cron.Close(); err != nil {
		t.Fatal(err)
	}

	first, err := ImportLegacyOrphans(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if !first.SourceValid || len(first.Undisposed) != 0 {
		t.Fatalf("source valid = %t, undisposed = %v", first.SourceValid, first.Undisposed)
	}
	wantRows := map[string]int{
		"runs": 3, "run_logs": 4, "garden_channels": 1, "garden_memberships": 1,
		"garden_messages": 2, "garden_read_cursors": 1, "garden_voice_leases": 1,
		"proposals": 2, "proposal_annotations": 1, "queryplan_teach_examples": 1,
		"flow_patterns": 3, "messages": 0, "search_state": 1,
	}
	assertReportCounts(t, first, wantRows)

	beforeBatches := destinationBatchCounts(t, options)
	second, err := ImportLegacyOrphans(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if after := destinationBatchCounts(t, options); !reflect.DeepEqual(after, beforeBatches) {
		t.Fatalf("idempotent replay changed batch counts from %v to %v", beforeBatches, after)
	}
	if !reflect.DeepEqual(reportDigests(first), reportDigests(second)) {
		t.Fatalf("idempotent replay changed table digests")
	}

	cron = openDatabase(t, options.CronDatabase)
	defer cron.Close()
	assertCountWhere(t, cron, "journeys", "ride = 'current-fixture'", 1)
	assertCount(t, cron, "legacy_runs", 3)
	assertCount(t, cron, "legacy_run_logs", 4)
	assertPayload(t, cron, "legacy_runs", 2, map[string]any{
		"error": "synthetic timeout", "started_at": "2026-08-14T09:00:00Z",
		"completed_at": "2026-08-14T09:01:00Z", "raw": map[string]any{"$blob": "00ff"},
	}, []string{"train", "ride", "gate_status"})
	assertPayload(t, cron, "legacy_run_logs", 4, map[string]any{
		"run_id": float64(2), "level": "error", "created_at": "2026-08-14T09:00:30Z",
	}, nil)

	ops := openDatabase(t, options.OpsDatabase)
	defer ops.Close()
	assertCount(t, ops, "legacy_records", 10)
	for _, recordType := range []string{"garden_channels", "garden_memberships", "garden_messages",
		"garden_read_cursors", "garden_voice_leases", "proposals", "proposal_annotations",
		"queryplan_teach_examples"} {
		assertCountWhere(t, ops, "legacy_records", "record_type = '"+recordType+"'", wantRows[recordType])
	}

	corpus := openDatabase(t, options.CorpusDatabase)
	defer corpus.Close()
	assertCount(t, corpus, "legacy_flow_patterns", 3)
	for _, db := range []*sql.DB{cron, ops, corpus} {
		assertCountWhere(t, db, "sqlite_schema", "type = 'table' AND name = 'messages'", 0)
		state, err := migrationledger.Inspect(context.Background(), db)
		if err != nil {
			t.Fatal(err)
		}
		if state.State != migrationledger.StateBatchInProgress {
			t.Fatalf("shadow destination state = %q, want batch-in-progress", state.State)
		}
	}

	source := openDatabase(t, options.SourceClone)
	defer source.Close()
	for table, want := range wantRows {
		assertCount(t, source, table, want)
	}
}

func TestLegacyImportRefusesAnUnratifiedDispositionBeforeCreatingDestinations(t *testing.T) {
	for _, fixture := range []struct {
		name, schema, want string
	}{
		{name: "unknown table", schema: `CREATE TABLE forgotten_rows (id INTEGER PRIMARY KEY)`, want: "undisposed tables: forgotten_rows"},
		{name: "nonempty messages", schema: `CREATE TABLE messages (id INTEGER PRIMARY KEY); INSERT INTO messages VALUES (1)`, want: "messages has 1 source rows"},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			directory := t.TempDir()
			source := filepath.Join(directory, "source.db")
			db := openDatabase(t, source)
			if _, err := db.Exec(fixture.schema); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			options := fixtureOptions(directory, source)
			_, err := ImportLegacyOrphans(context.Background(), options)
			if err == nil || !strings.Contains(err.Error(), fixture.want) {
				t.Fatalf("import error = %v, want %q", err, fixture.want)
			}
			for _, path := range []string{options.CronDatabase, options.OpsDatabase, options.CorpusDatabase} {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("preflight failure created destination %s: %v", path, err)
				}
			}
		})
	}
}

func TestRatifiedFrozenCloneCounts(t *testing.T) {
	source := os.Getenv("ROCA_DATA4_FROZEN_CLONE")
	if source == "" {
		t.Skip("set ROCA_DATA4_FROZEN_CLONE to replay the ratified read-only snapshot")
	}
	options := fixtureOptions(t.TempDir(), source)
	report, err := ImportLegacyOrphans(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	assertReportCounts(t, report, map[string]int{
		"runs": 435, "run_logs": 2973, "garden_channels": 5, "garden_memberships": 19,
		"garden_messages": 991, "garden_read_cursors": 14, "garden_voice_leases": 31,
		"proposals": 111, "proposal_annotations": 10, "queryplan_teach_examples": 1,
		"flow_patterns": 90464, "messages": 0, "search_state": 3,
	})
}

func legacyFixture(t *testing.T) LegacyOptions {
	t.Helper()
	directory := t.TempDir()
	sourcePath := filepath.Join(directory, "source.db")
	source := openDatabase(t, sourcePath)
	schema := `
		CREATE TABLE runs (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL, status TEXT NOT NULL,
			project TEXT, source_agent TEXT, metadata TEXT, started_at TEXT, completed_at TEXT,
			heartbeat_at TEXT, error TEXT, created_at TEXT, raw BLOB);
		CREATE TABLE run_logs (id INTEGER PRIMARY KEY AUTOINCREMENT, run_id INTEGER NOT NULL,
			level TEXT NOT NULL, message TEXT NOT NULL, metadata TEXT, created_at TEXT);
		CREATE TABLE garden_channels (id TEXT PRIMARY KEY, name TEXT, topic TEXT, kind TEXT, mode TEXT,
			project TEXT, created_at TEXT);
		CREATE TABLE garden_memberships (channel_id TEXT, nick TEXT, agent_type TEXT, parent_nick TEXT,
			role TEXT, joined_at TEXT, UNIQUE(channel_id, nick));
		CREATE TABLE garden_messages (id INTEGER PRIMARY KEY AUTOINCREMENT, channel_id TEXT, nick TEXT,
			type TEXT, content TEXT, reply_to INTEGER, created_at TEXT);
		CREATE TABLE garden_read_cursors (channel_id TEXT, nick TEXT, last_read_id INTEGER,
			updated_at TEXT, UNIQUE(channel_id, nick));
		CREATE TABLE garden_voice_leases (id INTEGER PRIMARY KEY AUTOINCREMENT, channel_id TEXT, nick TEXT,
			state TEXT, claimed_at TEXT, expires_at TEXT, heartbeat_at TEXT, yielded_at TEXT);
		CREATE TABLE proposals (id INTEGER PRIMARY KEY AUTOINCREMENT, kind TEXT, summary TEXT, payload TEXT,
			metadata TEXT, origin TEXT, source_agent TEXT, project TEXT, status TEXT,
			resolution_reason TEXT, approved_payload TEXT, supersedes INTEGER, resolved_by TEXT,
			created_at TEXT, resolved_at TEXT);
		CREATE TABLE proposal_annotations (id INTEGER PRIMARY KEY AUTOINCREMENT, proposal_id INTEGER,
			author TEXT, kind TEXT, content TEXT, reply_to INTEGER, metadata TEXT, created_at TEXT);
		CREATE TABLE queryplan_teach_examples (id INTEGER PRIMARY KEY AUTOINCREMENT, template TEXT,
			question TEXT, normalized_question TEXT, source_agent TEXT, metadata TEXT,
			created_at TEXT, updated_at TEXT);
		CREATE TABLE flow_patterns (id INTEGER PRIMARY KEY AUTOINCREMENT, session_id TEXT,
			exchange_number INTEGER, pattern TEXT, tool_count INTEGER, text_count INTEGER);
		CREATE TABLE messages (id INTEGER PRIMARY KEY AUTOINCREMENT, session_id TEXT,
			sequence INTEGER, role TEXT, content TEXT, timestamp TEXT);
		CREATE TABLE search_state (key TEXT PRIMARY KEY, value TEXT, updated_at TEXT);
		INSERT INTO runs VALUES
			(1, 'first', 'completed', 'synthetic', 'fixture', '{}', '2026-08-14T08:00:00Z', '2026-08-14T08:01:00Z', NULL, NULL, '2026-08-14T08:00:00Z', X'01'),
			(2, 'second', 'failed', 'synthetic', 'fixture', '{}', '2026-08-14T09:00:00Z', '2026-08-14T09:01:00Z', NULL, 'synthetic timeout', '2026-08-14T09:00:00Z', X'00ff'),
			(3, 'third', 'cancelled', NULL, NULL, '{}', NULL, NULL, NULL, NULL, '2026-08-14T10:00:00Z', NULL);
		INSERT INTO run_logs VALUES
			(1, 1, 'info', 'started', '{}', '2026-08-14T08:00:01Z'),
			(2, 1, 'info', 'finished', '{}', '2026-08-14T08:00:59Z'),
			(3, 2, 'warn', 'slow', '{}', '2026-08-14T09:00:20Z'),
			(4, 2, 'error', 'synthetic failure', '{"code":"timeout"}', '2026-08-14T09:00:30Z');
		INSERT INTO garden_channels VALUES ('chan', 'Synthetic channel', '', 'project', 'open', 'synthetic', '2026-08-14');
		INSERT INTO garden_memberships VALUES ('chan', 'fixture-agent', 'agent', NULL, 'member', '2026-08-14');
		INSERT INTO garden_messages VALUES (1, 'chan', 'fixture-agent', 'say', 'Synthetic hello', NULL, '2026-08-14'),
			(2, 'chan', 'fixture-agent', 'note', 'Synthetic note', 1, '2026-08-14');
		INSERT INTO garden_read_cursors VALUES ('chan', 'fixture-agent', 2, '2026-08-14');
		INSERT INTO garden_voice_leases VALUES (1, 'chan', 'fixture-agent', 'yielded', '2026-08-14', '2026-08-15', NULL, '2026-08-14');
		INSERT INTO proposals VALUES
			(1, 'change', 'Synthetic proposal', '{}', '{}', 'agent', 'fixture', 'synthetic', 'pending', NULL, NULL, NULL, NULL, '2026-08-14', NULL),
			(2, 'change', 'Synthetic resolution', '{}', '{}', 'human', NULL, 'synthetic', 'approved', 'synthetic approval', '{}', 1, 'fixture-owner', '2026-08-14', '2026-08-15');
		INSERT INTO proposal_annotations VALUES (1, 1, 'fixture-agent', 'note', 'Synthetic annotation', NULL, '{}', '2026-08-14');
		INSERT INTO queryplan_teach_examples VALUES (1, 'lookup', 'Synthetic question?', 'synthetic question', 'fixture', '{}', '2026-08-14', '2026-08-14');
		INSERT INTO flow_patterns VALUES (1, 'session-a', 1, 'text', 0, 1),
			(2, 'session-a', 2, 'tool', 1, 0), (3, NULL, NULL, NULL, 0, 0);
		INSERT INTO search_state VALUES ('lexical_index', '1', '2026-08-14');`
	if _, err := source.Exec(schema); err != nil {
		t.Fatal(err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	options := fixtureOptions(directory, sourcePath)
	options.BatchSize = 2
	return options
}

func fixtureOptions(directory, source string) LegacyOptions {
	return LegacyOptions{
		SourceClone: source, CronDatabase: filepath.Join(directory, "cron", "cron.db"),
		OpsDatabase:    filepath.Join(directory, "ops", "ops.db"),
		CorpusDatabase: filepath.Join(directory, "corpus", "corpus.db"),
	}
}

func openDatabase(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func assertReportCounts(t *testing.T, report LegacyReport, want map[string]int) {
	t.Helper()
	if len(report.Undisposed) != 0 {
		t.Fatalf("undisposed tables = %v", report.Undisposed)
	}
	got := make(map[string]int, len(report.Tables))
	for _, result := range report.Tables {
		got[result.SourceTable] = result.SourceRows
		if result.DestinationTable != "" && result.CanonicalDigest == "" {
			t.Fatalf("%s has no canonical digest", result.SourceTable)
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("source counts = %v, want %v", got, want)
	}
}

func destinationBatchCounts(t *testing.T, options LegacyOptions) map[string]int {
	t.Helper()
	counts := make(map[string]int)
	for name, path := range map[string]string{"cron": options.CronDatabase, "ops": options.OpsDatabase, "corpus": options.CorpusDatabase} {
		db := openDatabase(t, path)
		var count int
		if err := db.QueryRow("SELECT COUNT(*) FROM migration_batches").Scan(&count); err != nil {
			db.Close()
			t.Fatal(err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		counts[name] = count
	}
	return counts
}

func reportDigests(report LegacyReport) map[string]string {
	digests := make(map[string]string)
	for _, result := range report.Tables {
		digests[result.SourceTable] = result.CanonicalDigest
	}
	return digests
}

func assertCount(t *testing.T, db *sql.DB, table string, want int) {
	t.Helper()
	assertCountWhere(t, db, table, "1 = 1", want)
}

func assertCountWhere(t *testing.T, db *sql.DB, table, where string, want int) {
	t.Helper()
	var got int
	if err := db.QueryRow("SELECT COUNT(*) FROM " + quoteIdentifier(table) + " WHERE " + where).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("%s rows where %s = %d, want %d", table, where, got, want)
	}
}

func assertPayload(t *testing.T, db *sql.DB, table string, sourceID int, fields map[string]any, absent []string) {
	t.Helper()
	var payload string
	query := "SELECT payload FROM " + quoteIdentifier(table) + " WHERE json_extract(payload, '$.id') = ?"
	if err := db.QueryRow(query, sourceID).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
		t.Fatal(err)
	}
	for field, want := range fields {
		if got := decoded[field]; !reflect.DeepEqual(got, want) {
			t.Errorf("%s payload field %s = %#v, want %#v", table, field, got, want)
		}
	}
	for _, field := range absent {
		if _, exists := decoded[field]; exists {
			t.Errorf("%s payload fabricated field %s", table, field)
		}
	}
}
