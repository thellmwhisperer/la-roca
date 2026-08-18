package cli

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/distribution/rocacorpus"
	"github.com/thellmwhisperer/la-roca/internal/distribution/rocavector"
	"github.com/thellmwhisperer/la-roca/internal/distribution/supportreport"
	"github.com/thellmwhisperer/la-roca/internal/store"
)

const (
	personalName    = "Javier"
	personalPeer    = "Ana"
	personalProject = "Nortada"
	personalTalk    = "secret handshake about tulips"
	personalPath    = "/Users/javiermellado/Documents/secret-chat.json"
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
			name:     "federated install",
			setup:    setupFederatedSupportHome,
			wantMode: supportreport.FederationFederated,
			want: []string{
				"mode: federated", "serving: cutover", "corpus_custody: plugin-corpus",
				"plugin-corpus: present", "migrations:", "data2-memory-custody: verified",
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
	runRoot(t, contractBuild(), "doctor", "--report", "--db-path", dbPath)
	if _, err := os.Stat(filepath.Join(home, ".roca", "plugins")); !os.IsNotExist(err) {
		t.Fatalf("report created a plugins tree: %v", err)
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
	dbPath := filepath.Join(home, ".roca", "roca.db")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
		t.Fatal(err)
	}
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

func setupFederatedSupportHome(t *testing.T, home string) string {
	t.Helper()
	dbPath := setupFreshSupportHome(t, home)
	configPath := filepath.Join(home, ".roca", "config.toml")
	if err := os.WriteFile(configPath, []byte("[layout]\nserving = \"cutover\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	corpusPath := filepath.Join(home, ".roca", "plugins", rocacorpus.Name, rocacorpus.DatabaseFilename)
	corpus := openLayoutDatabase(t, corpusPath)
	if _, err := corpus.Exec(`
		INSERT INTO sessions (session_id, project, started_at)
		VALUES ('federated-1', ?, '2026-03-01T00:00:00Z')`, personalProject); err != nil {
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
		INSERT INTO plugin_migrations (migration, destination_table, migration_state)
		VALUES ('data2-memory-custody', 'memory_records', 'verified')`); err != nil {
		t.Fatal(err)
	}
	if err := corpus.Close(); err != nil {
		t.Fatal(err)
	}
	writeVectorSupportFixture(t, home)
	return dbPath
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
