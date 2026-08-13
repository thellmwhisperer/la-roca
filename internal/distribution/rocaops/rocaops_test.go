package rocaops_test

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/distribution/plugininstall"
	"github.com/thellmwhisperer/la-roca/internal/distribution/rocaops"
	"github.com/thellmwhisperer/la-roca/internal/provider/plugin"
	_ "modernc.org/sqlite"
)

func TestEnsureInstallsTheBundledResidentDataOnlyPluginAndPreservesItsDatabase(t *testing.T) {
	root := filepath.Join(t.TempDir(), "plugins")
	bin := filepath.Join(t.TempDir(), "bin")
	result, err := rocaops.Ensure(root, bin, "v-test")
	if err != nil {
		t.Fatal(err)
	}
	if result.Name != rocaops.Name || result.Risk != plugininstall.DataOnly || result.Executable != "" {
		t.Fatalf("installed bundle = %+v", result)
	}

	directory := filepath.Join(root, rocaops.Name)
	descriptor, err := plugin.Inspect(rocaops.Name, directory)
	if err != nil {
		t.Fatal(err)
	}
	if descriptor.Semantic.Attachment != plugin.AttachmentResident || !descriptor.Semantic.Custody {
		t.Fatalf("semantic contract = %+v", descriptor.Semantic)
	}
	if _, err := os.Stat(filepath.Join(directory, "roca-"+rocaops.Name)); !os.IsNotExist(err) {
		t.Fatalf("bundled data plugin carries an executable: %v", err)
	}

	db, err := sql.Open("sqlite", descriptor.Database)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO memories
		(layer, content, origin, source_agent, source_model, source_surface)
		VALUES ('handoff', 'preserved marker', 'agent', 'codex', 'gpt-test', 'cli')`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := rocaops.Ensure(root, bin, "v-next"); err != nil {
		t.Fatal(err)
	}
	db, err = sql.Open("sqlite", descriptor.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var content string
	if err := db.QueryRow("SELECT content FROM memories WHERE content = 'preserved marker'").Scan(&content); err != nil {
		t.Fatalf("bundled update did not preserve the owned database: %v", err)
	}
	var expiresAt int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('memories') WHERE name = 'expires_at'`).Scan(&expiresAt); err != nil {
		t.Fatal(err)
	}
	if expiresAt != 1 {
		t.Fatal("the expiry mechanism is absent from the bundled schema")
	}

	if err := os.MkdirAll(filepath.Join(root, "."+rocaops.Name+".previous"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := rocaops.Ensure(root, bin, "v-later"); err == nil {
		t.Fatal("a refused bundled update was reported as a successful one")
	}
}
