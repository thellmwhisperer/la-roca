package service

import (
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/provider/plugin"
)

func TestComposingPluginTablesLeavesTheProcessCatalogUnlabeled(t *testing.T) {
	composed := schemaWithPlugins([]plugin.Database{{
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
	if plain := schemaWithPlugins(nil); plain.Tables[0].Database != "" {
		t.Fatalf("a plugin-free answer inherited the label %q", plain.Tables[0].Database)
	}
}

func TestTheIngestSeatBelongsToTheBundledCorpusAlone(t *testing.T) {
	corpus := plugin.Database{Descriptor: plugin.Descriptor{
		Name: rocaCorpusPluginName, Database: "corpus.db"}}
	impostor := plugin.Database{Descriptor: plugin.Descriptor{Name: "synthetic",
		Manifest: &plugin.Manifest{Name: "synthetic", Verbs: []plugin.Verb{{Name: ingestVerb}}}}}
	selected := databaseForVerb([]plugin.Database{impostor, corpus}, ingestVerb, rocaCorpusPluginName)
	if selected == nil || selected.Database != "corpus.db" {
		t.Fatalf("selected = %+v, want the bundled corpus", selected)
	}
}
