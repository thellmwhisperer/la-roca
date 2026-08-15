package bundledplugin_test

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/distribution/bundledplugin"
	"github.com/thellmwhisperer/la-roca/internal/distribution/migrationledger"
	"github.com/thellmwhisperer/la-roca/internal/distribution/plugininstall"
	"github.com/thellmwhisperer/la-roca/internal/distribution/rocacorpus"
	"github.com/thellmwhisperer/la-roca/internal/distribution/rocacron"
	"github.com/thellmwhisperer/la-roca/internal/distribution/rocaops"
	_ "modernc.org/sqlite"
)

func TestApplyingPluginSchemasTwicePreservesOldFrozenHomes(t *testing.T) {
	for _, fixture := range []struct {
		name, filename, oldSchema, insert, count, indexCount string
		apply                                                func(string) error
	}{
		{
			name: "ops", filename: rocaops.DatabaseFilename, apply: rocaops.ApplySchema,
			oldSchema: `CREATE TABLE memories (
				id INTEGER PRIMARY KEY AUTOINCREMENT, layer TEXT NOT NULL, content TEXT NOT NULL,
				metadata TEXT DEFAULT '{}', origin TEXT NOT NULL, source_agent TEXT, source_model TEXT,
				source_surface TEXT, source_session TEXT, source_sequence INTEGER, project TEXT,
				status TEXT DEFAULT 'active', supersedes INTEGER,
				created_at TEXT DEFAULT (datetime('now')), expires_at TEXT)`,
			insert:     `INSERT INTO memories (layer, content, origin) VALUES ('handoff', 'frozen ops row', 'agent')`,
			count:      `SELECT COUNT(*) FROM memories WHERE content = 'frozen ops row'`,
			indexCount: `SELECT COUNT(*) FROM memories_fts WHERE memories_fts MATCH 'frozen'`,
		},
		{
			name: "corpus", filename: rocacorpus.DatabaseFilename, apply: rocacorpus.ApplySchema,
			oldSchema: `CREATE TABLE sessions (
				session_id TEXT PRIMARY KEY, source_agent TEXT DEFAULT 'claude-code', project TEXT,
				started_at TEXT, ended_at TEXT, duration_minutes INTEGER, title TEXT,
				metadata TEXT DEFAULT '{}')`,
			insert: `INSERT INTO sessions (session_id, title) VALUES ('frozen-session', 'frozen corpus row')`,
			count:  `SELECT COUNT(*) FROM sessions WHERE title = 'frozen corpus row'`,
		},
		{
			name: "cron", filename: rocacron.DatabaseFilename, apply: rocacron.ApplySchema,
			oldSchema: `CREATE TABLE journeys (
				id INTEGER PRIMARY KEY AUTOINCREMENT, train TEXT NOT NULL, ride TEXT NOT NULL,
				plugin TEXT NOT NULL, started_at TEXT NOT NULL, ended_at TEXT NOT NULL,
				duration_ms INTEGER NOT NULL, exit_code INTEGER, error TEXT NOT NULL DEFAULT '',
				gate_status TEXT NOT NULL, stdout TEXT NOT NULL DEFAULT '', stderr TEXT NOT NULL DEFAULT '')`,
			insert: `INSERT INTO journeys
				(train, ride, plugin, started_at, ended_at, duration_ms, gate_status)
				VALUES ('nightly', 'frozen-cron-row', 'synthetic', 'start', 'end', 1, 'ready')`,
			count: `SELECT COUNT(*) FROM journeys WHERE ride = 'frozen-cron-row'`,
		},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), fixture.filename)
			db := openFixture(t, path)
			if _, err := db.Exec(fixture.oldSchema); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(fixture.insert); err != nil {
				t.Fatal(err)
			}
			assertQueryCount(t, db, fixture.count, 1)
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}

			for range 2 {
				if err := fixture.apply(path); err != nil {
					t.Fatal(err)
				}
			}
			db = openFixture(t, path)
			defer db.Close()
			assertQueryCount(t, db, fixture.count, 1)
			if fixture.indexCount != "" {
				assertQueryCount(t, db, fixture.indexCount, 1)
			}
			state, err := migrationledger.Inspect(t.Context(), db)
			if err != nil {
				t.Fatal(err)
			}
			if state.Plugin != "roca-"+fixture.name || state.SchemaVersion < 1 {
				t.Fatalf("plugin identity = %+v, want roca-%s", state, fixture.name)
			}
		})
	}
}

func TestAnInterruptedUpgradeLeavesTheManifestOnTheVersionItsSchemaReached(t *testing.T) {
	root := filepath.Join(t.TempDir(), "plugins")
	directory := filepath.Join(root, "synthetic-ledger")
	live := filepath.Join(directory, "synthetic.db")
	interrupted := false
	spec := bundledplugin.Spec{
		Name: "synthetic-ledger", DatabaseFilename: "synthetic.db",
		Source: "bundled:roca",
		Semantic: []byte("version: 1\nattachment: on-demand\n" +
			"description: Synthetic bundle whose schema upgrade is interrupted.\n" +
			"questions:\n  - Which synthetic rows exist?\n" +
			"tables:\n  - name: records\n    description: Synthetic rows.\n    columns: [id]\n"),
		ApplySchema: func(path string) error {
			if interrupted && path == live {
				return errors.New("synthetic interruption")
			}
			return bundledplugin.ApplySchema(path, "synthetic-ledger",
				`CREATE TABLE IF NOT EXISTS records (id INTEGER PRIMARY KEY)`, 1, 0)
		},
	}
	if _, err := bundledplugin.Ensure(root, t.TempDir(), "v-one", spec); err != nil {
		t.Fatal(err)
	}

	interrupted = true
	if _, err := bundledplugin.Ensure(root, t.TempDir(), "v-two", spec); err == nil {
		t.Fatal("an interrupted schema upgrade was reported as a successful install")
	}
	manifest, err := plugininstall.ReadManifest(directory)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Version != "v-one" {
		t.Fatalf("manifest version = %q, but its schema never reached that version", manifest.Version)
	}
}

func openFixture(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func assertQueryCount(t *testing.T, db *sql.DB, query string, want int) {
	t.Helper()
	var got int
	if err := db.QueryRow(query).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("row count = %d, want %d", got, want)
	}
}
