package cli

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/distribution/datasplit"
	"github.com/thellmwhisperer/la-roca/internal/distribution/logfile"
	"github.com/thellmwhisperer/la-roca/internal/distribution/rocacorpus"
	"github.com/thellmwhisperer/la-roca/internal/distribution/rocacron"
	"github.com/thellmwhisperer/la-roca/internal/distribution/rocaops"
	"github.com/thellmwhisperer/la-roca/internal/distribution/rocavector"
	"github.com/thellmwhisperer/la-roca/internal/distribution/supportreport"
	"github.com/thellmwhisperer/la-roca/internal/store"
)

const (
	personalName    = "Ismael"
	personalPeer    = "Ana"
	personalProject = "Nortada"
	personalTalk    = "secret handshake about tulips"
	personalPath    = "/Users/op/Documents/secret-chat.json"
)

func TestDoctorReport(t *testing.T) {
	cases := []struct {
		name     string
		setup    func(*testing.T, string) string
		wantMode string
		want     []string
		refuse   []string
		json     bool
	}{
		{
			name:     "fresh install",
			setup:    setupFreshSupportHome,
			wantMode: supportreport.FederationFresh,
			want: []string{
				"```text", "roca support report", "FEDERATION", "mode: fresh",
				"serving: legacy-serving", "corpus_custody: empty",
				"plugin-corpus: present", "plugin-ops: present", "core: present",
			},
		},
		{
			name:     "legacy-only install",
			setup:    setupLegacySupportHome,
			wantMode: supportreport.FederationLegacyOnly,
			want: []string{
				"mode: legacy-only", "corpus_custody: legacy-core",
				"plugin-corpus: absent", "plugin-ops: absent", "PLUGINS\nnone",
			},
			refuse: []string{personalName, personalPeer, personalProject, personalTalk, personalPath},
		},
		{
			name:     "unreadable core store",
			setup:    setupUnreadableSupportHome,
			wantMode: supportreport.FederationLegacyOnly,
			want: []string{
				"mode: legacy-only", "corpus_custody: unknown", "core: unreadable",
			},
		},
		{
			name:     "unverified cutover",
			setup:    setupUnverifiedCutoverHome,
			wantMode: supportreport.FederationMigrating,
			want: []string{
				"mode: migrating", "serving: cutover", "cutover_eligible: false",
			},
		},
		{
			name:     "DATA-2 and DATA-3 only cutover",
			setup:    setupIncompleteData4SupportHome,
			wantMode: supportreport.FederationMigrating,
			want: []string{
				"mode: migrating", "serving: cutover", "cutover_eligible: false",
			},
		},
		{
			name:     "federated install",
			setup:    setupFederatedSupportHome,
			wantMode: supportreport.FederationFederated,
			want: []string{
				"mode: federated", "serving: cutover", "corpus_custody: plugin-corpus",
				"cutover_eligible: true",
				"plugin-corpus: present", "migrations:", "data2-memory-custody: verified",
				"runtime_layers_not_in_registry: pass",
				"orphan_supersedes: fail", "test_source_agent_rows: fail", "ghost_sessions: warn",
				"VECTOR", "model: nomic-embed-text", "chunks: sessions=3",
				"last_delta: exit=0 added=3",
			},
			refuse: []string{personalName, personalTalk, personalPath},
		},
		{
			name:     "json envelope",
			setup:    setupFreshSupportHome,
			wantMode: supportreport.FederationFresh,
			json:     true,
			want:     []string{`"kind": "` + supportreport.Kind + `"`, `"mode": "` + supportreport.FederationFresh + `"`},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			isolateRuntimeDirs(t, home)
			dbPath := tc.setup(t, home)
			args := []string{"doctor", "--report", "--db-path", dbPath}
			if tc.json {
				args = append(args, "--json")
			}
			out := runRoot(t, contractBuild(), args...)
			if !strings.Contains(out, tc.wantMode) {
				t.Fatalf("report lacks federation mode %q:\n%s", tc.wantMode, out)
			}
			for _, want := range tc.want {
				if !strings.Contains(out, want) {
					t.Errorf("report lacks %q:\n%s", want, out)
				}
			}
			refuse := append(append([]string{}, tc.refuse...), home)
			for _, leak := range refuse {
				if leak != "" && strings.Contains(out, leak) {
					t.Errorf("report leaked %q:\n%s", leak, out)
				}
			}
			if tc.json {
				mustJSON(t, out)
			} else if !strings.HasPrefix(out, "```text") || !strings.HasSuffix(out, "```") {
				t.Errorf("report is not one fenced block:\n%s", out)
			}
		})
	}
}

