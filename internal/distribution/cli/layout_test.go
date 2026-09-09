package cli

import (
	"database/sql"
	"github.com/thellmwhisperer/la-roca/internal/provider/query"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/data"
	"github.com/thellmwhisperer/la-roca/internal/distribution/rocacorpus"
	"github.com/thellmwhisperer/la-roca/internal/distribution/rocacron"
	"github.com/thellmwhisperer/la-roca/internal/distribution/rocaops"
	"github.com/thellmwhisperer/la-roca/internal/provider/service"
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
	seedLayoutMemory(t, corePath, "Synthetic shadow custody marker")
	if err := os.WriteFile(filepath.Join(filepath.Dir(corePath), "config.toml"),
		[]byte("[layout]\nserving = \"shadow-equal\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	env := migratedCLIEnv(t, corePath)
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
	seedLayoutMemory(t, corePath, "cutover readiness marker")
	env := migratedCLIEnv(t, corePath)
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
	for _, probe := range []struct{ plugin, database, migration string }{
		{rocaops.Name, rocaops.DatabaseFilename, "data2-memory-custody"},
		{rocacorpus.Name, rocacorpus.DatabaseFilename, "corpus-archive-reconciliation-v1"},
		{rocaops.Name, rocaops.DatabaseFilename, "data4-legacy-records"},
		{rocacorpus.Name, rocacorpus.DatabaseFilename, "data4-legacy-flow-patterns"},
		{rocacron.Name, rocacron.DatabaseFilename, "data4-legacy-runs"},
		{rocacron.Name, rocacron.DatabaseFilename, "data4-legacy-run-logs"},
	} {
		t.Run(probe.plugin, func(t *testing.T) {
			db := openLayoutDatabase(t, filepath.Join(home, ".roca", "plugins", probe.plugin, probe.database))
			defer db.Close()
			if _, err := db.Exec(`UPDATE plugin_migrations SET migration_state = 'batch-in-progress' WHERE migration = ?`, probe.migration); err != nil {
				t.Fatal(err)
			}
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

func seedLayoutMemory(t *testing.T, path, content string) {
	t.Helper()
	core := openLayoutDatabase(t, path)
	defer core.Close()
	if _, err := core.Exec(data.Schema+`INSERT INTO memories(id, layer, content, origin) VALUES(29, 'project', ?, 'agent')`, content); err != nil {
		t.Fatal(err)
	}

}

func migratedCLIEnv(t *testing.T, corePath string) *cliEnv {
	t.Helper()
	env := &cliEnv{dbPath: corePath, out: io.Discard, errOut: io.Discard, build: Build{Version: "v-test", Commit: "fixture"}}
	if code, err := executeWithEnv(env, []string{"--db-path", corePath, "migrate"}, nil); err != nil || code != 0 {
		t.Fatalf("explicit migration: code=%d err=%v", code, err)
	}
	return env
}
