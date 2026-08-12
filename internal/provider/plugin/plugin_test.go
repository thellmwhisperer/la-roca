package plugin_test

import (
	"context"
	"database/sql"
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
	if _, err := plugin.Validate(context.Background(), byName["lying"]); err == nil ||
		!strings.Contains(err.Error(), "outstanding_cents") {
		t.Fatalf("lying semantic layer passed with %v", err)
	}
}

func TestSemanticRelevanceIsStableAndBounded(t *testing.T) {
	candidates := []plugin.Descriptor{
		{Name: "broad", Semantic: plugin.Semantic{Description: "receipts and purchases"}},
		{Name: "exact", Semantic: plugin.Semantic{Questions: []string{"Which receipts were recorded?"}}},
		{Name: "unrelated", Semantic: plugin.Semantic{Description: "weather observations"}},
	}
	selected, omitted := plugin.Relevant("Which receipts were recorded?", candidates, 1)
	if len(selected) != 1 || selected[0].Name != "exact" {
		t.Fatalf("selected = %+v", selected)
	}
	if len(omitted) != 1 || omitted[0].Name != "broad" {
		t.Fatalf("omitted = %+v", omitted)
	}
}

func TestCommonQuestionFramingDoesNotRouteAnUnrelatedPlugin(t *testing.T) {
	candidates := []plugin.Descriptor{{
		Name: "weather", Semantic: plugin.Semantic{
			Description: "Weather observations and forecasts.",
			Questions:   []string{"Which weather observations were recorded?"},
		},
	}}
	selected, _ := plugin.Relevant("Which receipts were recorded?", candidates, 10)
	if len(selected) != 0 {
		t.Fatalf("unrelated plugin selected through framing words: %+v", selected)
	}
}

func TestSchemaNamesThatNormalizeTheSameRemainUnambiguous(t *testing.T) {
	root := installedFixtures(t, "well-formed")
	original := filepath.Join(root, "well-formed")
	for _, name := range []string{"a-b", "a_b"} {
		destination := filepath.Join(root, name)
		if err := os.Mkdir(destination, 0o700); err != nil {
			t.Fatal(err)
		}
		for _, file := range []string{plugin.SemanticFilename, "plugin.db"} {
			raw, err := os.ReadFile(filepath.Join(original, file))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(destination, file), raw, 0o600); err != nil {
				t.Fatal(err)
			}
		}
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
