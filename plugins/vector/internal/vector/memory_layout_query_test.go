package vector

import (
	"context"
	"path/filepath"
	"testing"
)

func TestVectorQueryIgnoresWithdrawnMemoryTables(t *testing.T) {
	federation, _, opsPath, embedder := federationFixture(t)
	mutateSourceDatabase(t, opsPath, `
		CREATE TABLE memory_records(id INTEGER PRIMARY KEY,content TEXT);
		WITH RECURSIVE ids(id) AS (VALUES(1) UNION ALL SELECT id+1 FROM ids WHERE id<128)
		INSERT INTO memory_records SELECT id,'alpha orchard compass note ' || id FROM ids;
		CREATE VIEW core_memories AS SELECT id,content FROM memory_records;`)
	registryPath := filepath.Join(federation.PluginRoot, vectorRegistryFilename)
	registry, err := loadVectorRegistry(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	for index := range registry.Databases {
		if registry.Databases[index].Database == "ops" {
			registry.Databases[index].Tables = append(registry.Databases[index].Tables,
				vectorTable{Name: "core_memories", IDColumn: "id", TextColumns: []string{"content"}})
		}
	}
	writeRegistry(t, federation.PluginRoot, registry)
	cutover, err := LoadFederation(federation.Core, federation.PluginRoot, DefaultModel, "v-test", embedder, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := cutover.Ingest(ctx, ""); err != nil {
		t.Fatal(err)
	}
	before, err := cutover.Query(ctx, "alpha", 1, "ops")
	if err != nil || len(before.Results) != 1 || before.Results[0].Table != "core_memories" {
		t.Fatalf("cutover query: result=%+v err=%v", before, err)
	}
	for index := range registry.Databases {
		if registry.Databases[index].Database == "ops" {
			registry.Databases[index].Tables = registry.Databases[index].Tables[:1]
		}
	}
	writeRegistry(t, federation.PluginRoot, registry)
	legacy, err := LoadFederation(federation.Core, federation.PluginRoot, DefaultModel, "v-test", embedder, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, expanded := range []bool{false, true} {
		var result FederatedQuery
		if expanded {
			result, err = legacy.QueryExpanded(ctx, "alpha", 1, "ops", 0)
		} else {
			result, err = legacy.Query(ctx, "alpha", 1, "ops")
		}
		if err != nil || !result.VectorExecuted || len(result.Results) == 0 {
			t.Fatalf("legacy query expanded=%t: result=%+v err=%v", expanded, result, err)
		}
		for _, hit := range result.Results {
			if hit.Database != "ops" || hit.Table != "memories" ||
				(hit.Text != "Operational decision" && hit.Text != "Temporary handoff") {
				t.Fatalf("legacy query returned a withdrawn memory: %+v", hit)
			}
		}
	}
}
