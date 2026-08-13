package cli

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/distribution/rocaops"
	"github.com/thellmwhisperer/la-roca/internal/store"
	_ "modernc.org/sqlite"
)

func TestRocaOpsFeatureRoutesTheExistingCLIStoreContractAndDrainsOnlyOnCommand(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dataDir := filepath.Join(home, ".roca")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dataDir, "roca.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "config.toml"),
		[]byte("[features]\nroca_ops = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	run := func(args ...string) (string, error) {
		var out, warnings strings.Builder
		env := &cliEnv{
			build: Build{Version: "v-test", Commit: "test"}, out: &out, errOut: &warnings,
			skipReconciliation: true,
		}
		_, err := executeWithEnv(env, args, nil)
		return out.String() + warnings.String(), err
	}
	if output, err := run("store", "--db-path", dbPath, "--layer", "handoff",
		"--content", "synthetic CLI extraction marker", "--agent", "codex", "--model", "gpt-test",
		"--metadata", `{"expires_at":"2026-08-12T00:00:00Z"}`); err != nil {
		t.Fatalf("store: %v\n%s", err, output)
	}

	core, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	var coreRows int
	if err := core.QueryRow("SELECT COUNT(*) FROM memories").Scan(&coreRows); err != nil {
		core.Close()
		t.Fatal(err)
	}
	core.Close()
	if coreRows != 0 {
		t.Fatalf("CLI store left %d extracted rows in core", coreRows)
	}

	opsPath := filepath.Join(dataDir, "plugins", rocaops.Name, rocaops.DatabaseFilename)
	if output, err := run("ops", "drain", "--db-path", dbPath,
		"--before", "2026-08-13T00:00:00Z"); err != nil {
		t.Fatalf("drain: %v\n%s", err, output)
	}
	ops, err := sql.Open("sqlite", opsPath)
	if err != nil {
		t.Fatal(err)
	}
	defer ops.Close()
	var remaining int
	if err := ops.QueryRow("SELECT COUNT(*) FROM memories").Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Fatalf("explicit drain left %d expired rows", remaining)
	}
}
