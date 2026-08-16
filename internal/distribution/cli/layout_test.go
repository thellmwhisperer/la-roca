package cli

import (
	"database/sql"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/distribution/rocaops"
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

func TestShadowCLIOrchestratesCustodyBeforeComparingTheHub(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	corePath := filepath.Join(home, "selected", "roca.db")
	if err := os.MkdirAll(filepath.Dir(corePath), 0o700); err != nil {
		t.Fatal(err)
	}
	core, err := store.Open(corePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ApplySchema(t.Context(), core); err != nil {
		t.Fatal(err)
	}
	if _, err := core.SQL().Exec(`INSERT INTO memories
		(id, layer, content, origin) VALUES (29, 'project', 'Synthetic shadow custody marker', 'agent')`); err != nil {
		t.Fatal(err)
	}
	if err := core.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(corePath), "config.toml"),
		[]byte("[layout]\nserving = \"shadow-equal\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	env := &cliEnv{dbPath: corePath, out: io.Discard, errOut: io.Discard,
		build: Build{Version: "v-test", Commit: "fixture"}}
	svc, _, err := env.openService()
	if err != nil {
		t.Fatal(err)
	}
	result, err := svc.Exec(t.Context(), service.ExecRequest{SQL: "SELECT id, content FROM memories LIMIT 5"})
	if err != nil || result.RowCount != 1 {
		t.Fatalf("shadow result = %+v, err = %v", result, err)
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
	result, err = svc.Exec(t.Context(), service.ExecRequest{SQL: "SELECT id, content FROM memories LIMIT 5"})
	if err != nil || result.Rows[0]["content"] != "Synthetic shadow custody marker" {
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
		Layer: "handoff", Content: "Synthetic post-rollback destination write",
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
	if storedContent != "Synthetic post-rollback destination write" {
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
