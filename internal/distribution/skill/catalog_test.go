package skill_test

import (
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/distribution/skill"
	"github.com/thellmwhisperer/la-roca/internal/provider/plugin"
)

// catalogFixtureDatabases is one unmistakably synthetic plugin database with
// every fragment the catalog composes: a description, database-level questions,
// and one table with its own description, columns and questions.
func catalogFixtureDatabases() []plugin.Database {
	return []plugin.Database{{
		Descriptor: plugin.Descriptor{
			Name: "synth-corpus", DatabaseName: "corpus", Schema: "plugin_synth_corpus",
			Semantic: plugin.Semantic{
				Description: "Synthetic perennial harvest for the catalog fixture.",
				Questions:   []string{"What did the synthetic corpus record?"},
			},
		},
		Tables: []plugin.Table{{
			Name:        "sessions",
			Columns:     []string{"session_id", "title"},
			Description: "Synthetic conversation sessions.",
			Questions:   []string{"Which synthetic sessions exist?"},
		}},
	}}
}

func catalogFixture() string { return skill.CatalogBody(catalogFixtureDatabases(), nil) }

// The catalog is a lazy-loaded second skill: its frontmatter must send agents
// back to query and explore unless they need exact SQL, and its body must carry
// the fragments the query catalog composes — what each database knows, its
// tables under the alias SQL reaches them by, and their example questions.
func TestCatalogBodyComposesFragmentsAndStatesTheHierarchy(t *testing.T) {
	cases := []struct {
		name      string
		databases []plugin.Database
		notes     []string
		want      []string
	}{
		{
			name:      "a validated plugin database",
			databases: catalogFixtureDatabases(),
			want: []string{
				"name: " + skill.CatalogName,
				"Load it only when you\n  need to know which tables or domains exist",
				"This catalog is for authors of `roca exec` SELECTs",
				"`roca query` and `roca explore` are last resort",
				"## synth-corpus — corpus (alias plugin_synth_corpus)",
				"Synthetic perennial harvest for the catalog fixture.",
				"- What did the synthetic corpus record?",
				"### sessions · plugin_synth_corpus.sessions",
				"Synthetic conversation sessions.",
				"Columns: session_id, title",
				"- Which synthetic sessions exist?",
			},
		},
		{
			name: "an installation without plugin databases still teaches the hierarchy",
			want: []string{
				"name: " + skill.CatalogName,
				"This catalog is for authors of `roca exec` SELECTs",
				"No plugin databases installed",
			},
		},
		{
			name:  "a database that cannot serve a query is named, not omitted",
			notes: []string{"plugin synth-broken is unavailable: semantic layer does not match its database"},
			want: []string{
				"## Not currently queryable",
				"- plugin synth-broken is unavailable: semantic layer does not match its database",
			},
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			body := skill.CatalogBody(test.databases, test.notes)
			for _, needle := range test.want {
				if !strings.Contains(body, needle) {
					t.Errorf("catalog body missing %q:\n%s", needle, body)
				}
			}
		})
	}
}

// A plugin that declares several databases gets one section per database, each
// under its own alias, and a single-database plugin does not repeat the
// database name the plugin name already states.
func TestCatalogHeadingsNameDatabasesAndAliases(t *testing.T) {
	shared := plugin.Descriptor{
		Name: "synth-twin", DatabaseName: "alpha", Schema: "plugin_synth_alpha",
		Semantic: plugin.Semantic{Description: "Alpha synthetic database."},
	}
	beta := shared
	beta.DatabaseName, beta.Schema = "beta", "plugin_synth_beta"
	beta.Semantic = plugin.Semantic{Description: "Beta synthetic database."}
	single := plugin.Database{
		Descriptor: plugin.Descriptor{
			Name: "synth-solo", DatabaseName: "solo", Schema: "plugin_synth_solo",
			Semantic: plugin.Semantic{Description: "A synthetic single-database plugin."},
		},
	}
	body := skill.CatalogBody([]plugin.Database{
		{Descriptor: shared}, {Descriptor: beta}, single,
	}, nil)
	for _, heading := range []string{
		"## synth-twin — alpha (alias plugin_synth_alpha)",
		"## synth-twin — beta (alias plugin_synth_beta)",
		"## synth-solo — solo (alias plugin_synth_solo)",
	} {
		if !strings.Contains(body, heading) {
			t.Errorf("catalog heading missing %q:\n%s", heading, body)
		}
	}
}