func TestDoctorReportLeavesALegacyInstallUntouched(t *testing.T) {
	home := t.TempDir()
	isolateRuntimeDirs(t, home)
	dbPath := setupLegacySupportHome(t, home)
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Lstat(dbPath + suffix); !os.IsNotExist(err) {
			t.Fatalf("fixture left a SQLite sidecar %q: %v", suffix, err)
		}
	}
	runRoot(t, contractBuild(), "doctor", "--report", "--db-path", dbPath)
	if _, err := os.Stat(filepath.Join(home, ".roca", "plugins")); !os.IsNotExist(err) {
		t.Fatalf("report created a plugins tree: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".roca", logfile.DirName)); !os.IsNotExist(err) {
		t.Fatalf("report created an execution log: %v", err)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Lstat(dbPath + suffix); !os.IsNotExist(err) {
			t.Fatalf("report created a SQLite sidecar %q: %v", suffix, err)
		}
	}
}

func TestDoctorReportDoesNotAppendOpsAudit(t *testing.T) {
	home := t.TempDir()
	isolateRuntimeDirs(t, home)
	dbPath := setupFreshSupportHome(t, home)
	opsPath := filepath.Join(home, ".roca", "plugins", rocaops.Name, rocaops.DatabaseFilename)
	ops := openLayoutDatabase(t, opsPath)
	defer ops.Close()
	var before, after int
	if err := ops.QueryRow("SELECT COUNT(*) FROM call_history").Scan(&before); err != nil {
		t.Fatal(err)
	}
	runRoot(t, contractBuild(), "doctor", "--report", "--db-path", dbPath)
	if err := ops.QueryRow("SELECT COUNT(*) FROM call_history").Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("report appended ops audit rows: before=%d after=%d", before, after)
	}
}

func setupFreshSupportHome(t *testing.T, home string) string {
	t.Helper()
	dbPath := filepath.Join(home, ".roca", "roca.db")
	runRoot(t, contractBuild(), "init", "--db-path", dbPath)
	return dbPath
}

