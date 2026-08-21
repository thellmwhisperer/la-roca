package plugin_test

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/provider/plugin"
	_ "modernc.org/sqlite"
)

func TestFixturePluginsDiscoverValidateAndDeclareCustody(t *testing.T) {
	root := installedFixtures(t, "well-formed", "lying", "custodial")
	found, warnings := plugin.Discover(root)
	if len(warnings) != 0 || len(found) != 3 {
		t.Fatalf("discovery = %d plugins, warnings %v", len(found), warnings)
	}

	byName := make(map[string]plugin.Descriptor, len(found))
	for _, candidate := range found {
		byName[candidate.Name] = candidate
	}
	if !byName["custodial"].Semantic.Custody {
		t.Fatal("the custodial fixture lost its custody declaration")
	}
	if _, err := plugin.Validate(context.Background(), byName["well-formed"]); err != nil {
		t.Fatalf("validate well-formed: %v", err)
	}
	db, err := sql.Open("sqlite", byName["well-formed"].Database)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"dedup_runs", "memory_id_remaps", "session_id_remaps",
		"exchange_id_remaps", "thinking_block_id_remaps"} {
		if _, err := db.Exec("CREATE TABLE " + name + " (fixture TEXT)"); err != nil {
			db.Close()
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.Validate(context.Background(), byName["well-formed"]); err != nil {
		t.Fatalf("dedup maintenance tables invalidated the semantic layer: %v", err)
	}
	if _, err := plugin.Validate(context.Background(), byName["lying"]); err == nil ||
		!strings.Contains(err.Error(), "outstanding_cents") {
		t.Fatalf("lying semantic layer passed with %v", err)
	}
}

func TestValidationPreservesInspectedFTS5Kind(t *testing.T) {
	path := filepath.Join(t.TempDir(), "search.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		CREATE VIRTUAL TABLE documents_search USING fts5(body);
		CREATE TABLE receipts_fts (body TEXT);
	`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	database, err := plugin.Validate(t.Context(), plugin.Descriptor{
		Name: "synthetic", Database: path,
		Semantic: plugin.Semantic{Tables: []plugin.SemanticTable{
			{Name: "documents_search", Columns: []string{"body"}},
			{Name: "receipts_fts", Columns: []string{"body"}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	kinds := make(map[string]bool)
	for _, table := range database.Tables {
		kinds[table.Name] = table.FTS5
	}
	if !kinds["documents_search"] || kinds["receipts_fts"] {
		t.Fatalf("inspected FTS5 kinds = %v", kinds)
	}
}

func TestImmutableValidationFreezesCommittedWALFramesInMemory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "live.db")
	writer, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if _, err := writer.Exec(`PRAGMA journal_mode=WAL; PRAGMA wal_autocheckpoint=0;
		CREATE TABLE rows (id INTEGER PRIMARY KEY, value TEXT);
		INSERT INTO rows VALUES (1, 'first')`); err != nil {
		t.Fatal(err)
	}
	database, err := plugin.ValidateImmutable(t.Context(), plugin.Descriptor{
		Name: "live", Database: path, Schema: "live",
		Semantic: plugin.Semantic{Tables: []plugin.SemanticTable{{
			Name: "rows", Columns: []string{"id", "value"},
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if _, err := writer.Exec(`INSERT INTO rows VALUES (2, 'second')`); err != nil {
		t.Fatal(err)
	}
	reader, err := sql.Open("sqlite", database.ReadOnlyURI())
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	var count int
	if err := reader.QueryRow("SELECT COUNT(*) FROM rows").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("snapshot rows = %d, want 1", count)
	}
}

func TestSemanticRelevanceIsStableAndBounded(t *testing.T) {
	candidates := []plugin.Descriptor{
		{Name: "broad", Semantic: plugin.Semantic{Description: "receipts and purchases"}},
		{Name: "exact", Semantic: plugin.Semantic{Questions: []string{"Which receipts were recorded?"}}},
		{Name: "unrelated", Semantic: plugin.Semantic{Description: "weather observations"}},
	}
	selected := plugin.Relevant("Which receipts were recorded?", candidates)
	if len(selected) != 2 || selected[0].Name != "exact" || selected[1].Name != "broad" {
		t.Fatalf("selected = %+v", selected)
	}
}

func TestCommonQuestionFramingDoesNotRouteAnUnrelatedPlugin(t *testing.T) {
	candidates := []plugin.Descriptor{{
		Name: "weather", Semantic: plugin.Semantic{
			Description: "Weather observations and forecasts.",
			Questions:   []string{"Which weather observations were recorded?"},
		},
	}}
	selected := plugin.Relevant("Which receipts were recorded?", candidates)
	if len(selected) != 0 {
		t.Fatalf("unrelated plugin selected through framing words: %+v", selected)
	}
}

func TestSemanticLayerDeclaresResidentOrOnDemandAttachment(t *testing.T) {
	for _, testCase := range []struct {
		name        string
		declaration string
		want        plugin.Attachment
		wantError   bool
	}{
		{name: "default remains on demand", want: plugin.AttachmentOnDemand},
		{name: "explicit on demand", declaration: "attachment: on-demand\n", want: plugin.AttachmentOnDemand},
		{name: "resident", declaration: "attachment: resident\n", want: plugin.AttachmentResident},
		{name: "unknown mode", declaration: "attachment: sometimes\n", wantError: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			directory := filepath.Join(t.TempDir(), "synthetic")
			if err := os.MkdirAll(directory, 0o700); err != nil {
				t.Fatal(err)
			}
			semantic := "version: 1\n" + testCase.declaration + `description: Synthetic records.
questions: ["Which synthetic records exist?"]
tables:
  - name: records
    description: Synthetic records.
    columns: [id, value]
`
			if err := os.WriteFile(filepath.Join(directory, plugin.SemanticFilename), []byte(semantic), 0o600); err != nil {
				t.Fatal(err)
			}
			db, err := sql.Open("sqlite", filepath.Join(directory, "plugin.db"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec("CREATE TABLE records (id INTEGER PRIMARY KEY, value TEXT)"); err != nil {
				db.Close()
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}

			descriptor, err := plugin.Inspect("synthetic", directory)
			if testCase.wantError {
				if err == nil {
					t.Fatalf("attachment %q was accepted", testCase.declaration)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if descriptor.Semantic.Attachment != testCase.want {
				t.Fatalf("attachment = %q, want %q", descriptor.Semantic.Attachment, testCase.want)
			}
		})
	}
}

func TestSchemaNamesThatNormalizeTheSameRemainUnambiguous(t *testing.T) {
	root := installedFixtures(t, "well-formed")
	for _, name := range []string{"a-b", "a_b"} {
		copyPlugin(t, root, "well-formed", name)
	}
	found, warnings := plugin.Discover(root)
	if len(warnings) != 0 {
		t.Fatal(warnings)
	}
	var schemas []string
	for _, descriptor := range found {
		if descriptor.Name == "a-b" || descriptor.Name == "a_b" {
			schemas = append(schemas, descriptor.Schema)
		}
	}
	if len(schemas) != 2 || schemas[0] == schemas[1] {
		t.Fatalf("colliding schemas = %v", schemas)
	}
}

func TestInstallerScratchDirectoriesAreNeverDiscoveredAsPlugins(t *testing.T) {
	root := installedFixtures(t, "well-formed")
	for _, scratch := range []string{".well-formed.previous", ".install-2451"} {
		copyPlugin(t, root, "well-formed", scratch)
	}
	found, warnings := plugin.Discover(root)
	if len(warnings) != 0 {
		t.Fatalf("installer scratch produced warnings: %v", warnings)
	}
	if len(found) != 1 || found[0].Name != "well-formed" {
		t.Fatalf("discovery = %+v", found)
	}
}

func TestExecutableOnlyPackagesAreNotDataPlugins(t *testing.T) {
	root := installedFixtures(t, "well-formed")
	directory := filepath.Join(root, "synthetic-exec")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := `{"schema":1,"kind":"executable"}`
	if err := os.WriteFile(filepath.Join(directory, ".roca-plugin.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	found, warnings := plugin.Discover(root)
	if len(warnings) != 0 || len(found) != 1 || found[0].Name != "well-formed" {
		t.Fatalf("executable package entered data discovery: found=%+v warnings=%v", found, warnings)
	}
}

func TestAPluginReachedThroughARelativeRootStillOpensReadOnly(t *testing.T) {
	root := installedFixtures(t, "well-formed")
	t.Chdir(filepath.Dir(root))
	found, warnings := plugin.Discover(filepath.Base(root))
	if len(warnings) != 0 || len(found) != 1 {
		t.Fatalf("discovery = %+v, warnings %v", found, warnings)
	}
	database, err := plugin.Validate(t.Context(), found[0])
	if err != nil {
		t.Fatalf("validate under a relative root: %v", err)
	}
	// An attachment has to reach the same file, and the first segment of a
	// relative path would otherwise be read as the URI authority.
	handle, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Close()
	if _, err := handle.ExecContext(t.Context(),
		"ATTACH DATABASE ? AS probe", database.ReadOnlyURI()); err != nil {
		t.Fatalf("attach under a relative root: %v", err)
	}
	// The read-only open runs while another process may be recovering or writing
	// the same plugin database. It must wait out the lock, not fail the open with
	// "database is locked".
	uri, err := url.Parse(database.ReadOnlyURI())
	if err != nil {
		t.Fatalf("read-only URI does not parse: %v", err)
	}
	if uri.Query().Get("_pragma") != "busy_timeout(15000)" {
		t.Fatalf("read-only URI does not wait out a concurrent writer: %s", database.ReadOnlyURI())
	}
}

func TestASemanticLayerMayNotClaimTheProvenanceColumn(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "shadow")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	semantic := "version: 1\ndescription: Synthetic rows that shadow provenance.\n" +
		"questions:\n  - \"Which synthetic rows exist?\"\ntables:\n  - name: rows\n" +
		"    description: Synthetic rows.\n    columns: [id, " + plugin.ProvenanceColumn + "]\n"
	if err := os.WriteFile(filepath.Join(directory, plugin.SemanticFilename), []byte(semantic), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.Inspect("shadow", directory); err == nil ||
		!strings.Contains(err.Error(), "reserved column") {
		t.Fatalf("a semantic layer claimed the provenance column: %v", err)
	}
}

func copyPlugin(t *testing.T, root, from, to string) {
	t.Helper()
	destination := filepath.Join(root, to)
	if err := os.Mkdir(destination, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{plugin.SemanticFilename, "plugin.db"} {
		raw, err := os.ReadFile(filepath.Join(root, from, file))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(destination, file), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func installedFixtures(t *testing.T, names ...string) string {
	t.Helper()
	root := t.TempDir()
	for _, name := range names {
		source := filepath.Join("..", "..", "..", "testdata", "plugin-standard", name)
		destination := filepath.Join(root, name)
		if err := os.Mkdir(destination, 0o700); err != nil {
			t.Fatal(err)
		}
		semantic, err := os.ReadFile(filepath.Join(source, plugin.SemanticFilename))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(destination, plugin.SemanticFilename), semantic, 0o600); err != nil {
			t.Fatal(err)
		}
		ddl, err := os.ReadFile(filepath.Join(source, "schema.sql"))
		if err != nil {
			t.Fatal(err)
		}
		db, err := sql.Open("sqlite", filepath.Join(destination, "plugin.db"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(string(ddl)); err != nil {
			db.Close()
			t.Fatal(err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != len(names) {
		t.Fatalf("fixtures = %v, err %v", entries, err)
	}
	actual := make([]string, 0, len(entries))
	for _, entry := range entries {
		actual = append(actual, entry.Name())
	}
	for _, name := range names {
		if !slices.Contains(actual, name) {
			t.Fatalf("fixture %q was not installed", name)
		}
	}
	return root
}
