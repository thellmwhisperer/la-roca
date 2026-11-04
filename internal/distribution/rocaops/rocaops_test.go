package rocaops_test

import (
	"database/sql"
	"os"
	"path/filepath"
	"slices"
	"strings"
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
	if _, err := os.Stat(filepath.Join(directory, plugin.SemanticFilename)); !os.IsNotExist(err) {
		t.Fatalf("ops still ships the legacy semantic layer: %v", err)
	}
	manifest, err := plugin.ReadManifest(filepath.Join(directory, plugin.PackageFilename))
	if err != nil {
		t.Fatal(err)
	}
	registrations, err := plugin.Register(manifest)
	if err != nil {
		t.Fatal(err)
	}
	wantVerbs := map[string][]string{
		"store": {"store"},
		"query": {"query"},
		"exec":  {"exec"},
		"sql":   {"query", "--sql-only"},
	}
	if manifest.Name != rocaops.Name || manifest.Version != "v-test" || manifest.Binary != "roca" ||
		len(manifest.Databases) != 1 || manifest.Databases[0].Alias != "plugin_roca_ops" ||
		len(registrations) != len(wantVerbs) {
		t.Fatalf("ops manifest = %+v, registrations = %+v", manifest, registrations)
	}
	for _, registration := range registrations {
		want, exists := wantVerbs[registration.Name]
		if !exists || registration.MCP != "roca_"+registration.Name ||
			!slices.Equal(registration.Command, want) ||
			registration.CLI != strings.Join(want, " ") {
			t.Fatalf("ops verb registration = %+v", registration)
		}
		delete(wantVerbs, registration.Name)
	}
	if len(wantVerbs) != 0 {
		t.Fatalf("ops manifest is missing verbs: %v", wantVerbs)
	}
	descriptor, err := plugin.Inspect(rocaops.Name, directory)
	if err != nil {
		t.Fatal(err)
	}
	if descriptor.Semantic.Attachment != plugin.AttachmentResident || !descriptor.Semantic.Custody {
		t.Fatalf("semantic contract = %+v", descriptor.Semantic)
	}
	validated, validationErr := plugin.Validate(t.Context(), descriptor)
	if validationErr != nil {
		t.Fatal(validationErr)
	}
	if len(validated.Tables) != 5 {
		t.Fatalf("visible ops tables = %d, want 5", len(validated.Tables))
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

func TestEnsureChecksSchemaReadOnlyWhenTheInstalledVersionMatches(t *testing.T) {
	root, bin := installOpsFixture(t)
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
	if _, err := rocaops.Ensure(root, bin, "v-test"); err == nil {
		t.Fatal("same-version ensure accepted an unreadable schema identity")
	}
	got, err := os.ReadFile(database)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(sentinel) {
		t.Fatalf("same-version ensure changed the custody database: %q", got)
	}
}

func TestFreshOpsSchemaDoesNotSeedMemoryIds(t *testing.T) {
	root, _ := installOpsFixture(t)
	db, err := sql.Open("sqlite", filepath.Join(root, rocaops.Name, rocaops.DatabaseFilename))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var seq sql.NullInt64
	if err := db.QueryRow(`SELECT seq FROM sqlite_sequence WHERE name = 'memories'`).Scan(&seq); err != nil && err != sql.ErrNoRows {
		t.Fatal(err)
	}
	if seq.Valid && seq.Int64 != 0 {
		t.Fatalf("fresh sqlite_sequence = %+v, want unset or 0", seq)
	}
	result, err := db.Exec(`INSERT INTO memories (layer, content, origin) VALUES ('discovery', 'fresh schema id', 'agent')`)
	if err != nil {
		t.Fatal(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if id < 1 || id > 1<<53-1 {
		t.Fatalf("unspecified insert id = %d, want a short id", id)
	}
}

func TestCompactionRemapsMemoryAliasesAndPreservesOrphanSupersedes(t *testing.T) {
	root, _ := installOpsFixture(t)
	database := filepath.Join(root, rocaops.Name, rocaops.DatabaseFilename)
	db, err := sql.Open("sqlite", database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	const (
		canonicalID    = int64(1152921504606853900)
		duplicateID    = int64(1152921504606853901)
		orphanTargetID = int64(1152921504606853902)
		orphanID       = int64(1152921504606853903)
	)
	for _, row := range []struct {
		id, supersedes int64
		createdAt      string
	}{
		{canonicalID, 0, "2026-01-01 00:00:00"},
		{duplicateID, 0, "2026-01-01 00:00:01"},
		{orphanTargetID, 0, "2026-01-01 00:00:02"},
		{orphanID, orphanTargetID, "2026-01-01 00:00:03"},
	} {
		if _, err := db.Exec(`INSERT INTO memories
			(id, layer, content, origin, supersedes, created_at)
			VALUES (?, 'discovery', ?, 'agent', NULLIF(?, 0), ?)`,
			row.id, "synthetic", row.supersedes, row.createdAt); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`DELETE FROM memories WHERE id = ?`, duplicateID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM memories WHERE id = ?`, orphanTargetID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE memory_id_remaps (
		old_id INTEGER PRIMARY KEY, canonical_id INTEGER NOT NULL REFERENCES memories(id))`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO memory_id_remaps(old_id, canonical_id) VALUES (?, ?)`, duplicateID, canonicalID); err != nil {
		t.Fatal(err)
	}
	if err := rocaops.ApplySchema(database); err != nil {
		t.Fatal(err)
	}
	var compactID int64
	if err := db.QueryRow(`SELECT id FROM memories WHERE legacy_id = ?`, canonicalID).Scan(&compactID); err != nil {
		t.Fatal(err)
	}
	var aliasTarget int64
	if err := db.QueryRow(`SELECT canonical_id FROM memory_id_remaps WHERE old_id = ?`, duplicateID).Scan(&aliasTarget); err != nil {
		t.Fatal(err)
	}
	if aliasTarget != compactID {
		t.Fatalf("memory alias target = %d, want %d", aliasTarget, compactID)
	}
	var supersedes sql.NullInt64
	if err := db.QueryRow(`SELECT supersedes FROM memories WHERE legacy_id = ?`, orphanID).Scan(&supersedes); err != nil {
		t.Fatal(err)
	}
	if !supersedes.Valid || supersedes.Int64 != orphanTargetID {
		t.Fatalf("orphan supersedes = %+v, want preserved %d", supersedes, orphanTargetID)
	}
}

func installOpsFixture(t *testing.T) (string, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "plugins")
	bin := filepath.Join(t.TempDir(), "bin")
	if _, err := rocaops.Ensure(root, bin, "v-test"); err != nil {
		t.Fatal(err)
	}
	return root, bin
}
