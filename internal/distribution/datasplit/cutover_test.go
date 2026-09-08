package datasplit

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"

	"github.com/thellmwhisperer/la-roca/internal/distribution/corpusarchive"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/distribution/rocacorpus"
	"github.com/thellmwhisperer/la-roca/internal/distribution/rocacron"
	"github.com/thellmwhisperer/la-roca/internal/distribution/rocaops"
	"github.com/thellmwhisperer/la-roca/internal/store"
)

func TestMigrateRunsEveryShadowCustodyMigrationBeforeCutover(t *testing.T) {
	directory := t.TempDir()
	options := HubOptions{
		CoreDatabase:   filepath.Join(directory, "roca.db"),
		OpsDatabase:    filepath.Join(directory, "plugins", rocaops.Name, rocaops.DatabaseFilename),
		CorpusDatabase: filepath.Join(directory, "plugins", rocacorpus.Name, rocacorpus.DatabaseFilename),
		CronDatabase:   filepath.Join(directory, "plugins", rocacron.Name, rocacron.DatabaseFilename),
		SnapshotDir:    filepath.Join(directory, "backups", "data-split"),
		LockPath:       filepath.Join(directory, "data-split.lock"),
	}
	seedHubSources(t, options)
	if ready, err := HubCutoverEligible(t.Context(), options); err != nil || ready {
		t.Fatalf("pre-migration eligibility = %t, err=%v", ready, err)
	}

	if _, err := Migrate(t.Context(), options); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(options.SnapshotDir, options.SnapshotDir+"-offline"); err != nil {
		t.Fatal(err)
	}
	if _, err := Migrate(t.Context(), options); err != nil {
		t.Fatalf("idempotent preparation: %v", err)
	}
	if ready, err := HubCutoverEligible(t.Context(), options); err != nil || !ready {
		t.Fatalf("prepared eligibility = %t, err=%v", ready, err)
	}
	missingCron := options
	missingCron.CronDatabase = filepath.Join(directory, "missing-cron.db")
	if ready, err := HubCutoverEligible(t.Context(), missingCron); err == nil || ready {
		t.Fatalf("missing-cron eligibility = %t, err=%v", ready, err)
	}

	ops := openCutoverDatabase(t, options.OpsDatabase)
	defer ops.Close()
	var memories int
	if err := ops.QueryRow(`SELECT COUNT(*) FROM memory_compatibility
		WHERE source_database = 'core' AND id = 17`).Scan(&memories); err != nil {
		t.Fatal(err)
	}
	if memories != 1 {
		t.Fatalf("core memory memberships = %d, want 1", memories)
	}

	corpus := openCutoverDatabase(t, options.CorpusDatabase)
	defer corpus.Close()
	var sessions int
	if err := corpus.QueryRow(`SELECT COUNT(*) FROM session_version_memberships
		WHERE source_database = 'core' AND source_session_id = 'synthetic-session'`).Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	if sessions != 1 {
		t.Fatalf("core session memberships = %d, want 1", sessions)
	}
	var sourceRowID int64
	if err := corpus.QueryRow(`SELECT source_row_id FROM session_version_memberships
		WHERE source_database = 'core' AND source_session_id = 'synthetic-session'`).Scan(&sourceRowID); err != nil {
		t.Fatal(err)
	}
	if sourceRowID < 1 {
		t.Fatalf("core session rowid = %d", sourceRowID)
	}
	var currentSessions int
	if err := corpus.QueryRow(`SELECT COUNT(*) FROM sessions
		WHERE session_id = 'synthetic-session' AND title = 'Synthetic archive marker'`).
		Scan(&currentSessions); err != nil {
		t.Fatal(err)
	}
	if currentSessions != 1 {
		t.Fatalf("materialized current sessions = %d, want 1", currentSessions)
	}
}

func seedHubSources(t *testing.T, options HubOptions) {
	t.Helper()
	core, err := store.Open(options.CoreDatabase)
	if err != nil {
		t.Fatal(err)
	}
	defer core.Close()
	if err := store.ApplySchema(t.Context(), core); err != nil {
		t.Fatal(err)
	}
	if _, err := core.SQL().Exec(`INSERT INTO memories
		(id, layer, content, origin) VALUES (17, 'project', 'Synthetic migration marker', 'agent');
		INSERT INTO sessions (session_id, source_agent, title) VALUES
		('synthetic-session', 'fixture', 'Synthetic archive marker');
		CREATE TABLE runs (id INTEGER PRIMARY KEY, name TEXT);
		INSERT INTO runs (id, name) VALUES (1, 'synthetic-run')`); err != nil {
		t.Fatal(err)
	}
	for path, apply := range map[string]func(string) error{
		options.OpsDatabase: rocaops.ApplySchema, options.CorpusDatabase: rocacorpus.ApplySchema,
		options.CronDatabase: rocacron.ApplySchema,
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := apply(path); err != nil {
			t.Fatalf("prepare %s: %v", path, err)
		}
	}
}

