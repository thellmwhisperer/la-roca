package service

import (
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/provider/plugin"
	"github.com/thellmwhisperer/la-roca/internal/provider/query"
)

func TestComposingPluginTablesLeavesTheProcessCatalogUnlabeled(t *testing.T) {
	composed := schemaWithPlugins(true, []plugin.Database{{
		Descriptor: plugin.Descriptor{Name: "synthetic", Schema: "plugin_synthetic",
			Semantic: plugin.Semantic{Description: "Synthetic receipts."}},
		Tables: []plugin.Table{{Name: "receipts", Columns: []string{"id"}, Description: "One receipt."}},
	}})
	if composed.Tables[0].Database != "core" {
		t.Fatalf("composed core table = %+v, want the core label", composed.Tables[0])
	}
	for _, table := range theModelsSchema().Tables {
		if table.Database != "" {
			t.Fatalf("table %s kept the label %q in the shared catalog", table.Name, table.Database)
		}
	}
	if plain := schemaWithPlugins(true, nil); plain.Tables[0].Database != "" {
		t.Fatalf("a plugin-free answer inherited the label %q", plain.Tables[0].Database)
	}
}

func TestComposedPluginSchemaTeachesQueryableFTSColumns(t *testing.T) {
	composed := schemaWithPlugins(true, []plugin.Database{{
		Descriptor: plugin.Descriptor{Name: "roca-corpus", Schema: "plugin_roca_corpus",
			Semantic: plugin.Semantic{Description: "Harvested transcripts."}},
		Tables: []plugin.Table{
			{Name: "exchanges", Columns: []string{"id", "session_id", "human_text", "agent_text", "human_timestamp"}},
			{Name: "exchanges_fts", Columns: []string{"human_text", "agent_text"},
				Description: "Accent-insensitive full-text index over harvested human and agent exchange text.", FTS5: true},
		},
	}})
	prompt := query.SQLSystemPrompt(composed, nil, nil)
	for _, needle := range []string{
		"plugin_roca_corpus.exchanges_fts(human_text, agent_text)",
		"kind: FTS5 virtual table", "only the listed tables", "sqlite_master",
	} {
		if !strings.Contains(strings.ToLower(prompt), strings.ToLower(needle)) &&
			!strings.Contains(prompt, needle) {
			t.Errorf("composed schema prompt omits %q:\n%s", needle, prompt)
		}
	}
}

func TestTheIngestSeatBelongsToTheBundledCorpusAlone(t *testing.T) {
	corpus := plugin.Database{Descriptor: plugin.Descriptor{
		Name: rocaCorpusPluginName, Database: "corpus.db"}}
	impostor := plugin.Database{Descriptor: plugin.Descriptor{Name: "synthetic",
		Manifest: &plugin.Manifest{Name: "synthetic", Verbs: []plugin.Verb{{Name: IngestVerb}}}}}
	selected := databaseForVerb([]plugin.Database{impostor, corpus}, IngestVerb, rocaCorpusPluginName)
	if selected == nil || selected.Database != "corpus.db" {
		t.Fatalf("selected = %+v, want the bundled corpus", selected)
	}
}