func setupLegacySupportHome(t *testing.T, home string) string {
	t.Helper()
	dbPath := prepareSupportDatabasePath(t, home)
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ApplySchema(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL().Exec(`
		INSERT INTO sessions (session_id, project, started_at, title)
		VALUES ('legacy-1', ?, '2026-01-02T00:00:00Z', ?)`, personalProject, personalTalk); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL().Exec(`
		INSERT INTO exchanges (session_id, exchange_number, human_text, agent_text)
		VALUES ('legacy-1', 1, ?, ?)`,
		personalName+" told "+personalPeer+" "+personalTalk,
		"I remember "+personalProject); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL().Exec(`
		INSERT INTO memories (layer, content, origin) VALUES ('handoff', ?, 'agent')`,
		personalName+" decided "+personalTalk); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL().Exec(`
		INSERT INTO ingest_file_state (path, source_kind, source_agent, last_synced_at)
		VALUES (?, 'claude_jsonl', 'claude', '2026-08-01T12:00:00Z')`, personalPath); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return dbPath
}

func setupUnreadableSupportHome(t *testing.T, home string) string {
	t.Helper()
	dbPath := prepareSupportDatabasePath(t, home)
	if err := os.WriteFile(dbPath, []byte("not a sqlite database"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dbPath
}

func setupUnverifiedCutoverHome(t *testing.T, home string) string {
	t.Helper()
	dbPath := setupFreshSupportHome(t, home)
	writeCutoverSupportConfig(t, home)
	return dbPath
}

func prepareSupportDatabasePath(t *testing.T, home string) string {
	t.Helper()
	dbPath := filepath.Join(home, ".roca", "roca.db")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
		t.Fatal(err)
	}
	return dbPath
}

func writeCutoverSupportConfig(t *testing.T, home string) {
	t.Helper()
	configPath := filepath.Join(home, ".roca", "config.toml")
	if err := os.WriteFile(configPath, []byte("[layout]\nserving = \"cutover\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func setupFederatedSupportHome(t *testing.T, home string) string {
	t.Helper()
	dbPath, options := setupFederationSupportSources(t, home)
	digest := strings.Repeat("a", 64)
	recordData23SupportMigrations(t, options, digest)
	recordData4RunParity(t, options, digest)
	writeVectorSupportFixture(t, home)
	return dbPath
}

func setupIncompleteData4SupportHome(t *testing.T, home string) string {
	t.Helper()
	dbPath, options := setupFederationSupportSources(t, home)
	digest := strings.Repeat("a", 64)
	recordData23SupportMigrations(t, options, digest)
	return dbPath
}

func recordData23SupportMigrations(t *testing.T, options datasplit.HubOptions, digest string) {
	t.Helper()
	corpus := openLayoutDatabase(t, options.CorpusDatabase)
	if _, err := corpus.Exec(`INSERT INTO session_versions
		(version_digest, session_id, project, started_at)
		VALUES (?, 'rollback-1', ?, '2026-02-01T00:00:00Z')`, digest, personalProject); err != nil {
		t.Fatal(err)
	}
	recordVerifiedMigrations(t, corpus, digest, map[string]string{
		"corpus-archive-sessions":          "session_versions",
		"corpus-archive-exchanges":         "exchange_versions",
		"corpus-archive-tool-uses":         "tool_use_versions",
		"corpus-archive-thinking-blocks":   "thinking_block_versions",
		"corpus-archive-ingest-file-state": "ingest_file_state_versions",
		"corpus-archive-reconciliation-v1": "session_versions",
	})
	if err := corpus.Close(); err != nil {
		t.Fatal(err)
	}
	ops := openLayoutDatabase(t, options.OpsDatabase)
	recordVerifiedMigrations(t, ops, digest, map[string]string{
		"data2-memory-custody": "memory_records",
	})
	if err := ops.Close(); err != nil {
		t.Fatal(err)
	}
}

func recordData4RunParity(t *testing.T, options datasplit.HubOptions, digest string) {
	t.Helper()
	cron := openLayoutDatabase(t, options.CronDatabase)
	defer cron.Close()
	if _, err := cron.Exec(`INSERT INTO plugin_migrations
		(migration, destination_table, migration_state)
		VALUES ('data4-legacy-runs', 'legacy_runs', 'batch-in-progress');
		INSERT INTO legacy_runs (canonical_digest, payload) VALUES (?, '{"id":1,"name":"synthetic-run"}');
		INSERT INTO migration_batches
		(migration, batch_id, destination_table, source_database, source_table,
		 row_count, canonical_digest, high_water_mark)
		VALUES ('data4-legacy-runs', 'data4-runs-1', 'legacy_runs', 'core', 'runs', 1, ?, '1');
		INSERT INTO custody_memberships
		(migration, source_database, source_table, source_key, destination_table,
		 destination_key, canonical_digest, batch_id)
		VALUES ('data4-legacy-runs', 'core', 'runs', '1', 'legacy_runs', ?, ?, 'data4-runs-1')`,
		digest, digest, digest, digest); err != nil {
		t.Fatal(err)
	}
}

func setupFederationSupportSources(t *testing.T, home string) (string, datasplit.HubOptions) {
	t.Helper()
	dbPath := setupFreshSupportHome(t, home)
	writeCutoverSupportConfig(t, home)
	core := openLayoutDatabase(t, dbPath)
	if _, err := core.Exec(`
		INSERT INTO sessions (session_id, project, started_at)
		VALUES ('rollback-1', ?, '2026-02-01T00:00:00Z')`, personalProject); err != nil {
		t.Fatal(err)
	}
	if _, err := core.Exec(`
		INSERT INTO exchanges (session_id, exchange_number, human_text)
		VALUES ('rollback-1', 1, ?)`, personalTalk); err != nil {
		t.Fatal(err)
	}
	if _, err := core.Exec(`
		INSERT INTO memories (layer, content, origin, source_agent)
		VALUES ('handoff', ?, 'agent', 'test')`, personalTalk); err != nil {
		t.Fatal(err)
	}
	if _, err := core.Exec(`CREATE TABLE runs (id INTEGER PRIMARY KEY, name TEXT);
		INSERT INTO runs (id, name) VALUES (1, 'synthetic-run')`); err != nil {
		t.Fatal(err)
	}
	if err := core.Close(); err != nil {
		t.Fatal(err)
	}
	corpusPath := filepath.Join(home, ".roca", "plugins", rocacorpus.Name, rocacorpus.DatabaseFilename)
	corpus := openLayoutDatabase(t, corpusPath)
	if _, err := corpus.Exec(`
		INSERT INTO sessions (session_id, project)
		VALUES ('federated-1', ?)`, personalProject); err != nil {
		t.Fatal(err)
	}
	if _, err := corpus.Exec(`
		INSERT INTO exchanges (session_id, exchange_number, human_text)
		VALUES ('federated-1', 1, ?)`, personalTalk); err != nil {
		t.Fatal(err)
	}
	if _, err := corpus.Exec(`
		INSERT INTO ingest_file_state (path, source_kind, source_agent, last_synced_at)
		VALUES (?, 'claude_jsonl', 'claude', '2026-08-17T09:00:00Z')`, personalPath); err != nil {
		t.Fatal(err)
	}
	if _, err := corpus.Exec(`
		INSERT INTO memories (layer, content, origin, supersedes)
		VALUES ('handoff', ?, 'agent', 999999)`, personalTalk); err != nil {
		t.Fatal(err)
	}
	if err := corpus.Close(); err != nil {
		t.Fatal(err)
	}
	opsPath := filepath.Join(home, ".roca", "plugins", rocaops.Name, rocaops.DatabaseFilename)
	cronPath := filepath.Join(home, ".roca", "plugins", rocacron.Name, rocacron.DatabaseFilename)
	if err := os.MkdirAll(filepath.Dir(cronPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := rocacron.ApplySchema(cronPath); err != nil {
		t.Fatal(err)
	}
	return dbPath, datasplit.HubOptions{
		CoreDatabase: dbPath, CorpusDatabase: corpusPath, OpsDatabase: opsPath, CronDatabase: cronPath,
		SnapshotDir: filepath.Join(home, ".roca", "backups", "data-split"),
		LockPath:    logfile.New(filepath.Dir(dbPath)).LockPath(),
	}
}

func recordVerifiedMigrations(t *testing.T, db *sql.DB, digest string, migrations map[string]string) {
	t.Helper()
	for migration, destination := range migrations {
		if _, err := db.Exec(`INSERT INTO plugin_migrations
			(migration, destination_table, migration_state, verification_digest)
			VALUES (?, ?, 'verified', ?)
			ON CONFLICT(migration) DO UPDATE SET
				destination_table=excluded.destination_table,
				migration_state=excluded.migration_state,
				verification_digest=excluded.verification_digest`, migration, destination, digest); err != nil {
			t.Fatal(err)
		}
	}
}

func writeVectorSupportFixture(t *testing.T, home string) {
	t.Helper()
	state := filepath.Join(home, ".roca", "plugins", rocavector.Name, rocavector.StateDir)
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(state, "vector.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT)`,
		`CREATE TABLE chunks (id INTEGER PRIMARY KEY, source_kind TEXT)`,
		`INSERT INTO meta VALUES ('model', 'nomic-embed-text'), ('dimensions', '768')`,
		`INSERT INTO chunks (source_kind) VALUES ('sessions'), ('sessions'), ('sessions')`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "completion.json"), []byte(
		`{"exit_status":0,"counts":{"added":3,"updated":0,"removed":0,"unchanged":1,"chunks":3},"finished_at":"2026-08-17T10:00:00Z"}`),
		0o600); err != nil {
		t.Fatal(err)
	}
}