func openCutoverDatabase(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func TestMigrateMaterializesCurrentRowsAfterStorageUpgrade(t *testing.T) {
	options := newMigrationFixture(t)
	if _, err := Migrate(t.Context(), options); err != nil {
		t.Fatal(err)
	}
	corpus := openCutoverDatabase(t, options.CorpusDatabase)
	if _, err := corpus.Exec(`DELETE FROM sessions;
		ALTER TABLE session_versions ADD COLUMN title TEXT;
		UPDATE session_versions SET title = 'Synthetic archive marker';
		UPDATE plugin_schema SET schema_version = 4`); err != nil {
		t.Fatal(err)
	}
	if err := corpus.Close(); err != nil {
		t.Fatal(err)
	}
	if err := rocacorpus.ApplySchema(options.CorpusDatabase); err != nil {
		t.Fatal(err)
	}
	if ready, err := corpusarchive.CutoverEligible(t.Context(), options.CorpusDatabase); err != nil || ready {
		t.Fatalf("unmaterialized upgrade eligibility = %t, err=%v", ready, err)
	}
	if report, err := Migrate(t.Context(), options); err != nil || !report.Ready {
		t.Fatalf("explicit upgrade migration = %+v, err=%v", report, err)
	}
	corpus = openCutoverDatabase(t, options.CorpusDatabase)
	defer corpus.Close()
	var title string
	if err := corpus.QueryRow(`SELECT title FROM sessions WHERE session_id = 'synthetic-session'`).Scan(&title); err != nil {
		t.Fatal(err)
	}
	if title != "Synthetic archive marker" {
		t.Fatalf("materialized title = %q", title)
	}
	if err := os.Rename(options.SnapshotDir, options.SnapshotDir+"-offline"); err != nil {
		t.Fatal(err)
	}
	if report, err := Migrate(t.Context(), options); err != nil || !report.Ready {
		t.Fatalf("verified upgrade reopened frozen sources: %+v, err=%v", report, err)
	}
}

func TestMigrateResumesEmptyMemoryCustodyAfterArchiveInterruption(t *testing.T) {
	options := newMigrationFixture(t)
	core := openCutoverDatabase(t, options.CoreDatabase)
	if _, err := core.Exec(`DELETE FROM memories;
		INSERT INTO exchanges(session_id, exchange_number, agent_text)
		VALUES ('synthetic-session', 1, 'resumable archive marker')`); err != nil {
		t.Fatal(err)
	}
	if err := core.Close(); err != nil {
		t.Fatal(err)
	}
	corpus := openCutoverDatabase(t, options.CorpusDatabase)
	defer corpus.Close()
	if _, err := corpus.Exec(`CREATE TRIGGER interrupt_archive BEFORE INSERT ON exchange_versions
		BEGIN SELECT RAISE(ABORT, 'synthetic archive interruption'); END`); err != nil {
		t.Fatal(err)
	}
	interrupted, err := Migrate(t.Context(), options)
	if err == nil || !strings.Contains(err.Error(), "synthetic archive interruption") {
		t.Fatalf("archive interruption = %v", err)
	}
	var snapshots int
	if err := corpus.QueryRow(`SELECT COUNT(*) FROM corpus_source_snapshots`).Scan(&snapshots); err != nil || snapshots != 2 {
		t.Fatalf("committed source snapshots = %d, err=%v", snapshots, err)
	}
	before := map[string]string{}
	for source, path := range interrupted.Memory.SnapshotPaths {
		digest, err := corpusarchive.SnapshotDigest(path)
		if err != nil {
			t.Fatal(err)
		}
		before[source] = digest
	}
	if _, err := corpus.Exec(`DROP TRIGGER interrupt_archive`); err != nil {
		t.Fatal(err)
	}
	if report, err := Migrate(t.Context(), options); err != nil || !report.Ready {
		t.Fatalf("resume after committed archive batches = %+v, err=%v", report, err)
	}
	for source, path := range interrupted.Memory.SnapshotPaths {
		digest, err := corpusarchive.SnapshotDigest(path)
		if err != nil || digest != before[source] {
			t.Fatalf("resume replaced %s snapshot: digest=%s err=%v", source, digest, err)
		}
	}
	var text string
	if err := corpus.QueryRow(`SELECT agent_text FROM exchanges WHERE session_id = 'synthetic-session'`).Scan(&text); err != nil {
		t.Fatal(err)
	}
	if text != "resumable archive marker" {
		t.Fatalf("resumed exchange = %q", text)
	}
}

func newMigrationFixture(t *testing.T) HubOptions {
	t.Helper()
	directory := t.TempDir()
	options := HubOptions{
		CoreDatabase:   filepath.Join(directory, "roca.db"),
		OpsDatabase:    filepath.Join(directory, "ops.db"),
		CorpusDatabase: filepath.Join(directory, "corpus.db"),
		CronDatabase:   filepath.Join(directory, "cron.db"),
		SnapshotDir:    filepath.Join(directory, "snapshots"),
	}
	seedHubSources(t, options)
	return options
}
