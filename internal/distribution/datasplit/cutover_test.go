package datasplit

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/distribution/rocacorpus"
	"github.com/thellmwhisperer/la-roca/internal/distribution/rocacron"
	"github.com/thellmwhisperer/la-roca/internal/distribution/rocaops"
	"github.com/thellmwhisperer/la-roca/internal/store"
)

func TestPrepareHubRunsEveryShadowCustodyMigrationBeforeCutover(t *testing.T) {
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

	if _, err := PrepareHub(t.Context(), options); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareHub(t.Context(), options); err != nil {
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
