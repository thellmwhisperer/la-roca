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

	before, err := os.Stat(descriptor.Database)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rocaops.Ensure(root, bin, "v-next"); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(descriptor.Database)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("the bundled update replaced the custody database whoever holds it open is writing to")
	}
	updated, err := plugininstall.ReadManifest(directory)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Version != "v-next" {
		t.Fatalf("in-place update left the manifest at %q", updated.Version)
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

	if _, err := rocaops.Ensure(root, "", "v-later"); err == nil {
		t.Fatal("a refused bundled update was reported as a successful one")
	}
}

func TestEnsureDoesNotTouchTheDatabaseWhenTheInstalledVersionMatches(t *testing.T) {
	root := filepath.Join(t.TempDir(), "plugins")
	bin := filepath.Join(t.TempDir(), "bin")
	if _, err := rocaops.Ensure(root, bin, "v-test"); err != nil {
		t.Fatal(err)
	}
	writer, err := sql.Open("sqlite", filepath.Join(root, rocaops.Name, rocaops.DatabaseFilename))
	if err != nil {
		t.Fatal(err)
	}
	holding, err := writer.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := holding.Exec(`INSERT INTO memories (layer, content, origin)
		VALUES ('handoff', 'synthetic resident writer', 'agent')`); err != nil {
		t.Fatal(err)
	}
	if _, err := rocaops.Ensure(root, bin, "v-test"); err != nil {
		t.Fatalf("the schema check fought a resident writer for the write lock: %v", err)
	}
	if err := holding.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	database := filepath.Join(root, rocaops.Name, rocaops.DatabaseFilename)
	sentinel := []byte("custody database bytes are not an install-time migration target")
	if err := os.WriteFile(database, sentinel, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := rocaops.Ensure(root, bin, "v-test"); err != nil {
		t.Fatalf("same-version ensure inspected or rewrote the custody database: %v", err)
	}
	got, err := os.ReadFile(database)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(sentinel) {
		t.Fatalf("same-version ensure changed the custody database: %q", got)
	}
}
