package cli

import (
	"database/sql"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/distribution/corpusarchive"
	"github.com/thellmwhisperer/la-roca/internal/distribution/rocacorpus"
	"github.com/thellmwhisperer/la-roca/internal/distribution/rocaops"
	"github.com/thellmwhisperer/la-roca/internal/provider/query"
	"github.com/thellmwhisperer/la-roca/internal/provider/service"
	"github.com/thellmwhisperer/la-roca/internal/store"
	_ "modernc.org/sqlite"
)

func TestCutoverCLIHasNoFileBackedKernelDependency(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	core := filepath.Join(home, "selected", "roca.db")
	if err := os.MkdirAll(filepath.Dir(core), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(core), "config.toml"),
		[]byte("[layout]\nserving = \"cutover\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := &cliEnv{dbPath: core, out: io.Discard, errOut: io.Discard,
		build: Build{Version: "v-test", Commit: "fixture"}}
	svc, _, err := env.openService()
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	if _, err := os.Stat(core); !os.IsNotExist(err) {
		t.Fatalf("cutover CLI touched roca.db: %v", err)
	}
}

func TestShadowCLIComparesTheHubAfterExplicitMigration(t *testing.T) {
	t.Setenv("ROCA_MODELS_ORDER", "claude")
	t.Setenv("PATH", t.TempDir())
	home := t.TempDir()
	t.Setenv("HOME", home)
	corePath := filepath.Join(home, "selected", "roca.db")
	if err := os.MkdirAll(filepath.Dir(corePath), 0o700); err != nil {
		t.Fatal(err)
	}
	env := seedLayoutCore(t, corePath, `INSERT INTO memories
		(id, layer, content, origin) VALUES (29, 'project', 'Synthetic shadow custody marker', 'agent')`)
	if err := os.WriteFile(filepath.Join(filepath.Dir(corePath), "config.toml"),
		[]byte("[layout]\nserving = \"shadow-equal\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if code, err := executeWithEnv(env, []string{"--db-path", corePath, "migrate"}, nil); err != nil || code != 0 {
		t.Fatalf("explicit migration: code=%d err=%v", code, err)
	}
	svc, _, err := env.openService()
	if err != nil {
		t.Fatal(err)
	}
	// Deterministic search exercises compatibility reads without model inference.
	runSearch := func() ([]map[string]any, error) {
		_, rows, _, _, _, err := svc.SearchByTerm(t.Context(), query.Plan{Template: query.TemplateSearchByTerm, Term: "shadow+custody+marker"}, "", service.DefaultMaxChars, true, service.PluginRoute{IncludeCore: true})
		return rows, err
	}
	result, err := runSearch()
	if err != nil || len(result) != 1 {
		t.Fatalf("shadow result = %+v, err = %v", result, err)
	}
	initialMarker, err := os.ReadFile(filepath.Join(filepath.Dir(corePath), "config.toml"))
	if err != nil || string(initialMarker) != "[layout]\nserving = \"shadow-equal\"\n" {
		t.Fatalf("equal reads rolled back the marker = %q, err = %v", initialMarker, err)
	}

	opsPath := filepath.Join(home, ".roca", "plugins", rocaops.Name, rocaops.DatabaseFilename)
	ops := openLayoutDatabase(t, opsPath)
	var memberships int
	if err := ops.QueryRow(`SELECT COUNT(*) FROM memory_compatibility
		WHERE source_database = 'core' AND id = 29`).Scan(&memberships); err != nil {
		t.Fatal(err)
	}
	if memberships != 1 {
		t.Fatalf("core memory memberships = %d, want 1", memberships)
	}
	if _, err := ops.Exec(`UPDATE memory_records SET content = 'Synthetic divergent hub row'
		WHERE id = (SELECT physical_id FROM memory_compatibility
			WHERE source_database = 'core' AND id = 29)`); err != nil {
		t.Fatal(err)
	}
	result, err = runSearch()
	if err != nil || len(result) != 1 || result[0]["text"] != "Synthetic shadow custody marker" {
		t.Fatalf("legacy rollback answer = %+v, err = %v", result, err)
	}
	if err := ops.Close(); err != nil {
		t.Fatal(err)
	}
	if err := svc.Close(); err != nil {
		t.Fatal(err)
	}
	marker, err := os.ReadFile(filepath.Join(filepath.Dir(corePath), "config.toml"))
	if err != nil || string(marker) != "[layout]\nserving = \"legacy-serving\"\n" {
		t.Fatalf("rolled-back marker = %q, err = %v", marker, err)
	}

	legacy, _, err := env.openService()
	if err != nil {
		t.Fatal(err)
	}
	defer legacy.Close()
	stored, err := legacy.Store(t.Context(), service.StoreRequest{
		Layer:      "handoff",
		Content:    "Synthetic post-rollback destination write\nbranch: fixture\ndone: recorded\nstate: stored\nnext: continue\n",
		Authorship: service.Authorship{Agent: "claude", Model: "sonnet", Surface: service.SurfaceCLI},
	})
	if err != nil {
		t.Fatal(err)
	}
	verification := openLayoutDatabase(t, opsPath)
	defer verification.Close()
	var storedContent string
	if err := verification.QueryRow("SELECT content FROM memories WHERE id = ?", stored.ID).Scan(&storedContent); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(storedContent, "Synthetic post-rollback destination write") {
		t.Fatalf("post-rollback write = %q", storedContent)
	}
}

func openLayoutDatabase(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func TestCutoverCLIRejectsUnfinishedDestinationCustody(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	corePath := filepath.Join(home, "roca.db")
	env := seedLayoutCore(t, corePath, `INSERT INTO memories(layer, content, origin)
		VALUES ('project', 'cutover readiness marker', 'agent')`)
	if code, err := executeWithEnv(env, []string{"--db-path", corePath, "migrate"}, nil); err != nil || code != 0 {
		t.Fatalf("explicit migration: code=%d err=%v", code, err)
	}
	selectCutoverWithoutSnapshots(t, env)
	for _, probe := range []struct{ plugin, database, migration string }{
		{rocaops.Name, rocaops.DatabaseFilename, "data2-memory-custody"},
		{rocacorpus.Name, rocacorpus.DatabaseFilename, "corpus-archive-reconciliation-v1"},
	} {
		t.Run(probe.plugin, func(t *testing.T) {
			db := openLayoutDatabase(t, filepath.Join(home, ".roca", "plugins", probe.plugin, probe.database))
			defer db.Close()
			if _, err := db.Exec(`UPDATE plugin_migrations SET migration_state = 'batch-in-progress' WHERE migration = ?`, probe.migration); err != nil {
				t.Fatal(err)
			}
			assertLayoutRequiresMigration(t, env)
			if _, err := db.Exec(`UPDATE plugin_migrations SET migration_state = 'verified' WHERE migration = ?`, probe.migration); err != nil {
				t.Fatal(err)
			}
		})
	}
	env.forceReadOnly = false
	svc, _, err := env.openService()
	if err != nil {
		t.Fatalf("verified open without frozen snapshots: %v", err)
	}
	if err := svc.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCutoverCLIRejectsInterruptedLegacyCustody(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	corePath := filepath.Join(home, "roca.db")
	env := seedLayoutCore(t, corePath, `CREATE TABLE garden_channels (id INTEGER PRIMARY KEY, name TEXT);
		INSERT INTO garden_channels VALUES (1, 'interrupted garden')`)
	paths, err := env.resolvePaths()
	if err != nil {
		t.Fatal(err)
	}
	root := pluginRoot(paths)
	if _, err := rocaops.Ensure(root, pluginExecutableDir(paths), env.build.Version); err != nil {
		t.Fatal(err)
	}
	opsPath := filepath.Join(root, rocaops.Name, rocaops.DatabaseFilename)
	ops := openLayoutDatabase(t, opsPath)
	defer ops.Close()
	if _, err := ops.Exec(`CREATE TRIGGER interrupt_legacy BEFORE INSERT ON legacy_records
		BEGIN SELECT RAISE(ABORT, 'synthetic legacy interruption'); END`); err != nil {
		t.Fatal(err)
	}
	if code, err := executeWithEnv(env, []string{"--db-path", corePath, "migrate"}, nil); code == 0 || err == nil || !strings.Contains(err.Error(), "synthetic legacy interruption") {
		t.Fatalf("legacy interruption: code=%d err=%v", code, err)
	}
	if ready, err := rocaops.MemoryCustodyCutoverEligible(t.Context(), opsPath); err != nil || !ready {
		t.Fatalf("DATA-2 readiness: ready=%t err=%v", ready, err)
	}
	if ready, err := corpusarchive.CutoverEligible(t.Context(), filepath.Join(root, rocacorpus.Name, rocacorpus.DatabaseFilename)); err != nil || !ready {
		t.Fatalf("DATA-3 readiness: ready=%t err=%v", ready, err)
	}
	snapshots := selectCutoverWithoutSnapshots(t, env)
	assertLayoutRequiresMigration(t, env)
	var count int
	if err := ops.QueryRow(`SELECT COUNT(*) FROM legacy_records`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("ordinary open imported records: count=%d err=%v", count, err)
	}
	if err := os.Rename(snapshots+"-offline", snapshots); err != nil {
		t.Fatal(err)
	}
	if _, err := ops.Exec(`DROP TRIGGER interrupt_legacy`); err != nil {
		t.Fatal(err)
	}
	env.forceReadOnly = false
	if code, err := executeWithEnv(env, []string{"--db-path", corePath, "migrate"}, nil); code != 0 || err != nil {
		t.Fatalf("resume legacy migration: code=%d err=%v", code, err)
	}
	if err := os.Rename(snapshots, snapshots+"-offline"); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"exec", "SELECT COUNT(*) FROM plugin_roca_ops.legacy_records"},
		{"--read-only", "exec", "SELECT COUNT(*) FROM plugin_roca_ops.legacy_records"},
	} {
		if code, err := executeWithEnv(env, append([]string{"--db-path", corePath}, args...), nil); code != 0 || err != nil {
			t.Fatalf("verified DATA-4 without snapshots: code=%d err=%v", code, err)
		}
	}
	if err := ops.QueryRow(`SELECT COUNT(*) FROM legacy_records`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("resumed legacy records: count=%d err=%v", count, err)
	}
}

// seedLayoutCore owns the legacy database setup shared by migration cases.
func seedLayoutCore(t *testing.T, corePath, seedSQL string) *cliEnv {
	t.Helper()
	core, err := store.Open(corePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ApplySchema(t.Context(), core); err != nil {
		t.Fatal(err)
	}
	if _, err := core.SQL().Exec(seedSQL); err != nil {
		t.Fatal(err)
	}
	if err := core.Close(); err != nil {
		t.Fatal(err)
	}
	return &cliEnv{dbPath: corePath, out: io.Discard, errOut: io.Discard,
		build: Build{Version: "v-test", Commit: "fixture"}}
}

func selectCutoverWithoutSnapshots(t *testing.T, env *cliEnv) string {
	t.Helper()
	paths, err := env.resolvePaths()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.Config, []byte("[layout]\nserving = \"cutover\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	snapshots := filepath.Join(paths.Backups, "data-split")
	if err := os.Rename(snapshots, snapshots+"-offline"); err != nil {
		t.Fatal(err)
	}
	return snapshots
}

func assertLayoutRequiresMigration(t *testing.T, env *cliEnv) {
	t.Helper()
	for _, readOnly := range []bool{false, true} {
		env.forceReadOnly = readOnly
		svc, _, err := env.openService()
		if svc != nil {
			svc.Close()
		}
		if err == nil || !strings.Contains(err.Error(), "roca migrate") {
			t.Fatalf("unfinished custody readOnly=%t: %v", readOnly, err)
		}
	}
}
